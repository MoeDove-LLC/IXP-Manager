package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/moedove/ixp-manager/internal/routing"
)

func TestBIRD1FetchAndRefusesUnboundLiveActions(t *testing.T) {
	opts, config, response := fetchFixture(t)
	opts.Platform = "bird1"
	response.Header.Set("X-Config-Platform", "bird1")
	if _, e := fetchDeployment(context.Background(), fetchTransport(func(*http.Request) (*http.Response, error) { return response, nil }), opts, "mdt.id.private"); e != nil {
		t.Fatal(e)
	}
	if raw, e := os.ReadFile(opts.Output); e != nil || string(raw) != config {
		t.Fatal("BIRD1 fetch changed bytes", e)
	}
	for _, mode := range []string{"apply", "recover", "stage", "serve"} {
		if e := run([]string{mode, "--platform", "bird1", "--file", opts.Output, "--checksum", routing.Checksum(config)}); e == nil {
			t.Fatal("BIRD1 accepted an unbound live action", mode)
		}
	}
}
func TestBIRD1CLIActualFamilyCheck(t *testing.T) {
	v4, v6 := os.Getenv("BIRD1_BIN"), os.Getenv("BIRD1_IPV6_BIN")
	if v4 == "" || v6 == "" {
		t.Skip("actual BIRD 1 v4/v6 binaries required")
	}
	for _, family := range []int{4, 6} {
		router := routing.Router{ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "192.0.2.1"}
		right, wrong := v4, v6
		if family == 6 {
			router.LocalIPv4 = ""
			router.LocalIPv6 = "2001:db8::1"
			right, wrong = v6, v4
		}
		config, e := routing.RenderFor("bird1", router, nil)
		if e != nil {
			t.Fatal(e)
		}
		file := filepath.Join(t.TempDir(), "approved.conf")
		if e = os.WriteFile(file, []byte(config), 0600); e != nil {
			t.Fatal(e)
		}
		base := []string{"check", "--platform", "bird1", "--file", file, "--checksum", routing.Checksum(config), "--validator"}
		if e = run(append(append([]string{}, base...), right)); e != nil {
			t.Fatal("matching family CLI check", e)
		}
		if e = run(append(append([]string{}, base...), wrong)); e == nil {
			t.Fatal("wrong-family CLI checker accepted")
		}
	}
}
