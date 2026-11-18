package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func cliPatchFixture(t *testing.T, root string) ([]byte, string, []byte) {
	t.Helper()
	name := "  original ../../attachment.txt  "
	content := []byte("synthetic old attachment\x00\xff")
	keyHash := sha256.Sum256([]byte("1-" + name))
	key := hex.EncodeToString(keyHash[:])
	// The old model hashes port ID + original name; it is not a content digest.
	rel, err := migrate.PatchStoragePath(key)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, content, 0640); err != nil {
		t.Fatal(err)
	}
	source := map[string]any{"tables": map[string]any{
		"patch_panel":                   []map[string]any{{"id": 1, "name": "Fixture panel", "port_prefix": "PP", "active": 1, "installation_date": "2020-01-02"}},
		"patch_panel_port":              []map[string]any{{"id": 1, "patch_panel_id": 1, "number": 1, "state": 1, "notes": "exact\nsource notes", "private_notes": "staff only", "last_state_change": "2020-01-02 03:04:05"}},
		"patch_panel_port_history":      []map[string]any{{"id": 1, "patch_panel_port_id": 1, "number": 1, "state": 3, "customer": "former member label", "switchport": "former port label", "connected_at": "2019-02-03", "created_at": "2020-01-02 03:04:05"}},
		"patch_panel_port_file":         []map[string]any{{"id": 1, "patch_panel_port_id": 1, "name": name, "type": "text/plain", "size": len(content), "is_private": 0, "storage_location": key, "uploaded_at": "2020-01-01 00:00:00"}},
		"patch_panel_port_history_file": []map[string]any{{"id": 1, "patch_panel_port_history_id": 1, "name": name, "type": "text/plain", "size": len(content), "is_private": 1, "storage_location": key}},
	}}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	return raw, key, content
}
func readCLIPatch(t *testing.T, path string) (*migrate.PatchBundle, map[string][]byte) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	contents := map[string][]byte{}
	bundle, err := migrate.ReadPatchBundle(file, func(f migrate.PatchFileSource, r io.Reader) error {
		content, e := io.ReadAll(r)
		contents[f.Entry] = content
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return bundle, contents
}
func TestPatchBundleCLIProducesPrivateByteVerifiedArchive(t *testing.T) {
	root := t.TempDir()
	raw, key, content := cliPatchFixture(t, root)
	output := filepath.Join(t.TempDir(), "patch.tar.gz")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--patch-bundle", "--patch-files-root", root, "--source-timezone", "UTC", "--output", output}, bytes.NewReader(raw), &stdout, &stderr); err != nil {
		t.Fatalf("bundle CLI failed: %v %s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatal("bundle bytes or source metadata leaked to stdout")
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("bundle output must be mode0600")
	}
	bundle, contents := readCLIPatch(t, output)
	if bundle.Patching.SourceTimezone != "UTC" || len(bundle.Patching.Files) != 2 || len(bundle.Patching.Histories) != 1 {
		t.Fatal("patch metadata/history lost")
	}
	if bundle.Patching.Panels[0].Facts["installation_date"] != "2020-01-02" || bundle.Patching.Histories[0].Facts["connected_at"] != "2019-02-03" || bundle.Patching.Ports[0].Facts["last_state_change"] != "2020-01-02 03:04:05" {
		t.Fatal("source dates or date precision rewritten")
	}
	wantHash := sha256.Sum256(content)
	for _, f := range bundle.Patching.Files {
		if f.StorageKey != key || f.Checksum != hex.EncodeToString(wantHash[:]) || !bytes.Equal(contents[f.Entry], content) {
			t.Fatal("original storage mapping/actual content integrity lost")
		}
	}
	if bundle.Patching.Files[0].Private || !bundle.Patching.Files[1].Private {
		t.Fatal("original per-record privacy changed")
	}
}
func TestPatchBundleCLIRequiresExplicitSafeInputs(t *testing.T) {
	root := t.TempDir()
	raw, _, _ := cliPatchFixture(t, root)
	for _, tc := range []struct {
		name string
		args func(string) []string
	}{
		{"no bundle mode", func(out string) []string { return []string{"--output", out} }},
		{"no source root", func(out string) []string { return []string{"--patch-bundle", "--output", out} }},
		{"no output file", func(string) []string { return []string{"--patch-bundle", "--patch-files-root", root} }},
		{"timezone Local", func(out string) []string {
			return []string{"--patch-bundle", "--patch-files-root", root, "--source-timezone", "Local", "--output", out}
		}},
		{"missing source bytes", func(out string) []string {
			return []string{"--patch-bundle", "--patch-files-root", t.TempDir(), "--source-timezone", "UTC", "--output", out}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out.tar.gz")
			var stdout, stderr bytes.Buffer
			if err := run(tc.args(out), bytes.NewReader(raw), &stdout, &stderr); err == nil {
				t.Fatal("unsafe CLI inputs accepted")
			}
			if stdout.Len() != 0 {
				t.Fatal("failed CLI emitted output")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("failed CLI published file")
			}
		})
	}
}
func TestPatchBundleCLIExistingOutputAndSymlinkAreUntouched(t *testing.T) {
	root := t.TempDir()
	raw, _, _ := cliPatchFixture(t, root)
	for _, link := range []bool{false, true} {
		t.Run(fmt.Sprint(link), func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "existing")
			if err := os.WriteFile(target, []byte("preserve original"), 0600); err != nil {
				t.Fatal(err)
			}
			output := target
			if link {
				output = filepath.Join(dir, "link")
				if err := os.Symlink(target, output); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			if err := run([]string{"--patch-bundle", "--patch-files-root", root, "--source-timezone", "UTC", "--output", output}, bytes.NewReader(raw), &stdout, &stderr); err == nil {
				t.Fatal("existing destination overwritten")
			}
			current, err := os.ReadFile(target)
			if err != nil || string(current) != "preserve original" {
				t.Fatal("existing destination mutated")
			}
		})
	}
}
func TestPatchBundleCLIStrictWarningIsAtomicAndDoesNotLeakBearer(t *testing.T) {
	root := t.TempDir()
	raw, _, _ := cliPatchFixture(t, root)
	var source map[string]any
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	tables := source["tables"].(map[string]any)
	ports := tables["patch_panel_port"].([]any)
	ports[0].(map[string]any)["loa_code"] = "sensitive-old-bearer-not-authority"
	raw, _ = json.Marshal(source)
	output := filepath.Join(t.TempDir(), "strict.tar.gz")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--strict", "--patch-bundle", "--patch-files-root", root, "--source-timezone", "UTC", "--output", output}, bytes.NewReader(raw), &stdout, &stderr); err == nil {
		t.Fatal("strict conversion ignored omitted authorization")
	}
	if stdout.Len() != 0 || strings.Contains(stderr.String(), "sensitive-old-bearer-not-authority") {
		t.Fatal("failed migration emitted output or original LOA credential")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("strict failure published a bundle")
	}
	if !strings.Contains(stderr.String(), "LOA bearer authorization excluded") {
		t.Fatal("omitted original authorization was not reported")
	}
}
func TestPatchBundleCLIRejectsContentSizeAndStorageKeyMismatch(t *testing.T) {
	root := t.TempDir()
	raw, _, _ := cliPatchFixture(t, root)
	for _, field := range []string{"size", "storage_location"} {
		t.Run(field, func(t *testing.T) {
			var source map[string]any
			if err := json.Unmarshal(raw, &source); err != nil {
				t.Fatal(err)
			}
			file := source["tables"].(map[string]any)["patch_panel_port_file"].([]any)[0].(map[string]any)
			if field == "size" {
				file[field] = 999
			} else {
				file[field] = "../../unrelated"
			}
			bad, _ := json.Marshal(source)
			output := filepath.Join(t.TempDir(), "bad.tar.gz")
			var stdout, stderr bytes.Buffer
			if err := run([]string{"--patch-bundle", "--patch-files-root", root, "--source-timezone", "UTC", "--output", output}, bytes.NewReader(bad), &stdout, &stderr); err == nil {
				t.Fatal("mismatched source metadata accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("bad metadata produced publication")
			}
		})
	}
}
