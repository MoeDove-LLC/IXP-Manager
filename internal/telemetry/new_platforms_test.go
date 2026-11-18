package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBIRD3IdentityVersionAndDuplicateFields(t *testing.T) {
	raw, e := os.ReadFile("testdata/bird2-protocols.txt")
	if e != nil {
		t.Fatal(e)
	}
	three := strings.Replace(string(raw), "BIRD 2.17.5", "BIRD 3.4.0", 1)
	rows, e := ParseBIRD3([]byte(three))
	if e != nil || rows["peer_a"].NeighborASN != 64513 || rows["peer_a"].State != "Established" {
		t.Fatal("BIRD3 native identity lost", e)
	}
	if _, e = ParseBIRD3(raw); e == nil {
		t.Fatal("BIRD2 output relabeled BIRD3")
	}
	if _, e = ParseBIRD3([]byte(strings.TrimPrefix(three, "BIRD 3.4.0 ready.\n"))); e == nil {
		t.Fatal("BIRD3 identity lacked native version evidence")
	}
	duplicate := strings.Replace(three, "    Neighbor AS:      64513", "    Neighbor AS:      64513\n    Neighbor AS:      64514", 1)
	if _, e = ParseBIRD3([]byte(duplicate)); e == nil {
		t.Fatal("duplicate native ASN could overwrite identity")
	}
}
func TestOpenBGPDActualNeighborJSONAndMissingCounts(t *testing.T) {
	raw := []byte(`{"neighbors":[{"remote_as":"64513","remote_addr":"192.0.2.2","state":"Established","stats":{"prefixes":{"received":12,"sent":3}},"config":{"authentication":"md5sig"},"description":"must not be exported"},{"remote_as":"64514","remote_addr":"2001:db8::2","state":"Idle"}]}`)
	rows, e := ParseOpenBGPDNeighbors(raw)
	if e != nil {
		t.Fatal(e)
	}
	if rows["192.0.2.2"].NeighborASN != 64513 || rows["192.0.2.2"].ReceivedPrefixes != 12 || !rows["192.0.2.2"].PrefixesKnown {
		t.Fatal("OpenBGPD counters/identity fabricated or lost")
	}
	if rows["2001:db8::2"].PrefixesKnown || rows["2001:db8::2"].State != "Idle" {
		t.Fatal("missing native count became known zero")
	}
	for _, bad := range []string{`{}`, `{"neighbors":[{"remote_as":"0","remote_addr":"192.0.2.2","state":"Established"}]}`, `{"neighbors":[{"remote_as":"64513","remote_addr":"192.0.2.2/24","state":"Established"}]}`} {
		if _, e = ParseOpenBGPDNeighbors([]byte(bad)); e == nil {
			t.Fatal("unproven native identity accepted")
		}
	}
}
func TestCollectorOpenBGPDUsesOnlyFixedStatusQuery(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "bgpctl")
	log := filepath.Join(dir, "argv")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + log + "'\nprintf '%s' '{\"neighbors\":[{\"remote_as\":\"64513\",\"remote_addr\":\"192.0.2.2\",\"state\":\"Established\",\"stats\":{\"prefixes\":{\"received\":12}}}]}'\n"
	if e := os.WriteFile(binary, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	config := CollectorConfig{Endpoint: "https://manager.example/api/monitoring/ingest", RouterID: "router-a", Platform: "openbgpd", Bgpctl: binary, BgpSocket: filepath.Join(dir, "bgpd.sock"), Peers: map[string]string{"peer-a": "192.0.2.2"}}
	batch, e := Collect(context.Background(), config)
	if e != nil || len(batch.Sessions) != 1 || batch.Sessions[0].NeighborASN != 64513 {
		t.Fatal("OpenBGPD collector did not use real identity parser", e)
	}
	args, _ := os.ReadFile(log)
	if string(args) != "-s "+config.BgpSocket+" -j show neighbor\n" {
		t.Fatal("unexpected collector command", string(args))
	}
}
