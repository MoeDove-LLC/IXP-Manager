package as112

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type HealthFailure struct {
	Address   string `json:"address"`
	Transport string `json:"transport"`
	Name      string `json:"name"`
	Code      string `json:"code"`
}
type HealthResult struct {
	RouterID              string          `json:"routerId"`
	CheckedAt             time.Time       `json:"checkedAt"`
	Healthy               bool            `json:"healthy"`
	LocalAddressesPresent bool            `json:"localAddressesPresent"`
	Queries               int             `json:"queries"`
	Failures              []HealthFailure `json:"failures"`
}
type healthRuntime struct {
	addresses func() ([]net.Addr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
}

// Probe tests only the manifest's fixed AS112 addresses on DNS port 53. It
// refuses to query unless those addresses and the unique node address are
// present on this host, then rechecks that condition before declaring success.
// It neither installs addresses nor changes routing advertisements.
func Probe(ctx context.Context, b Bundle) (HealthResult, error) {
	d := net.Dialer{Timeout: 2 * time.Second}
	return probe(ctx, b, healthRuntime{net.InterfaceAddrs, d.DialContext})
}
func localAddresses(b Bundle, r healthRuntime) bool {
	actual, err := r.addresses()
	if err != nil {
		return false
	}
	seen := map[netip.Addr]bool{}
	for _, row := range actual {
		if prefix, err := netip.ParsePrefix(row.String()); err == nil {
			seen[prefix.Addr().Unmap()] = true
		}
	}
	for _, value := range b.RequiredLocalAddresses {
		ip, err := netip.ParseAddr(value)
		if err != nil || !seen[ip] {
			return false
		}
	}
	return true
}
func probe(ctx context.Context, b Bundle, r healthRuntime) (HealthResult, error) {
	result := HealthResult{RouterID: b.RouterID, CheckedAt: time.Now().UTC(), Failures: []HealthFailure{}}
	if err := Validate(b); err != nil {
		return result, err
	}
	if !localAddresses(b, r) {
		return result, errors.New("all AS112 service addresses and the unique node IP must be configured locally before probing")
	}
	result.LocalAddressesPresent = true
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for _, address := range b.ListenAddresses {
		for _, transport := range []string{"udp", "tcp"} {
			for _, check := range b.HealthPlan.Checks {
				name := check.Name
				if check.RCode == "NXDOMAIN" {
					nonce := make([]byte, 8)
					if _, err := rand.Read(nonce); err != nil {
						return result, errors.New("DNS probe randomness unavailable")
					}
					name = "health-" + hex.EncodeToString(nonce) + "." + strings.SplitN(check.Name, ".", 2)[1]
				}
				result.Queries++
				if err := healthQuery(ctx, b, r, address, transport, name, check); err != nil {
					result.Failures = append(result.Failures, HealthFailure{address, transport, check.Name, "dns-answer-unverified"})
					return result, nil
				}
			}
		}
	}
	if !localAddresses(b, r) {
		result.LocalAddressesPresent = false
		result.Failures = append(result.Failures, HealthFailure{Code: "local-addresses-changed"})
		return result, nil
	}
	result.Healthy = true
	return result, nil
}
func healthQuery(ctx context.Context, b Bundle, r healthRuntime, address, transport, name string, check HealthCheck) error {
	random := make([]byte, 2)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	id := binary.BigEndian.Uint16(random)
	qname, err := dnsmessage.NewName(name)
	if err != nil {
		return err
	}
	qtype := dnsmessage.TypePTR
	if check.Type == "TXT" {
		qtype = dnsmessage.TypeTXT
	}
	query := dnsmessage.Message{Header: dnsmessage.Header{ID: id}, Questions: []dnsmessage.Question{{Name: qname, Type: qtype, Class: dnsmessage.ClassINET}}}
	// A valid two-field node identity can exceed classic DNS's 512-byte UDP
	// limit. Advertise a conservative EDNS0 payload instead of mistaking the
	// server's legitimate truncation for a broken authoritative service.
	rootName, _ := dnsmessage.NewName(".")
	query.Additionals = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: rootName, Type: dnsmessage.TypeOPT, Class: dnsmessage.Class(1232)}, Body: &dnsmessage.OPTResource{}}}
	wire, err := query.Pack()
	if err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	connection, err := r.dial(call, transport, net.JoinHostPort(address, "53"))
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline, _ := call.Deadline()
	if err = connection.SetDeadline(deadline); err != nil {
		return err
	}
	if transport == "tcp" {
		frame := make([]byte, 2+len(wire))
		binary.BigEndian.PutUint16(frame, uint16(len(wire)))
		copy(frame[2:], wire)
		if err = writeDNS(connection, frame); err != nil {
			return err
		}
		prefix := make([]byte, 2)
		if _, err = io.ReadFull(connection, prefix); err != nil {
			return err
		}
		n := int(binary.BigEndian.Uint16(prefix))
		if n < 12 || n > 8192 {
			return errors.New("DNS response bound")
		}
		wire = make([]byte, n)
		if _, err = io.ReadFull(connection, wire); err != nil {
			return err
		}
	} else {
		if _, err = connection.Write(wire); err != nil {
			return err
		}
		buffer := make([]byte, 8193)
		n, err := connection.Read(buffer)
		if err != nil || n > 8192 {
			return errors.New("DNS response unavailable")
		}
		wire = buffer[:n]
	}
	var response dnsmessage.Message
	if response.Unpack(wire) != nil || response.ID != id || !response.Response || !response.Authoritative || response.RecursionAvailable || response.Truncated || response.OpCode != 0 || len(response.Questions) != 1 || response.Questions[0] != query.Questions[0] {
		return errors.New("DNS response identity/flags mismatch")
	}
	if check.RCode == "NOERROR" {
		if response.RCode != dnsmessage.RCodeSuccess || len(response.Answers) != 3 {
			return errors.New("DNS identity answer missing")
		}
		wanted := [][]string{{b.Identity.Facility, b.Identity.Location}, {"See https://www.as112.net/ for more information."}, {"Unique IP: " + b.Identity.UniqueIP}}
		seen := map[int]bool{}
		for _, answer := range response.Answers {
			value, ok := answer.Body.(*dnsmessage.TXTResource)
			if !ok || answer.Header.Type != dnsmessage.TypeTXT || answer.Header.Class != dnsmessage.ClassINET || !strings.EqualFold(answer.Header.Name.String(), name) {
				return errors.New("DNS identity answer unexpected")
			}
			found := false
			for i, text := range wanted {
				if !seen[i] && reflect.DeepEqual(value.TXT, text) {
					seen[i] = true
					found = true
					break
				}
			}
			if !found {
				return errors.New("DNS identity TXT mismatch")
			}
		}
		return nil
	}
	if response.RCode != dnsmessage.RCodeNameError || len(response.Answers) != 0 {
		return errors.New("DNS empty-zone result mismatch")
	}
	zone := strings.SplitN(name, ".", 2)[1]
	found := false
	for _, authority := range response.Authorities {
		soa, ok := authority.Body.(*dnsmessage.SOAResource)
		if !ok || authority.Header.Class != dnsmessage.ClassINET || !strings.EqualFold(authority.Header.Name.String(), zone) {
			continue
		}
		mname, mbox := "prisoner.iana.org.", "hostmaster.root-servers.org."
		if zone == "empty.as112.arpa." {
			mname, mbox = "blackhole.as112.arpa.", "noc.dns.icann.org."
		}
		if found || !strings.EqualFold(soa.NS.String(), mname) || !strings.EqualFold(soa.MBox.String(), mbox) || soa.Serial != 1 {
			return errors.New("DNS negative SOA mismatch")
		}
		found = true
	}
	if !found {
		return errors.New("DNS negative SOA missing")
	}
	return nil
}
func writeDNS(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
