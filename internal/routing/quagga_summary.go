package routing

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

const quaggaSummaryColumns = "Neighbor V AS MsgRcvd MsgSent TblVer InQ OutQ Up/Down State/PfxRcd"

var errQuaggaSummary = errors.New("unrecognized or incomplete Quagga unicast session summary")

func parseQuaggaSummary(raw []byte, expectedID netip.Addr, expectedASN uint32, ipv6 bool) (map[netip.Addr]QuaggaObservedSession, bool, error) {
	if len(raw) == 0 || len(raw) > quaggaObservationLimit {
		return nil, false, errQuaggaSummary
	}
	text := strings.TrimSpace(string(raw))
	if strings.ContainsAny(text, "\r\x00") {
		return nil, false, errQuaggaSummary
	}
	lines := strings.Split(text, "\n")
	if ipv6 && len(lines) >= 2 && lines[0] == "IPv6 Unicast Summary:" && lines[1] == "---------------------" {
		lines = lines[2:]
	}
	none := "No IPv4 neighbor is configured"
	if ipv6 {
		none = "No IPv6 neighbor is configured"
	}
	if len(lines) == 1 && lines[0] == none {
		return map[netip.Addr]QuaggaObservedSession{}, false, nil
	}
	if len(lines) == 0 {
		return nil, false, errQuaggaSummary
	}
	header, ok := strings.CutPrefix(lines[0], "BGP router identifier ")
	routerID, asn, complete := strings.Cut(header, ", local AS number ")
	identity, idErr := quaggaObservationAddress(routerID)
	local, asErr := quaggaSummaryCount(asn)
	if !ok || !complete || idErr != nil || asErr != nil || identity != expectedID || local != uint64(expectedASN) {
		return nil, false, errors.New("Quagga summary router identifier or local ASN does not match the expected view")
	}
	lines = lines[1:]
	for len(lines) > 0 && strings.Join(strings.Fields(lines[0]), " ") != quaggaSummaryColumns {
		line := strings.TrimSpace(lines[0])
		if line != "" && !strings.HasPrefix(line, "RIB entries ") && !strings.HasPrefix(line, "Peers ") &&
			!strings.HasPrefix(line, "RS-Client peers ") && !strings.HasPrefix(line, "Peer groups ") && line != "Dampening enabled." {
			return nil, false, errQuaggaSummary
		}
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return nil, false, errQuaggaSummary
	}
	return parseQuaggaSummaryRows(lines[1:], ipv6)
}

func parseQuaggaSummaryRows(lines []string, ipv6 bool) (map[netip.Addr]QuaggaObservedSession, bool, error) {
	out := map[netip.Addr]QuaggaObservedSession{}
	var established, received uint64
	for len(lines) > 0 && !strings.HasPrefix(lines[0], "Total number of neighbors ") {
		row := strings.Fields(lines[0])
		lines = lines[1:]
		if len(row) == 0 {
			continue
		}
		if len(row) == 1 {
			// The stock summary wraps peer addresses of 16 or more
			// characters onto a line of their own (common for IPv6).
			if len(row[0]) < 16 || len(lines) == 0 || !strings.HasPrefix(lines[0], "                ") {
				return nil, false, errQuaggaSummary
			}
			row = append(row, strings.Fields(lines[0])...)
			lines = lines[1:]
		}
		address, session, err := parseQuaggaSummaryPeer(row, ipv6)
		if err != nil {
			return nil, false, err
		}
		if _, exists := out[address]; exists {
			return nil, false, errQuaggaSummary
		}
		out[address] = session
		if session.PrefixesKnown {
			established++
			received += session.ReceivedPrefixes
		}
	}
	var totals []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			totals = append(totals, line)
		}
	}
	if len(totals) != 3 || len(out) == 0 {
		return nil, false, errQuaggaSummary
	}
	for i, total := range []struct {
		label string
		want  uint64
	}{{"Total number of neighbors ", uint64(len(out))}, {"Total num. Established sessions ", established}, {"Total num. of routes received ", received}} {
		raw, ok := strings.CutPrefix(totals[i], total.label)
		got, err := quaggaSummaryCount(strings.TrimSpace(raw))
		if !ok || err != nil || got != total.want {
			return nil, false, errQuaggaSummary
		}
	}
	return out, true, nil
}

func parseQuaggaSummaryPeer(row []string, ipv6 bool) (netip.Addr, QuaggaObservedSession, error) {
	if len(row) < 10 || len(row) > 11 || row[1] != "4" {
		return netip.Addr{}, QuaggaObservedSession{}, errQuaggaSummary
	}
	address, err := quaggaObservationAddress(row[0])
	asn, asErr := quaggaSummaryCount(row[2])
	if err != nil || asErr != nil || !validASN(uint32(asn)) || address.Is6() != ipv6 {
		return netip.Addr{}, QuaggaObservedSession{}, errQuaggaSummary
	}
	// Stock %d time-independent counters may wrap into negative values. They
	// are checked only as delimiters and never mistaken for prefix counts.
	for _, value := range row[3:8] {
		if _, err := strconv.ParseInt(value, 10, 32); err != nil {
			return netip.Addr{}, QuaggaObservedSession{}, errQuaggaSummary
		}
	}
	peer := QuaggaObservedSession{NeighborAddress: address.String(), NeighborASN: uint32(asn)}
	switch state := strings.Join(row[9:], " "); state {
	case "Idle", "Idle (Admin)", "Idle (PfxCt)":
		peer.State = "Idle"
	case "Connect", "Active", "OpenSent", "OpenConfirm":
		peer.State = state
	case "Clearing", "Deleted":
		peer.State = "unknown"
	default:
		count, err := quaggaSummaryCount(state)
		if err != nil {
			return netip.Addr{}, QuaggaObservedSession{}, errQuaggaSummary
		}
		peer.State, peer.ReceivedPrefixes, peer.PrefixesKnown = "Established", count, true
	}
	return address, peer, nil
}

func quaggaSummaryCount(raw string) (uint64, error) {
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || strconv.FormatUint(n, 10) != raw {
		return 0, errQuaggaSummary
	}
	return n, nil
}
