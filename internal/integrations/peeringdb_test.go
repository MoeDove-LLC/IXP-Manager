package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureSnapshot() Snapshot {
	n := int64(10)
	return Snapshot{Version: 1, Provider: "peeringdb", FetchedAt: time.Now().UTC(), Network: Network{ID: 101, ASN: 64501, Name: "Example network", Website: "https://network.example.test", Prefixes4: &n, Status: "ok"}, NetIXLANs: []NetIXLAN{{ID: 201, NetID: 101, IXID: 301, IXLANID: 401, ASN: 64501, Name: "Example IX", IPv4: "192.0.2.10", IPv6: "2001:db8::10", Speed: 1000, IsRSPeer: true, Status: "not-operational"}}, Exchanges: []Exchange{{ID: 301, Name: "Example IX", Country: "CN", Status: "ok"}}, Facilities: []Facility{{ID: 501, Name: "Example facility", City: "Shanghai", Country: "CN", Status: "ok"}}}
}
func fixtureClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	c, err := NewClient(Config{Enabled: true, BaseURL: "https://www.peeringdb.com/api", RequestIntervalSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = transport
	return c
}
func TestTypedPeeringDBFetchUsesOnlyFixedPublicGETs(t *testing.T) {
	snapshot := fixtureSnapshot()
	requests := []string{}
	c := fixtureClient(t, fakeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Host != "www.peeringdb.com" || r.Header.Get("Authorization") != "" || r.URL.Query().Get("depth") != "0" {
			t.Fatal("provider request escaped read-only contract")
		}
		requests = append(requests, r.URL.Path)
		var data any
		switch r.URL.Path {
		case "/api/net":
			if r.URL.Query().Get("asn") != "64501" {
				t.Fatal("ASN query mismatch")
			}
			data = []Network{snapshot.Network}
		case "/api/netixlan":
			if r.URL.Query().Get("net_id") != "101" || r.URL.Query().Get("ix_id") != "301" {
				t.Fatal("connection filter mismatch")
			}
			data = snapshot.NetIXLANs
		case "/api/ix/301":
			data = snapshot.Exchanges
		case "/api/fac/501":
			data = snapshot.Facilities
		default:
			t.Fatal("unexpected provider object")
		}
		raw, _ := json.Marshal(map[string]any{"data": data})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}, Request: r}, nil
	}))
	got, err := c.Fetch(context.Background(), 64501, 301, 501)
	if err != nil {
		t.Fatal(err)
	}
	if got.Network.ASN != 64501 || len(requests) != 4 || got.NetIXLANs[0].Status != "not-operational" || got.Network.Prefixes6 != nil {
		t.Fatal("typed response lost fields/unknown state")
	}
	if _, err = c.Fetch(context.Background(), 64501, 301, 501); err == nil || len(requests) != 4 {
		t.Fatal("provider request budget not enforced")
	}
}
func TestProviderConfigRejectsGenericURLsAndRedirects(t *testing.T) {
	for _, base := range []string{"http://www.peeringdb.com/api", "https://attacker.example/api", "https://www.peeringdb.com/api?token=secret", "https://user:pass@www.peeringdb.com/api", "https://www.peeringdb.com/anything"} {
		if _, err := NewClient(Config{Enabled: true, BaseURL: base}); err == nil {
			t.Fatalf("unsafe baseURL accepted: %s", base)
		}
	}
	calls := 0
	c := fixtureClient(t, fakeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}))
	if _, err := c.Fetch(context.Background(), 64501, 0, 0); err == nil || calls != 1 {
		t.Fatal("redirect followed outside allowlisted provider")
	}
}
func TestSnapshotBindingFreshnessAndCanonicalRoundtrip(t *testing.T) {
	s := fixtureSnapshot()
	raw, checksum, err := Canonical(s)
	if err != nil || checksum != Checksum(raw) {
		t.Fatal("snapshot digest invalid")
	}
	parsed, err := Parse(raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	canonical, again, _ := Canonical(parsed)
	if string(canonical) != string(raw) || again != checksum {
		t.Fatal("typed snapshot canonicalization unstable")
	}
	for _, mutate := range []func(*Snapshot){func(s *Snapshot) { s.Network.ASN = 0 }, func(s *Snapshot) { s.NetIXLANs[0].ASN = 64502 }, func(s *Snapshot) { s.NetIXLANs[0].NetID = 999 }, func(s *Snapshot) { s.Network.Website = "javascript:alert(1)" }, func(s *Snapshot) { s.FetchedAt = time.Now().Add(-8 * 24 * time.Hour) }, func(s *Snapshot) { s.FetchedAt = time.Now().Add(10 * time.Minute) }} {
		s = fixtureSnapshot()
		mutate(&s)
		if _, err := Normalize(s, time.Now()); err == nil {
			t.Fatal("invalid provider binding accepted")
		}
	}
	if _, err = Parse([]byte(`{"version":1,"unexpectedURL":"http://127.0.0.1"}`), time.Now()); err == nil {
		t.Fatal("unrecognized snapshot schema accepted")
	}
}
func TestPrivateProviderConfigAndResponseLimits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.json")
	os.WriteFile(path, []byte(`{"enabled":true,"baseURL":"https://www.peeringdb.com/api"}`), 0644)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("nonprivate provider config accepted")
	}
	os.Chmod(path, 0600)
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	c := fixtureClient(t, fakeTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", MaxSnapshotBytes+1))), Header: http.Header{}, Request: r}, nil
	}))
	if _, err := c.Fetch(context.Background(), 64501, 0, 0); err == nil {
		t.Fatal("oversized provider response accepted")
	}
}
