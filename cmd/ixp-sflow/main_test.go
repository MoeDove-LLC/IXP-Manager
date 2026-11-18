package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/sflow"
)

func TestCLIExplicitOptInAndPrivateConfiguration(t *testing.T) {
	dir := t.TempDir()
	config := sflow.Config{Endpoint: "https://manager.example/api/sflow", Bind: "127.0.0.1:6343", MaxDatagramsPerSecond: 100, Exporters: []sflow.Exporter{{ID: "exporter", SwitchID: "switch", SourceIP: "192.0.2.1", AgentAddress: "192.0.2.1"}}}
	raw, _ := json.Marshal(config)
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, raw, 0600)
	var out bytes.Buffer
	if err := run([]string{"check-config", "--config", path}, &out); err != nil || !strings.Contains(out.String(), "no socket opened") {
		t.Fatal(err, out.String())
	}
	out.Reset()
	for _, args := range [][]string{{}, {"listen", "--config", path}, {"listen", "--config", path, "--forward"}, {"listen", "--config", path, "--token", "not-a-valid-secret-flag"}, {"forward", "--config", path}} {
		if err := run(args, &out); err == nil || strings.Contains(err.Error(), "not-a-valid-secret-flag") {
			t.Fatal("unsafe command accepted or secret echoed", err)
		}
	}
	if out.Len() != 0 {
		t.Fatal("failed command emitted metadata")
	}
	os.Chmod(path, 0644)
	if err := run([]string{"check-config", "--config", path}, &out); err == nil {
		t.Fatal("public config read")
	}
}
