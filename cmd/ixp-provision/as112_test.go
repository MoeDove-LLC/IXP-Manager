package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/as112"
)

func as112CLIFixture(t *testing.T) (as112.Bundle, string) {
	t.Helper()
	b, err := as112.Render("router-as112", as112.Service{Mode: as112.Both, IPv4: true, IPv6: true}, as112.Identity{ServerName: "node.example.test", ResponsibleMailbox: "hostmaster.example.test", Facility: "AS112 fixture", Location: "Isolated lab", UniqueIP: "192.0.2.112"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := as112.Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	return b, raw
}
func TestAS112CLIFetchBindsRouterAndRejectsFalseHealth(t *testing.T) {
	b, content := as112CLIFixture(t)
	headerRouter := "router-as112"
	transport := coreFetchTransport(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set("X-Provisioning-Revision", "revision")
		h.Set("X-Provisioning-Profile-ID", "profile")
		h.Set("X-Provisioning-Kind", "as112-dns")
		h.Set("X-Provisioning-Router-ID", headerRouter)
		h.Set("X-Config-SHA256", checksum(content))
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(content)), Request: r}, nil
	})
	o := fetchOptions{Server: "https://control.example.test", Profile: "profile", Revision: "revision", Kind: "as112-dns", RouterID: headerRouter, Output: filepath.Join(t.TempDir(), "bundle.json")}
	if _, err := fetch(context.Background(), transport, o, testToken); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(o.Output)
	if err != nil || string(data) != content {
		t.Fatal("AS112 fetch changed exact bytes")
	}
	st, _ := os.Stat(o.Output)
	if st.Mode().Perm() != 0600 {
		t.Fatal("AS112 output permissions are not private")
	}
	for _, mode := range []string{"wrong-header", "wrong-target", "forged-health", "ambiguous-json"} {
		t.Run(mode, func(t *testing.T) {
			candidate := o
			candidate.Output = filepath.Join(t.TempDir(), "rejected.json")
			_, original := as112CLIFixture(t)
			content = original
			headerRouter = o.RouterID
			switch mode {
			case "wrong-header":
				headerRouter = "another-router"
			case "wrong-target":
				candidate.RouterID = "another-router"
			case "forged-health":
				bad := b
				bad.HealthPlan.Executed = true
				raw, _ := json.MarshalIndent(bad, "", "  ")
				content = string(raw) + "\n"
			case "ambiguous-json":
				content = strings.Replace(content, "\"version\": 1,", "\"version\": 0,\n  \"version\": 1,", 1)
			}
			if _, err := fetch(context.Background(), transport, candidate, testToken); err == nil {
				t.Fatal("invalid router/health binding was accepted")
			}
			if _, err := os.Stat(candidate.Output); !os.IsNotExist(err) {
				t.Fatal("rejected AS112 fetch wrote output")
			}
		})
	}
}
func TestAS112CLIExtractionAndMissingLocalAddressGate(t *testing.T) {
	b, raw := as112CLIFixture(t)
	file := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "bundle")
	args := []string{"--file", file, "--checksum", checksum(raw), "--router", b.RouterID}
	var output bytes.Buffer
	if code, err := runAS112(append(append([]string{}, args...), "--output", directory), &output, false); err != nil || code != 0 {
		t.Fatal("AS112 extraction failed", code, err)
	}
	for _, f := range b.Files {
		data, err := os.ReadFile(filepath.Join(directory, f.Path))
		if err != nil || string(data) != f.Content {
			t.Fatal("extracted service bytes changed")
		}
	}
	if code, err := runAS112(append(append([]string{}, args...), "--output", directory), io.Discard, false); err == nil || code == 0 {
		t.Fatal("AS112 extraction overwrote an installed directory")
	}
	// The ordinary unit sandbox has no fixed AS112 addresses. Probe must stop
	// locally with zero packets, never query another public anycast node.
	output.Reset()
	code, err := runAS112(args, &output, true)
	var result as112.HealthResult
	if code != 3 || err == nil || json.Unmarshal(output.Bytes(), &result) != nil || result.Queries != 0 || result.Healthy || result.LocalAddressesPresent {
		t.Fatal("AS112 probe bypassed local-address evidence", code, err)
	}
	if code, err := runAS112([]string{"--help"}, io.Discard, true); err != nil || code != 0 {
		t.Fatal("AS112 help failed")
	}
}
