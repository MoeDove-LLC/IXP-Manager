package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routing"
)

func TestQuaggaFetchCheckAndRejectsUnboundControl(t *testing.T) {
	config, err := routing.RenderFor("quagga", routing.Router{ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "192.0.2.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	opts, _, response := fetchFixture(t)
	opts.Platform = "quagga"
	response.Header.Set("X-Config-Platform", "quagga")
	response.Header.Set("X-Config-SHA256", routing.Checksum(config))
	response.Body = io.NopCloser(strings.NewReader(config))
	transport := fetchTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("fetch mutated target")
		}
		return response, nil
	})
	checksum, err := fetchDeployment(context.Background(), transport, opts, "mdt.fixture.private")
	if err != nil || checksum != routing.Checksum(config) {
		t.Fatal("Quagga approved transfer failed", err)
	}
	raw, err := os.ReadFile(opts.Output)
	info, statErr := os.Stat(opts.Output)
	if err != nil || statErr != nil || string(raw) != config || info.Mode().Perm() != 0600 {
		t.Fatal("download changed content or private mode")
	}
	for _, mode := range []string{"apply", "recover", "serve", "stage", "status", "confirm", "cancel", "bootstrap"} {
		if err := run([]string{mode, "--platform", "quagga", "--file", opts.Output, "--checksum", checksum}); err == nil {
			t.Fatal("Quagga accepted an unbound command", mode)
		}
	}
	if err := run([]string{"check", "--platform", "quagga", "--file", opts.Output, "--checksum", checksum, "--validator", "/usr/bin/true"}); err == nil {
		t.Fatal("non-Quagga parser accepted")
	}
	if actual := os.Getenv("QUAGGA_BGPD_BIN"); actual != "" {
		if err := run([]string{"check", "--platform", "quagga", "--file", opts.Output, "--checksum", checksum, "--validator", actual}); err != nil {
			t.Fatal("actual native Quagga check failed", err)
		}
	}
}
