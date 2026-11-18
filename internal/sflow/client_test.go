package sflow

import (
	"context"
	"encoding/json"
	"errors"
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
func configFixture() Config {
	return Config{Endpoint: "https://manager.example/api/sflow", Bind: "127.0.0.1:6343", MaxDatagramsPerSecond: 100, Exporters: []Exporter{{ID: "exporter", SwitchID: "switch", SourceIP: "192.0.2.1", AgentAddress: "192.0.2.1"}}}
}
func TestPrivateConfigAndTokenBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	c := configFixture()
	b, _ := json.Marshal(c)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(path); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0644)
	if _, err := ReadConfig(path); err == nil {
		t.Fatal("public configuration accepted")
	}
	os.Chmod(path, 0600)
	os.Symlink(path, filepath.Join(dir, "link"))
	if _, err := ReadConfig(filepath.Join(dir, "link")); err == nil {
		t.Fatal("config symlink accepted")
	}
	os.WriteFile(filepath.Join(dir, "token"), []byte("secret-fixture-token-123"), 0600)
	if _, err := ReadToken(filepath.Join(dir, "token")); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://manager.example/api/sflow", "https://user:secret@manager.example/api/sflow", "https://manager.example/api/sflow?redirect=x", "https://manager.example/other"} {
		c.Endpoint = endpoint
		if ValidateConfig(c) == nil {
			t.Fatal("unsafe control endpoint accepted")
		}
	}
	c = configFixture()
	c.Exporters = append(c.Exporters, Exporter{ID: "other", SwitchID: "switch2", SourceIP: "192.0.2.1", AgentAddress: "192.0.2.1"})
	if ValidateConfig(c) == nil {
		t.Fatal("ambiguous source tuple accepted")
	}
}
func TestHTTPSClientBindsIdentityAndRefusesRedirectWithoutNetwork(t *testing.T) {
	e := configFixture().Exporters[0]
	calls := 0
	client := &http.Client{Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.Host != "manager.example" || r.Header.Get("Authorization") != "Bearer fixture-secret-token" {
			t.Fatal("wrong target or credentials")
		}
		now := time.Now()
		body, _ := json.Marshal(Binding{ExporterID: e.ID, SwitchID: e.SwitchID, MappingRevision: strings.Repeat("a", 22), IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})}
	if _, err := FetchBinding(context.Background(), client, "https://manager.example/api/sflow", "fixture-secret-token", e); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	calls = 0
	client.Transport = fakeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://other.example/secret"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if err := Upload(context.Background(), client, "https://manager.example/api/sflow", "fixture-secret-token", Batch{}); err == nil || calls != 1 {
		t.Fatal("redirect followed", err, calls)
	}
	client.Transport = fakeTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("secret-fixture-token-123 private detail")
	})
	if _, err := FetchBinding(context.Background(), client, "https://manager.example/api/sflow", "fixture-secret-token", e); err == nil || strings.Contains(err.Error(), "secret-fixture") {
		t.Fatal("transport leaked private details", err)
	}
}

func TestControlHTTPStatusKeepsSafeRetryClassification(t *testing.T) {
	for _, status := range []int{401, 403, 409, 429, 500, 503, 422} {
		client := &http.Client{Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("private token / response detail")), Request: r}, nil
		})}
		err := Upload(context.Background(), client, "https://manager.example/api/sflow", "fixture-secret-token", Batch{})
		var typed *ControlError
		if !errors.As(err, &typed) || typed.StatusCode != status || strings.Contains(err.Error(), "private") {
			t.Fatal("control error lost status or leaked response", err)
		}
		retry := status == 409 || status == 429 || status >= 500
		if RetryableControl(err) != retry {
			t.Fatal("incorrect retry policy", status, err)
		}
	}
}
