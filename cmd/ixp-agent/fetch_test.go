package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routing"
)

type fetchTransport func(*http.Request) (*http.Response, error)

func (f fetchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fetchFixture(t *testing.T) (fetchOptions, string, *http.Response) {
	t.Helper()
	opts := fetchOptions{Server: "https://control.example", Deployment: "reviewed-id", Router: "router-id", Platform: "bird2", Output: filepath.Join(t.TempDir(), "reviewed.conf")}
	config := "router id 192.0.2.1; # private test artifact\n"
	response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(config))}
	response.Header.Set("X-Config-Platform", opts.Platform)
	response.Header.Set("X-Config-Router-ID", opts.Router)
	response.Header.Set("X-Config-Revision", opts.Deployment)
	response.Header.Set("X-Config-SHA256", routing.Checksum(config))
	return opts, config, response
}

func TestFetchBindsApprovedTargetAndWritesPrivateFileWithoutOverwrite(t *testing.T) {
	opts, config, response := fetchFixture(t)
	calls := 0
	transport := fetchTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://control.example/api/deployments/reviewed-id/config" || r.Header.Get("Authorization") != "Bearer mdt.id.private" || r.Method != "GET" {
			t.Fatal("unexpected download request")
		}
		return response, nil
	})
	sum, err := fetchDeployment(context.Background(), transport, opts, "mdt.id.private")
	if err != nil || sum != routing.Checksum(config) {
		t.Fatal("approved fetch failed", err)
	}
	b, err := os.ReadFile(opts.Output)
	st, statErr := os.Stat(opts.Output)
	if err != nil || statErr != nil || string(b) != config || st.Mode().Perm() != 0600 {
		t.Fatal("download did not preserve exact content in a private file")
	}
	if _, err := fetchDeployment(context.Background(), transport, opts, "mdt.id.private"); err == nil || calls != 1 {
		t.Fatal("existing output was fetched again or overwritten")
	}
	if err := os.Remove(opts.Output); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(filepath.Dir(opts.Output), "existing.conf")
	if err := os.WriteFile(victim, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, opts.Output); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchDeployment(context.Background(), transport, opts, "mdt.id.private"); err == nil || calls != 1 {
		t.Fatal("symlink output accepted")
	}
}

func TestFetchRejectsWrongTargetsRedirectsChecksumsAndSecretErrors(t *testing.T) {
	const secret = "NeverPrintThisCredential"
	for _, tc := range []struct {
		name   string
		change func(*http.Response)
	}{
		{"router", func(r *http.Response) { r.Header.Set("X-Config-Router-ID", "different-router") }},
		{"platform", func(r *http.Response) { r.Header.Set("X-Config-Platform", "junos") }},
		{"revision", func(r *http.Response) { r.Header.Set("X-Config-Revision", "different-revision") }},
		{"duplicate", func(r *http.Response) { r.Header.Add("X-Config-Router-ID", "router-id") }},
		{"checksum", func(r *http.Response) { r.Header.Set("X-Config-SHA256", strings.Repeat("a", 64)) }},
		{"missing-checksum", func(r *http.Response) { r.Header.Del("X-Config-SHA256") }},
		{"redirect", func(r *http.Response) {
			r.StatusCode = 302
			r.Header.Set("Location", "https://unexpected.example/stolen")
		}},
		{"error-body", func(r *http.Response) { r.StatusCode = 409; r.Body = io.NopCloser(strings.NewReader(secret)) }},
		{"oversize", func(r *http.Response) { r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", (16<<20)+1))) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, response := fetchFixture(t)
			tc.change(response)
			calls := 0
			transport := fetchTransport(func(*http.Request) (*http.Response, error) { calls++; return response, nil })
			if _, err := fetchDeployment(context.Background(), transport, opts, "mdt.id.private"); err == nil || strings.Contains(err.Error(), secret) || calls != 1 {
				t.Fatal("unsafe download accepted, followed redirect or echoed a secret")
			}
			if _, err := os.Lstat(opts.Output); !os.IsNotExist(err) {
				t.Fatal("failed download left a final output")
			}
		})
	}
	opts, _, _ := fetchFixture(t)
	transport := fetchTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New(secret) })
	if _, err := fetchDeployment(context.Background(), transport, opts, "mdt.id.private"); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("transport error exposed private detail")
	}
}

func TestFetchRefusesUnsafeOriginTokenAndTokenFile(t *testing.T) {
	opts, _, _ := fetchFixture(t)
	transport := fetchTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid request reached transport")
		return nil, nil
	})
	for _, origin := range []string{"http://control.example", "https://user:password@control.example", "https://control.example/subpath", "https://control.example?token=secret", "https://control.example?", "https://control.example/#secret"} {
		opts.Server = origin
		if _, err := fetchDeployment(context.Background(), transport, opts, "mdt.id.private"); err == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("mdt.id.private"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runFetch([]string{"--token-file", path}, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatal("insecure token file accepted")
	}
	for _, token := range []string{"", "raw secret", "mdt.id.private\nHeader: evil", strings.Repeat("x", 257)} {
		if err := runFetch([]string{"--token-stdin"}, strings.NewReader(token), io.Discard); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
}
