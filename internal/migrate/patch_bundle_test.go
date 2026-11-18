package migrate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func patchFileFixture(kind string, id int64, key string, data []byte) PatchFileSource {
	parentKind, parentField := "patch-ports", "patch_panel_port_id"
	if kind == "history" {
		parentKind, parentField = "patch-history", "patch_panel_port_history_id"
	}
	checksum := sha256.Sum256(data)
	name := "../../original attachment.pdf"
	return PatchFileSource{ID: PatchFileID(kind, id), LegacyID: id, Kind: kind, ParentID: PatchID(parentKind, 1), Name: name, Private: true, StorageKey: key, Size: int64(len(data)), Checksum: hex.EncodeToString(checksum[:]), Entry: PatchFileEntry(kind, id), Facts: map[string]string{"id": fmt.Sprint(id), parentField: "1", "name": name, "storage_location": key, "size": fmt.Sprint(len(data)), "is_private": "1"}}
}
func patchBundleFixture(data []byte) PatchBundle {
	return PatchBundle{Version: 1, Kind: PatchBundleKind, Source: "ixp-manager-legacy", Resources: map[string][]map[string]any{}, Warnings: []string{}, Patching: PatchingImport{Version: 1, SourceTimezone: "UTC", Panels: []PatchPanelSource{}, Ports: []PatchPortSource{}, Histories: []PatchHistorySource{}, Files: []PatchFileSource{patchFileFixture("current", 1, strings.Repeat("a", 64), data)}}}
}
func createPatchSource(t *testing.T, root string, f PatchFileSource, data []byte) string {
	t.Helper()
	rel, err := PatchStoragePath(f.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, filepath.FromSlash(rel))
	if err = os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, data, 0640); err != nil {
		t.Fatal(err)
	}
	return name
}
func gzipPatch(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	if _, err := gz.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func rawPatchEntry(t *testing.T, name string, kind byte, link string, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	// Start with an ordinary USTAR header, then change the physical type to
	// construct forbidden extension/special records without tar.Writer rewriting.
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), out.Bytes()[:tarEntryBytes(int64(len(data)))]...)
	raw[156] = kind
	copy(raw[157:257], make([]byte, 100))
	copy(raw[157:257], link)
	for i := 148; i < 156; i++ {
		raw[i] = ' '
	}
	var sum int64
	for _, b := range raw[:512] {
		sum += int64(b)
	}
	copy(raw[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return raw
}
func patchTar(t *testing.T, bundle PatchBundle, entries ...[]byte) []byte {
	t.Helper()
	manifest, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	out := rawPatchEntry(t, "manifest.json", tar.TypeReg, "", manifest)
	for _, entry := range entries {
		out = append(out, entry...)
	}
	return append(out, make([]byte, 1024)...)
}
func assertPatchRejected(t *testing.T, raw []byte) {
	t.Helper()
	b, err := ReadPatchBundle(bytes.NewReader(raw), nil)
	if err == nil || b != nil {
		t.Fatal("unsafe archive returned a usable bundle")
	}
}

func TestPatchBundleRoundTripUsesOriginalStorageKey(t *testing.T) {
	content := []byte("actual legacy attachment bytes\x00\xff")
	bundle := patchBundleFixture(content)
	bundle.Patching.Files = append(bundle.Patching.Files, patchFileFixture("history", 1, bundle.Patching.Files[0].StorageKey, content))
	for i := range bundle.Patching.Files {
		bundle.Patching.Files[i].Checksum = ""
	}
	root := t.TempDir()
	source := createPatchSource(t, root, bundle.Patching.Files[0], content)
	parent := t.TempDir()
	output := filepath.Join(parent, "migration.tar.gz")
	if err := WritePatchBundle(output, root, bundle); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("bundle output is not private")
	}
	archive, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	consumed := map[string][]byte{}
	decoded, err := ReadPatchBundle(archive, func(f PatchFileSource, r io.Reader) error {
		data, e := io.ReadAll(r)
		consumed[f.Entry] = data
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range decoded.Patching.Files {
		if !bytes.Equal(consumed[f.Entry], content) || f.Checksum == f.StorageKey || f.Name != "../../original attachment.pdf" {
			t.Fatal("original mapping/content digest was confused with a name or content key")
		}
	}
	if len(consumed) != 2 || len(decoded.Patching.Files) != 2 {
		t.Fatal("current and historical file IDs were collapsed")
	}
	if bundle.Patching.Files[0].Checksum != "" {
		t.Fatal("snapshot packaging mutated caller DTO")
	}
	sourceInfo, _ := os.Stat(source)
	if sourceInfo.Mode().Perm() != 0640 {
		t.Fatal("source permissions were modified")
	}
	names, _ := os.ReadDir(parent)
	if len(names) != 1 || names[0].Name() != "migration.tar.gz" {
		t.Fatal("private staging files leaked")
	}
	original, _ := os.ReadFile(output)
	if err := WritePatchBundle(output, root, bundle); err == nil {
		t.Fatal("existing output replaced")
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(original, after) {
		t.Fatal("existing output changed")
	}
}

func TestPatchSourceRejectsLinksSpecialFilesAndSizeMismatch(t *testing.T) {
	for _, mode := range []string{"symlink-file", "symlink-component", "symlink-root", "symlink-root-ancestor", "hardlink", "fifo", "directory", "size"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("test")
			bundle := patchBundleFixture(data)
			f := bundle.Patching.Files[0]
			root := t.TempDir()
			source := createPatchSource(t, root, f, data)
			switch mode {
			case "symlink-file":
				os.Remove(source)
				target := filepath.Join(t.TempDir(), "target")
				_ = os.WriteFile(target, data, 0600)
				if err := os.Symlink(target, source); err != nil {
					t.Fatal(err)
				}
			case "symlink-component":
				original := filepath.Join(root, "files", "ppp")
				moved := filepath.Join(root, "moved")
				if err := os.Rename(original, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, original); err != nil {
					t.Fatal(err)
				}
			case "symlink-root":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				root = link
			case "symlink-root-ancestor":
				outer := t.TempDir()
				nested := filepath.Join(outer, "nested")
				if err := os.Rename(root, nested); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(t.TempDir(), "ancestor")
				if err := os.Symlink(outer, link); err != nil {
					t.Fatal(err)
				}
				root = filepath.Join(link, "nested")
			case "hardlink":
				if err := os.Link(source, filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				os.Remove(source)
				if err := syscall.Mkfifo(source, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				os.Remove(source)
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			case "size":
				f.Size++
			}
			file, err := OpenPatchSource(root, f)
			if file != nil {
				file.Close()
			}
			if err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
}

func TestPatchBundleFailureLeavesNoPublicationOrCallerMutation(t *testing.T) {
	data := []byte("first")
	bundle := patchBundleFixture(data)
	bundle.Patching.Files[0].Checksum = ""
	bundle.Patching.Files = append(bundle.Patching.Files, patchFileFixture("history", 2, strings.Repeat("b", 64), []byte("missing")))
	root := t.TempDir()
	createPatchSource(t, root, bundle.Patching.Files[0], data)
	outDir := t.TempDir()
	output := filepath.Join(outDir, "bundle.tar.gz")
	if err := WritePatchBundle(output, root, bundle); err == nil {
		t.Fatal("missing source produced bundle")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("failed operation published output")
	}
	if bundle.Patching.Files[0].Checksum != "" {
		t.Fatal("failed operation mutated caller's first digest")
	}
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Fatal("failed operation leaked private staging data")
	}
}

func TestPatchBundleRejectsArchiveShapeAndIntegrity(t *testing.T) {
	data := []byte("content")
	bundle := patchBundleFixture(data)
	entry := rawPatchEntry(t, bundle.Patching.Files[0].Entry, tar.TypeReg, "", data)
	for _, tc := range []struct {
		name    string
		archive func() []byte
	}{
		{"symlink", func() []byte {
			return patchTar(t, bundle, rawPatchEntry(t, "files/current-1", tar.TypeSymlink, "/etc/passwd", nil))
		}},
		{"hardlink", func() []byte {
			return patchTar(t, bundle, rawPatchEntry(t, "files/current-1", tar.TypeLink, "manifest.json", nil))
		}},
		{"fifo", func() []byte { return patchTar(t, bundle, rawPatchEntry(t, "files/current-1", tar.TypeFifo, "", nil)) }},
		{"directory", func() []byte { return patchTar(t, bundle, rawPatchEntry(t, "files/current-1", tar.TypeDir, "", nil)) }},
		{"extra", func() []byte { return patchTar(t, bundle, entry, rawPatchEntry(t, "extra", tar.TypeReg, "", nil)) }},
		{"duplicate", func() []byte { return patchTar(t, bundle, entry, entry) }},
		{"canonical alias", func() []byte {
			return patchTar(t, bundle, rawPatchEntry(t, "files/../files/current-1", tar.TypeReg, "", data))
		}},
		{"absolute", func() []byte { return patchTar(t, bundle, rawPatchEntry(t, "/files/current-1", tar.TypeReg, "", data)) }},
		{"backslash", func() []byte { return patchTar(t, bundle, rawPatchEntry(t, `files\current-1`, tar.TypeReg, "", data)) }},
		{"missing", func() []byte { return patchTar(t, bundle) }},
		{"digest", func() []byte {
			return patchTar(t, bundle, rawPatchEntry(t, "files/current-1", tar.TypeReg, "", []byte("tamper!")))
		}},
		{"size", func() []byte {
			return patchTar(t, bundle, rawPatchEntry(t, "files/current-1", tar.TypeReg, "", []byte("short")))
		}},
		{"nonzero file padding", func() []byte {
			bad := append([]byte(nil), entry...)
			bad[512+len(data)] = 1
			return patchTar(t, bundle, bad)
		}},
		{"no end blocks", func() []byte { tar := patchTar(t, bundle, entry); return tar[:len(tar)-1024] }},
		{"one end block", func() []byte { tar := patchTar(t, bundle, entry); return tar[:len(tar)-512] }},
		{"nonzero tail", func() []byte { return append(patchTar(t, bundle, entry), 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) { assertPatchRejected(t, gzipPatch(t, tc.archive())) })
	}
	archive := gzipPatch(t, patchTar(t, bundle, entry))
	corrupt := append([]byte(nil), archive...)
	corrupt[len(corrupt)-8] ^= 0x80
	assertPatchRejected(t, corrupt)
	assertPatchRejected(t, archive[:len(archive)-1])
	assertPatchRejected(t, append(append([]byte(nil), archive...), gzipPatch(t, nil)...))
	assertPatchRejected(t, append(append([]byte(nil), archive...), byte(1)))
}

func TestPatchBundleRejectsHiddenGNUExtensionHeader(t *testing.T) {
	data := []byte("content")
	bundle := patchBundleFixture(data)
	manifest, _ := json.Marshal(bundle)
	extension := rawPatchEntry(t, "././@LongLink", tar.TypeGNULongName, "", []byte("manifest.json\x00"))
	regular := rawPatchEntry(t, "placeholder", tar.TypeReg, "", manifest)
	raw := append(extension, regular...)
	raw = append(raw, rawPatchEntry(t, "files/current-1", tar.TypeReg, "", data)...)
	raw = append(raw, make([]byte, 1024)...)
	// Establish that archive/tar hides this physical entry and returns a benign
	// effective manifest header, so ordinary Next-based counting is insufficient.
	decoded, err := tar.NewReader(bytes.NewReader(raw)).Next()
	if err != nil || decoded.Name != "manifest.json" || decoded.Typeflag != tar.TypeReg {
		t.Fatalf("invalid regression fixture: %v %v", decoded, err)
	}
	assertPatchRejected(t, gzipPatch(t, raw))
	for _, kind := range []byte{tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongLink, tar.TypeGNUSparse} {
		bad := append(rawPatchEntry(t, "extension", kind, "", nil), raw...)
		assertPatchRejected(t, gzipPatch(t, bad))
	}
}

func TestPatchBundleLimitsAndConsumerCompletion(t *testing.T) {
	data := []byte("content")
	bundle := patchBundleFixture(data)
	raw := patchTar(t, bundle, rawPatchEntry(t, "files/current-1", tar.TypeReg, "", data))
	compressed := gzipPatch(t, raw)
	for _, tc := range []struct {
		name   string
		change func(*patchBundleLimits)
	}{
		{"manifest", func(l *patchBundleLimits) { l.manifest = 1 }},
		{"file", func(l *patchBundleLimits) { l.file = int64(len(data) - 1) }},
		{"entries", func(l *patchBundleLimits) { l.entries = 1 }},
		{"unpacked", func(l *patchBundleLimits) { l.unpacked = int64(len(raw) - 1) }},
		{"compressed", func(l *patchBundleLimits) { l.compressed = int64(len(compressed) - 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := standardPatchBundleLimits()
			tc.change(&limits)
			if got, err := readPatchBundle(bytes.NewReader(compressed), nil, limits); err == nil || got != nil {
				t.Fatal("archive size/count limit was bypassed")
			}
		})
	}
	limits := standardPatchBundleLimits()
	limits.unpacked = int64(len(raw))
	limits.compressed = int64(len(compressed))
	if _, err := readPatchBundle(bytes.NewReader(compressed), nil, limits); err != nil {
		t.Fatalf("exact boundary rejected: %v", err)
	}
	for _, consume := range []func(PatchFileSource, io.Reader) error{func(_ PatchFileSource, r io.Reader) error { one := make([]byte, 1); _, err := r.Read(one); return err }, func(_ PatchFileSource, _ io.Reader) error { return errors.New("storage failed") }} {
		if b, err := ReadPatchBundle(bytes.NewReader(compressed), consume); err == nil || b != nil {
			t.Fatal("incomplete/failing consumer produced usable import")
		}
	}
	// Large zero padding cannot evade the cumulative decompression budget.
	padded := gzipPatch(t, append(raw, make([]byte, 2048)...))
	limits = standardPatchBundleLimits()
	limits.unpacked = int64(len(raw) + 512)
	if b, err := readPatchBundle(bytes.NewReader(padded), nil, limits); err == nil || b != nil {
		t.Fatal("compressed padding bomb bypassed cumulative bound")
	}
	root := t.TempDir()
	createPatchSource(t, root, bundle.Patching.Files[0], data)
	for _, tc := range []struct {
		name   string
		change func(*patchBundleLimits)
	}{
		{"file", func(l *patchBundleLimits) { l.file = 1 }},
		{"manifest", func(l *patchBundleLimits) { l.manifest = 1 }},
		{"entries", func(l *patchBundleLimits) { l.entries = 1 }},
		{"unpacked", func(l *patchBundleLimits) { l.unpacked = int64(len(raw) - 1) }},
		{"compressed", func(l *patchBundleLimits) { l.compressed = 1 }},
	} {
		t.Run("writer_"+tc.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "bounded.tar.gz")
			limits := standardPatchBundleLimits()
			tc.change(&limits)
			if err := writePatchBundle(output, root, bundle, limits); err == nil {
				t.Fatal("writer ignored archive limit")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("writer published failed limit check")
			}
		})
	}
	// The writer must count physical header+padding bytes exactly, not reject a
	// correctly bounded archive due to an inflated manifest overhead estimate.
	limits = standardPatchBundleLimits()
	limits.unpacked = int64(len(raw))
	if err := writePatchBundle(filepath.Join(t.TempDir(), "exact.tar.gz"), root, bundle, limits); err != nil {
		t.Fatalf("writer rejected exact decompressed boundary: %v", err)
	}
}
