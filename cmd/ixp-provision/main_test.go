package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/provision"
)

const testToken = "mdt.test.only_for_isolated_fixtures"

func smokeFixture(t *testing.T) provision.Artifact {
	t.Helper()
	a, err := provision.Render(provision.Input{Options: provision.Options{Kind: "smokeping"}, Targets: []provision.Target{{ID: "target", Kind: "member", Name: "Test member", Address: "192.0.2.2", Active: true, Monitor: true, CanPing: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestRenderAndCheckDoNotOverwriteOrActivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "targets.conf")
	in := provision.Input{Options: provision.Options{Kind: "smokeping"}, Targets: []provision.Target{{ID: "member", Kind: "member", Name: "Test", Address: "192.0.2.3", Active: true, Monitor: true, CanPing: true}}}
	raw, _ := json.Marshal(in)
	var out bytes.Buffer
	if _, err := run([]string{"render", "--input", "-", "--output", path}, bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	var result struct {
		SHA256    string `json:"sha256"`
		Activated bool   `json:"activated"`
		Validated bool   `json:"validated"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Activated || result.Validated {
		t.Fatalf("unexpected render result: %s %v", out.String(), err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if checksum(string(content)) != result.SHA256 {
		t.Fatal("file checksum mismatch")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("output not private")
	}
	if _, err := run([]string{"render", "--input", "-", "--output", path}, bytes.NewReader(raw), io.Discard); err == nil {
		t.Fatal("overwrote existing artifact")
	}
	out.Reset()
	if _, err := run([]string{"check", "--file", path, "--kind", "smokeping", "--checksum", result.SHA256}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"level":"structural"`) || !strings.Contains(out.String(), `"activated":false`) {
		t.Fatal(out.String())
	}
	if _, err := run([]string{"check", "--file", path, "--kind", "smokeping", "--checksum", strings.Repeat("0", 64)}, nil, io.Discard); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	bad := strings.Replace(string(raw), `"options":`, `"unknown":true,"options":`, 1)
	if _, err := run([]string{"render", "--input", "-", "--output", filepath.Join(dir, "bad")}, strings.NewReader(bad), io.Discard); err == nil {
		t.Fatal("unknown input field accepted")
	}
}
func TestPrivateFilesAndAtomicPublication(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "token")
	if err := os.WriteFile(secret, []byte(testToken), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(secret, false, nil); err == nil {
		t.Fatal("public token accepted")
	}
	os.Chmod(secret, 0600)
	if value, err := readToken(secret, false, nil); err != nil || value != testToken {
		t.Fatal("private token rejected")
	}
	link := filepath.Join(dir, "link")
	os.Symlink(secret, link)
	if _, err := readToken(link, false, nil); err == nil {
		t.Fatal("token symlink accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegular(fifo, 256, false); err == nil {
		t.Fatal("FIFO accepted")
	}
	target := filepath.Join(dir, "atomic")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, v := range []byte{'a', 'b'} {
		wg.Add(1)
		go func(value byte) { defer wg.Done(); errs <- writeNew(target, bytes.Repeat([]byte{value}, 100000)) }(v)
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("wanted exactly one publisher, got %d", success)
	}
	raw, err := os.ReadFile(target)
	if err != nil || len(raw) != 100000 || !bytes.Equal(raw, bytes.Repeat(raw[:1], 100000)) {
		t.Fatal("partial or mixed publication")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".moedove-provision-") {
			t.Fatal("temporary file leak")
		}
	}
}
func TestFetchPinsRevisionAndChecksumAndRejectsRedirect(t *testing.T) {
	a := smokeFixture(t)
	mode := "ok"
	hits := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/provisioning/revision/config" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("wrong route/token")
		}
		if mode == "redirect" {
			w.Header().Set("Location", "/unexpected")
			w.WriteHeader(302)
			return
		}
		w.Header().Set("X-Provisioning-Revision", "revision")
		w.Header().Set("X-Provisioning-Profile-ID", "profile")
		w.Header().Set("X-Provisioning-Kind", "smokeping")
		w.Header().Set("X-Config-SHA256", checksum(a.Content))
		if mode == "profile" {
			w.Header().Set("X-Provisioning-Profile-ID", "different")
		}
		if mode == "checksum" {
			w.Header().Set("X-Config-SHA256", strings.Repeat("0", 64))
		}
		fmt.Fprint(w, a.Content)
	}))
	defer server.Close()
	for _, m := range []string{"profile", "checksum", "redirect", "ok"} {
		mode = m
		before := hits
		path := filepath.Join(t.TempDir(), "output")
		o := fetchOptions{Server: server.URL, Profile: "profile", Revision: "revision", Kind: "smokeping", Output: path}
		sum, err := fetch(context.Background(), server.Client().Transport, o, testToken)
		if m == "ok" {
			if err != nil || sum != checksum(a.Content) {
				t.Fatalf("fetch failed: %v", err)
			}
		} else {
			if err == nil {
				t.Fatalf("accepted %s", m)
			}
			if _, e := os.Stat(path); !os.IsNotExist(e) {
				t.Fatal("failed fetch wrote output")
			}
		}
		if hits-before != 1 {
			t.Fatal("redirect followed or unexpected extra request")
		}
	}
	if _, err := serverURL("http://127.0.0.1"); err == nil {
		t.Fatal("plaintext accepted")
	}
	for _, url := range []string{"https://user:pass@example.net", "https://example.net/path", "https://example.net/?token=secret"} {
		if _, err := serverURL(url); err == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
}
func TestObservedProbeUsesActualSampleAndIdentity(t *testing.T) {
	now := time.Now().UTC()
	session := sessionObservation{PeerID: "peer", RouterID: "router", State: "Established", Status: "fresh", ObservedAt: now, IdentityVerified: true}
	var sessionBody any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/monitoring/summary" {
			t.Error("unexpected path")
		}
		json.NewEncoder(w).Encode(sessionBody)
	}))
	defer server.Close()
	cases := []struct {
		name   string
		mutate func(*sessionObservation)
		code   int
	}{
		{"established", func(*sessionObservation) {}, 0},
		{"old_sample_fresh_query", func(s *sessionObservation) { s.ObservedAt = now.Add(-10 * time.Minute) }, 3},
		{"missing_sample", func(s *sessionObservation) { s.ObservedAt = time.Time{} }, 3},
		{"future", func(s *sessionObservation) { s.ObservedAt = now.Add(time.Minute) }, 3},
		{"identity", func(s *sessionObservation) { s.IdentityVerified = false }, 3},
		{"server_stale", func(s *sessionObservation) { s.Status = "stale" }, 3},
		{"unknown_state", func(s *sessionObservation) { s.State = "unknown" }, 3},
		{"idle", func(s *sessionObservation) { s.State = "Idle" }, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := session
			tc.mutate(&s)
			sessionBody = map[string]any{"observedAt": now, "sessions": []sessionObservation{s}}
			r := probe(context.Background(), server.Client().Transport, server.URL, testToken, "bgp", "peer", 120*time.Second, now)
			if r.Code != tc.code {
				t.Fatalf("code %d: %s", r.Code, r.Message)
			}
		})
	}
	sessionBody = map[string]any{"sessions": []sessionObservation{session, session}}
	if r := probe(context.Background(), server.Client().Transport, server.URL, testToken, "bgp", "peer", 120*time.Second, now); r.Code != 3 {
		t.Fatal("duplicate observation accepted")
	}
	sessionBody = map[string]any{"sessions": []sessionObservation{session}}
	r := probe(context.Background(), server.Client().Transport, server.URL, testToken, "router", "router", 120*time.Second, now)
	if r.Code != 0 || !strings.Contains(r.Message, "independent router daemon health is not measured") {
		t.Fatal(r)
	}
	disabled := false
	other := sessionObservation{PeerID: "disabled", RouterID: "router", State: "unknown", Status: "unknown", ConfiguredEnabled: &disabled}
	sessionBody = map[string]any{"sessions": []sessionObservation{session, other}}
	if r := probe(context.Background(), server.Client().Transport, server.URL, testToken, "router", "router", 120*time.Second, now); r.Code != 0 {
		t.Fatal("disabled peer spoiled measured router freshness", r)
	}
	sessionBody = map[string]any{"sessions": []sessionObservation{other}}
	if r := probe(context.Background(), server.Client().Transport, server.URL, testToken, "router", "router", 120*time.Second, now); r.Code != 3 {
		t.Fatal("no monitored observations must stay UNKNOWN", r)
	}
	var output bytes.Buffer
	code, err := run([]string{"probe", "--kind", "core", "--id", "bundle"}, nil, &output)
	if err != nil || code != 3 || !strings.HasPrefix(output.String(), "UNKNOWN") {
		t.Fatal("core was fabricated healthy")
	}
}
func TestSwitchProbeRequiresLastSuccessfulObservation(t *testing.T) {
	now := time.Now().UTC()
	sw := switchObservation{SwitchID: "switch", Status: "fresh", LastObservedAt: now, LastSuccessAt: now}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/devices/status" {
			t.Error("wrong path")
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []switchObservation{sw}})
	}))
	defer server.Close()
	check := func(want int) {
		t.Helper()
		r := probe(context.Background(), server.Client().Transport, server.URL, testToken, "switch", "switch", 120*time.Second, now)
		if r.Code != want {
			t.Fatal(r)
		}
	}
	check(0)
	sw.LastSuccessAt = now.Add(-time.Hour)
	check(3)
	sw.LastSuccessAt = now
	sw.ErrorCode = "timeout"
	check(3)
	sw.ErrorCode = ""
	sw.Status = "unknown"
	check(3)
}
