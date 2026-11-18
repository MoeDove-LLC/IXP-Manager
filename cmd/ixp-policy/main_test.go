package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/policy"
)

func TestOfflineROAImportAndCandidateCLI(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "vrps.json")
	output := filepath.Join(dir, "snapshot.json")
	raw := fmt.Sprintf(`{"metadata":{"generated":%d},"roas":[{"prefix":"192.0.2.0/24","asn":"AS64501","maxLength":24}]}`, time.Now().Unix())
	if err := os.WriteFile(source, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"roa-import", "--input", source, "--source", "local-validator", "--output", output}, &stdout); err != nil {
		t.Fatal(err)
	}
	var snapshot policy.ROASnapshot
	if err := policy.ReadJSON(output, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Source != "local-validator" || len(snapshot.ROAs) != 1 {
		t.Fatal("collector output contract mismatch")
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0600 {
		t.Fatal("snapshot not private")
	}
}
func TestCollectorRequiresExplicitSourceAndDoesNotOverwriteOnFailure(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "snapshot.json")
	_ = os.WriteFile(output, []byte("previous snapshot"), 0600)
	for _, args := range [][]string{{"irr", "--as-set", "AS-EXAMPLE", "--output", output}, {"roa-fetch", "--output", output}, {"roa-import", "--input", "missing", "--source", "validator", "--output", output}} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Fatal("missing source accepted")
		}
	}
	after, _ := os.ReadFile(output)
	if string(after) != "previous snapshot" {
		t.Fatal("failed collection replaced existing snapshot")
	}
}
