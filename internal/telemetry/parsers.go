package telemetry

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

type ObservedSession struct {
	NeighborAddress  string
	NeighborASN      uint32
	PrefixesKnown    bool
	State            string
	ReceivedPrefixes uint64
}

var birdRoutes = regexp.MustCompile(`^Routes:\s+([0-9]+) imported(?:,|$)`)

func sessionState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "established":
		return "Established"
	case "idle", "down":
		return "Idle"
	case "connect":
		return "Connect"
	case "active":
		return "Active"
	case "opensent", "open sent":
		return "OpenSent"
	case "openconfirm", "open confirm":
		return "OpenConfirm"
	default:
		return "unknown"
	}
}

// ParseBIRD2 reads the real "show protocols all" structure. Prefixes represent
// currently imported routes, not cumulative BGP update message counts.
func ParseBIRD2(raw []byte) (map[string]ObservedSession, error) { return parseBIRD(raw, 2, false) }

func ParseBIRD3(raw []byte) (map[string]ObservedSession, error) { return parseBIRD(raw, 3, true) }

func parseBIRD(raw []byte, major int, requireVersion bool) (map[string]ObservedSession, error) {
	if len(raw) > 4<<20 {
		return nil, errors.New("BIRD output exceeds 4 MiB")
	}
	out := map[string]ObservedSession{}
	scan := bufio.NewScanner(bytes.NewReader(raw))
	scan.Buffer(make([]byte, 4096), 1<<20)
	current := ""
	header := false
	versionSeen := false
	stateColumn := 3
	identityFields := map[string]bool{}
	for scan.Scan() {
		line := scan.Text()
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "Name") && strings.Contains(trim, "Proto") {
			header = true
			for index, field := range strings.Fields(trim) {
				if field == "State" {
					stateColumn = index
				}
			}
			current = ""
			continue
		}
		if strings.HasPrefix(trim, "BIRD ") {
			if !strings.HasPrefix(trim, fmt.Sprintf("BIRD %d.", major)) {
				return nil, errors.New("BIRD control major version mismatch")
			}
			versionSeen = true
			continue
		}
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			fields := strings.Fields(line)
			current = ""
			if len(fields) >= 4 && fields[1] == "BGP" {
				current = fields[0]
				if _, exists := out[current]; exists {
					return nil, errors.New("duplicate native BIRD protocol identity")
				}
				state := "unknown"
				if stateColumn < len(fields) && strings.EqualFold(fields[stateColumn], "down") {
					state = "Idle"
				}
				for _, f := range fields[4:] {
					if v := sessionState(f); v != "unknown" {
						state = v
					}
				}
				out[current] = ObservedSession{State: state}
			}
			continue
		}
		if current == "" {
			continue
		}
		session := out[current]
		if strings.HasPrefix(trim, "BGP state:") {
			session.State = sessionState(strings.TrimSpace(strings.TrimPrefix(trim, "BGP state:")))
		}
		if strings.HasPrefix(trim, "Neighbor address:") {
			key := current + "/address"
			if identityFields[key] {
				return nil, errors.New("duplicate BIRD neighbor address")
			}
			identityFields[key] = true
			fields := strings.Fields(strings.TrimPrefix(trim, "Neighbor address:"))
			if len(fields) > 0 {
				session.NeighborAddress, _ = NormalizeNeighborAddress(fields[0])
			}
		}
		if strings.HasPrefix(trim, "Neighbor AS:") {
			key := current + "/asn"
			if identityFields[key] {
				return nil, errors.New("duplicate BIRD neighbor ASN")
			}
			identityFields[key] = true
			fields := strings.Fields(strings.TrimPrefix(trim, "Neighbor AS:"))
			if len(fields) > 0 {
				asn, err := strconv.ParseUint(fields[0], 10, 32)
				if err == nil {
					session.NeighborASN = uint32(asn)
				}
			}
		}
		if match := birdRoutes.FindStringSubmatch(trim); match != nil {
			n, err := strconv.ParseUint(match[1], 10, 32)
			if err != nil {
				return nil, err
			}
			if session.ReceivedPrefixes+n > 1<<32-1 {
				return nil, errors.New("BIRD imported prefix count exceeds uint32")
			}
			session.ReceivedPrefixes += n
			session.PrefixesKnown = true
		}
		out[current] = session
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if !header || (requireVersion && !versionSeen) {
		return nil, errors.New("BIRD protocol table header missing")
	}
	for name, value := range out {
		out[name] = verifiedObserved(value)
	}
	return out, nil
}

