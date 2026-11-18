package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSnapshotOutputIsPrivateAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := writeSnapshot(path, []byte("{}\n"), "abc123"); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("snapshot is not private")
	}
	if err := writeSnapshot(path, []byte("different"), "other"); err == nil {
		t.Fatal("overwrote existing artifact")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "{}\n" {
		t.Fatal("original artifact changed")
	}
}
func TestSubmissionIsProposalOnlyAndNeverRedirects(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/api/integrations/proposals/import" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer private-sync-token" {
			t.Fatal("wrong submission operation")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://elsewhere.example/steal"}}, Body: io.NopCloser(strings.NewReader("private-sync-token")), Request: r}, nil
	})}
	_, err := submit(context.Background(), client, "https://manager.example/api/integrations/proposals/import", "private-sync-token", "member", "", "{}", "hash")
	if err == nil || calls != 1 || strings.Contains(err.Error(), "private-sync-token") {
		t.Fatal("unsafe redirect or secret echo")
	}
	if err = validateSubmitURL("http://manager.example/api/integrations/proposals/import"); err == nil {
		t.Fatal("plaintext authenticated submission accepted")
	}
	if err = validateSubmitURL("https://manager.example/api/integrations/proposals/id/apply"); err == nil {
		t.Fatal("CLI can submit to apply endpoint")
	}
}
func TestCLIRejectsCredentialArgumentsWithoutEcho(t *testing.T) {
	var output bytes.Buffer
	secret := "do-not-print-token"
	err := run([]string{"--token", secret}, strings.NewReader(""), &output)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(output.String(), secret) {
		t.Fatal("credential argument leaked")
	}
}
