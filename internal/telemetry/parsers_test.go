package telemetry

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBIRD2AndFRRFixtures(t *testing.T) {
	bird, _ := os.ReadFile("testdata/bird2-protocols.txt")
	sessions, err := ParseBIRD2(bird)
	if err != nil {
		t.Fatal(err)
	}
	if sessions["peer_a"].NeighborAddress != "192.0.2.2" || sessions["peer_a"].NeighborASN != 64513 || sessions["peer_a"].State != "Established" || sessions["peer_a"].ReceivedPrefixes != 15 || sessions["peer_b"].State != "Active" || len(sessions) != 2 {
		t.Fatal(sessions)
	}
	frr, _ := os.ReadFile("testdata/frr-summary.json")
	sessions, err = ParseFRRSummary(frr)
	if err != nil {
		t.Fatal(err)
	}
	if sessions["192.0.2.2"].NeighborAddress != "192.0.2.2" || sessions["192.0.2.2"].NeighborASN != 64513 || sessions["192.0.2.2"].ReceivedPrefixes != 15 || sessions["2001:db8::2"].ReceivedPrefixes != 7 || sessions["192.0.2.3"].State != "Active" {
		t.Fatal(sessions)
	}
	if _, err = ParseBIRD2([]byte("Unable to connect to server")); err == nil {
		t.Fatal("failed command parsed as empty success")
	}
	for _, bad := range []string{`null`, `{}`, `{"peers":{"192.0.2.2":{"pfxRcd":-1}}}`, `{"peers":{}} {}`} {
		if _, err = ParseFRRSummary([]byte(bad)); err == nil {
			t.Fatal("invalid FRR output accepted")
		}
	}
}
func TestLinuxExplicitMappingAndCounterPrecision(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "eth0")
	os.MkdirAll(filepath.Join(base, "statistics"), 0700)
	for path, value := range map[string]string{"statistics/rx_bytes": "18446744073709551615\n", "statistics/tx_bytes": "9007199254740993\n", "ifindex": "3\n", "operstate": "up\n"} {
		os.WriteFile(filepath.Join(base, path), []byte(value), 0600)
	}
	boot := filepath.Join(root, "boot")
	os.WriteFile(boot, []byte("test-boot"), 0600)
	ports, err := ReadLinuxPorts(root, boot, map[string]string{"port-a": "eth0"})
	if err != nil {
		t.Fatal(err)
	}
	if ports[0].InOctets != "18446744073709551615" || ports[0].CounterEpoch != "test-boot:3" || ports[0].OperStatus != "up" {
		t.Fatal(ports)
	}
	if _, err = ReadLinuxPorts(root, boot, map[string]string{"port-a": "../eth0"}); err == nil {
		t.Fatal("interface traversal accepted")
	}
	os.Remove(filepath.Join(base, "statistics/rx_bytes"))
	if _, err = ReadLinuxPorts(root, boot, map[string]string{"port-a": "eth0"}); err == nil {
		t.Fatal("missing counter silently fabricated")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestUploadDoesNotRedirectOrLeakToken(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Fatal("missing authorization")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://another.example/steal"}}, Body: io.NopCloser(strings.NewReader("private-token")), Request: r}, nil
	})}
	err := Upload(context.Background(), client, "https://manager.example/api/monitoring/ingest", "private-token", Batch{})
	if err == nil || calls != 1 || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("unsafe redirect/logging: %d %v", calls, err)
	}
	if err = Upload(context.Background(), client, "http://manager.example/api/monitoring/ingest", "private-token", Batch{}); err == nil {
		t.Fatal("plaintext upload accepted")
	}
}

func TestUnknownPrefixCountsAreNotFabricated(t *testing.T) {
	bird := []byte("BIRD 2.17.5 ready.\nName Proto Table State Since Info\np BGP --- up 12:00 Established\n  BGP state: Established\n")
	b, err := ParseBIRD2(bird)
	if err != nil {
		t.Fatal(err)
	}
	if b["p"].PrefixesKnown || b["p"].State != "unknown" || b["p"].NeighborASN != 0 {
		t.Fatal("missing BIRD route count became known zero")
	}
	f, err := ParseFRRSummary([]byte(`{"peers":{"192.0.2.2":{"state":"Established"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if f["192.0.2.2"].PrefixesKnown || f["192.0.2.2"].State != "unknown" {
		t.Fatal("missing FRR prefix count became known zero")
	}
}

func TestCollectorRunsOnlyFixedLocalStatusArguments(t *testing.T) {
	dir := t.TempDir()
	fixture, _ := filepath.Abs("testdata/bird2-protocols.txt")
	argsPath := filepath.Join(dir, "args")
	binary := filepath.Join(dir, "birdc")
	script := `#!/bin/sh
printf '%s\n' "$@" > '` + argsPath + `'
cat '` + fixture + `'
`
	// No production router is invoked; this executable only emits the fixture.
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := CollectorConfig{Endpoint: "https://manager.example/api/monitoring/ingest", RouterID: "router-a", Platform: "bird2", Birdc: binary, BirdSocket: "/run/bird/test.ctl", Peers: map[string]string{"peer-id": "peer_a"}}
	batch, err := Collect(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Sessions) != 1 || batch.Sessions[0].ReceivedPrefixes == nil || *batch.Sessions[0].ReceivedPrefixes != 15 {
		t.Fatal(batch)
	}
	args, _ := os.ReadFile(argsPath)
	if string(args) != "-s\n/run/bird/test.ctl\nshow\nprotocols\nall\n" {
		t.Fatalf("unexpected executable arguments %q", args)
	}
}

func TestFRRConflictingAddressFamilyIdentityIsUnknown(t *testing.T) {
	raw := []byte(`{"ipv4Unicast":{"peers":{"192.0.2.2":{"state":"Established","remoteAs":64513,"pfxRcd":10}}},"ipv6Unicast":{"peers":{"192.0.2.2":{"state":"Established","remoteAs":64514,"pfxRcd":20}}}}`)
	peers, err := ParseFRRSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	peer := peers["192.0.2.2"]
	if peer.State != "unknown" || peer.PrefixesKnown || peer.NeighborASN != 0 {
		t.Fatal("conflicting native neighbor identities merged as established")
	}
}