// ParseFRRSummary supports FRR's per-address-family summary JSON. A mapped
// neighbor occurring in several AFs must agree on state; prefix counts sum.
func ParseFRRSummary(raw []byte) (map[string]ObservedSession, error) {
	if len(raw) == 0 || len(raw) > 4<<20 {
		return nil, errors.New("FRR summary must be 1 byte..4 MiB")
	}
	var root map[string]json.RawMessage
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(&root); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("FRR output must contain exactly one JSON value")
	}
	if root == nil {
		return nil, errors.New("FRR summary must be an object")
	}
	out := map[string]ObservedSession{}
	found := false
	parsePeers := func(payload json.RawMessage) error {
		var peers map[string]struct {
			State    string      `json:"state"`
			Prefixes json.Number `json:"pfxRcd"`
			RemoteAS json.Number `json:"remoteAs"`
		}
		if err := json.Unmarshal(payload, &peers); err != nil {
			return err
		}
		for key, p := range peers {
			ip, err := netip.ParseAddr(key)
			if err != nil {
				return fmt.Errorf("invalid FRR peer address: %w", err)
			}
			var neighborASN uint32
			if p.RemoteAS != "" {
				asn, err := strconv.ParseUint(p.RemoteAS.String(), 10, 32)
				if err != nil {
					return errors.New("invalid FRR remote ASN")
				}
				neighborASN = uint32(asn)
			}
			state := sessionState(p.State)
			known := p.Prefixes != ""
			var count uint64
			if p.Prefixes != "" {
				count, err = strconv.ParseUint(p.Prefixes.String(), 10, 32)
				if err != nil {
					return errors.New("invalid FRR received prefix count")
				}
			}
			key = ip.String()
			if old, ok := out[key]; ok {
				if old.State != state || old.NeighborASN != neighborASN {
					state = "unknown"
				}
				count += old.ReceivedPrefixes
				known = known && old.PrefixesKnown
				if old.NeighborASN != neighborASN {
					neighborASN = 0
				}
			}
			if count > 1<<32-1 {
				return errors.New("FRR prefix count exceeds uint32")
			}
			out[key] = ObservedSession{State: state, ReceivedPrefixes: count, PrefixesKnown: known, NeighborAddress: key, NeighborASN: neighborASN}
		}
		found = true
		return nil
	}
	if peers, ok := root["peers"]; ok {
		if err := parsePeers(peers); err != nil {
			return nil, err
		}
	}
	for _, family := range []string{"ipv4Unicast", "ipv6Unicast"} {
		if payload, ok := root[family]; ok {
			var section map[string]json.RawMessage
			if err := json.Unmarshal(payload, &section); err != nil {
				return nil, err
			}
			if peers, ok := section["peers"]; ok {
				if err := parsePeers(peers); err != nil {
					return nil, err
				}
			}
		}
	}
	if !found {
		return nil, errors.New("FRR summary has no supported peers object")
	}
	for key, value := range out {
		out[key] = verifiedObserved(value)
	}
	return out, nil
}

func verifiedObserved(value ObservedSession) ObservedSession {
	address, err := NormalizeNeighborAddress(value.NeighborAddress)
	if err != nil || value.NeighborASN == 0 || value.NeighborASN == ^uint32(0) {
		return ObservedSession{State: "unknown"}
	}
	value.NeighborAddress = address
	return value
}
