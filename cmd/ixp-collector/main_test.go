package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectorRejectsTokenArgumentsWithoutEcho(t *testing.T) {
	var out bytes.Buffer
	secret := "private-test-token"
	err := run([]string{"--token", secret}, strings.NewReader(""), &out)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) {
		t.Fatal("unsafe token arguments or output")
	}
}
func TestCollectorRequiresPrivateTokenFile(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "collector.json")
	os.WriteFile(config, []byte(`{"endpoint":"https://manager.example/api/monitoring/ingest","routerId":"router-a","platform":"none","ports":{"port-a":"eth0"},"peers":{}}`), 0600)
	token := filepath.Join(dir, "token")
	os.WriteFile(token, []byte("private-token"), 0644)
	var out bytes.Buffer
	err := run([]string{"--config", config, "--token-file", token, "--once"}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "private regular file") || out.Len() != 0 {
		t.Fatalf("insecure token file accepted: %v", err)
	}
}
func TestCollectorIntervalBoundAndSecretSource(t *testing.T) {
	for _, args := range [][]string{{"--config", "unused"}, {"--config", "unused", "--token-file", "token", "--token-stdin"}, {"--config", "unused", "--token-stdin", "--interval", "2s"}, {"--config", "unused", "--token-stdin", "--interval", "5m"}} {
		var out bytes.Buffer
		if err := run(args, strings.NewReader("private-token"), &out); err == nil {
			t.Fatal("invalid collector settings accepted")
		}
	}
}
