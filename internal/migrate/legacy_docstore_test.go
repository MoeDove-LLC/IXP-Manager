package migrate

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func docstoreFixture(content []byte) DocstoreBundle {
	hash := sha256.Sum256(content)
	checksum := hex.EncodeToString(hash[:])
	name := "../original report.txt"
	return DocstoreBundle{Version: 1, Kind: DocstoreBundleKind, Source: "ixp-manager-legacy", Warnings: []string{}, Docstore: DocstoreImport{Version: 1, SourceTimezone: "UTC", Directories: []DocstoreDirectorySource{}, Logs: []DocstoreLogSource{}, Files: []DocstoreFileSource{{ID: DocstoreID("shared", "file", 1), LegacyID: 1, Kind: "shared", Name: name, MinPrivileges: 2, StorageKey: "opaque/original-key", Size: int64(len(content)), Checksum: checksum, Entry: DocstoreFileEntry("shared", 1), Facts: map[string]string{"id": "1", "name": name, "min_privs": "2", "disk": "docstore", "path": "opaque/original-key", "sha256": checksum, "file_last_updated": "2020-01-02 03:04:05"}}}}}
}
func docstoreTar(t *testing.T, b DocstoreBundle, entries ...[]byte) []byte {
	t.Helper()
	manifest, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	raw := rawPatchEntry(t, "manifest.json", tar.TypeReg, "", manifest)
	for _, entry := range entries {
		raw = append(raw, entry...)
	}
	return append(raw, make([]byte, 1024)...)
}
func createDocstoreSource(t *testing.T, root string, f DocstoreFileSource, content []byte) string {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(f.StorageKey))
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, content, 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestDocstoreBundleRoundTripUsesRecordedDiskAndPath(t *testing.T) {
	content := []byte("old arbitrary bytes\x00\xff")
	b := docstoreFixture(content)
	b.Docstore.Files[0].Checksum, b.Docstore.Files[0].Size = "", 0
	root := t.TempDir()
	createDocstoreSource(t, root, b.Docstore.Files[0], content)
	output := filepath.Join(t.TempDir(), "docstore.tar.gz")
	if err := WriteDocstoreBundle(output, map[string]string{"docstore": root}, b); err != nil {
		t.Fatal(err)
	}
	if b.Docstore.Files[0].Checksum != "" || b.Docstore.Files[0].Size != 0 {
		t.Fatal("writer mutated caller DTO")
	}
	stat, err := os.Stat(output)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("output is not private")
	}
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var actual []byte
	result, err := ReadDocstoreBundle(f, func(file DocstoreFileSource, r io.Reader) error {
		if file.MinPrivileges != 2 || file.StorageKey != "opaque/original-key" || file.Name != "../original report.txt" {
			t.Fatal("original metadata changed")
		}
		actual, err = io.ReadAll(r)
		return err
	})
	if err != nil || !bytes.Equal(actual, content) || result.Docstore.Files[0].Size != int64(len(content)) {
		t.Fatalf("roundtrip: %v", err)
	}
	if result.Docstore.Files[0].Facts["file_last_updated"] != "2020-01-02 03:04:05" {
		t.Fatal("source date was rewritten")
	}
	if err = WriteDocstoreBundle(output, map[string]string{"docstore": root}, b); err == nil {
		t.Fatal("overwrote existing output")
	}
}

func TestDocstoreBundleRejectsArchiveManipulation(t *testing.T) {
	content := []byte("document content")
	b := docstoreFixture(content)
	entry := rawPatchEntry(t, b.Docstore.Files[0].Entry, tar.TypeReg, "", content)
	valid := gzipPatch(t, docstoreTar(t, b, entry))
	cases := map[string][]byte{
		"missing":      gzipPatch(t, docstoreTar(t, b)),
		"duplicate":    gzipPatch(t, docstoreTar(t, b, entry, entry)),
		"extra":        gzipPatch(t, docstoreTar(t, b, entry, rawPatchEntry(t, "files/extra", tar.TypeReg, "", nil))),
		"symlink":      gzipPatch(t, docstoreTar(t, b, rawPatchEntry(t, b.Docstore.Files[0].Entry, tar.TypeSymlink, "other", content))),
		"hardlink":     gzipPatch(t, docstoreTar(t, b, rawPatchEntry(t, b.Docstore.Files[0].Entry, tar.TypeLink, "other", content))),
		"pax":          gzipPatch(t, docstoreTar(t, b, rawPatchEntry(t, "PaxHeader", tar.TypeXHeader, "", nil), entry)),
		"noncanonical": gzipPatch(t, docstoreTar(t, b, rawPatchEntry(t, "files/./shared-1", tar.TypeReg, "", content))),
		"hash":         gzipPatch(t, docstoreTar(t, b, rawPatchEntry(t, b.Docstore.Files[0].Entry, tar.TypeReg, "", bytes.Repeat([]byte{'x'}, len(content))))),
		"size":         gzipPatch(t, docstoreTar(t, b, rawPatchEntry(t, b.Docstore.Files[0].Entry, tar.TypeReg, "", content[:len(content)-1]))),
		"trailing":     append(append([]byte(nil), valid...), 1),
		"second-gzip":  append(append([]byte(nil), valid...), gzipPatch(t, nil)...),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := ReadDocstoreBundle(bytes.NewReader(raw), nil)
			if err == nil || result != nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	if result, err := ReadDocstoreBundle(bytes.NewReader(valid), func(DocstoreFileSource, io.Reader) error { return nil }); err == nil || result != nil {
		t.Fatal("incomplete consumer accepted")
	}
	sentinel := errors.New("staging failed")
	if _, err := ReadDocstoreBundle(bytes.NewReader(valid), func(DocstoreFileSource, io.Reader) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal("consumer failure lost")
	}
}

func TestDocstoreBundleEnforcesEveryLimit(t *testing.T) {
	content := []byte("bounded content")
	b := docstoreFixture(content)
	tarBytes := docstoreTar(t, b, rawPatchEntry(t, b.Docstore.Files[0].Entry, tar.TypeReg, "", content))
	compressed := gzipPatch(t, tarBytes)
	manifest, _ := json.Marshal(b)
	for name, alter := range map[string]func(*patchBundleLimits){
		"compressed": func(l *patchBundleLimits) { l.compressed = int64(len(compressed) - 1) },
		"unpacked":   func(l *patchBundleLimits) { l.unpacked = int64(len(tarBytes) - 1) },
		"manifest":   func(l *patchBundleLimits) { l.manifest = int64(len(manifest) - 1) },
		"file":       func(l *patchBundleLimits) { l.file = int64(len(content) - 1) },
		"entries":    func(l *patchBundleLimits) { l.entries = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			limits := standardDocstoreBundleLimits()
			alter(&limits)
			if _, err := readDocstoreBundle(bytes.NewReader(compressed), nil, limits); err == nil {
				t.Fatal("reader limit not enforced")
			}
			root := t.TempDir()
			createDocstoreSource(t, root, b.Docstore.Files[0], content)
			output := filepath.Join(t.TempDir(), "bundle.tar.gz")
			if err := writeDocstoreBundle(output, map[string]string{"docstore": root}, b, limits); err == nil {
				t.Fatal("writer limit not enforced")
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("failed writer published output")
			}
		})
	}
}

func TestDocstoreSourcesRejectLinksSpecialsAndMissingDisk(t *testing.T) {
	for _, kind := range []string{"missing-disk", "symlink", "parent-symlink", "root-symlink", "hardlink", "fifo", "directory", "checksum"} {
		t.Run(kind, func(t *testing.T) {
			b := docstoreFixture([]byte("bytes"))
			root := t.TempDir()
			file := createDocstoreSource(t, root, b.Docstore.Files[0], []byte("bytes"))
			roots := map[string]string{"docstore": root}
			switch kind {
			case "missing-disk":
				roots = map[string]string{"other": root}
			case "symlink":
				if err := os.Rename(file, file+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(file+"-real", file); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				if err := os.Rename(filepath.Dir(file), filepath.Dir(file)+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(file)+"-real", filepath.Dir(file)); err != nil {
					t.Fatal(err)
				}
			case "root-symlink":
				link := filepath.Join(t.TempDir(), "root")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				roots["docstore"] = link
			case "hardlink":
				if err := os.Link(file, file+"-second"); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(file, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			case "checksum":
				if err := os.WriteFile(file, []byte("other"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			output := filepath.Join(t.TempDir(), "bundle.tar.gz")
			if err := WriteDocstoreBundle(output, roots, b); err == nil {
				t.Fatal("unsafe source accepted")
			}
			entries, err := os.ReadDir(filepath.Dir(output))
			if err != nil || len(entries) != 0 {
				t.Fatal("failed snapshot left artifacts")
			}
		})
	}
}

func TestDocstoreMetadataRejectsPrivilegeAndNamespaceConfusion(t *testing.T) {
	for name, mutate := range map[string]func(*DocstoreImport){
		"privilege-downgrade":             func(p *DocstoreImport) { p.Files[0].MinPrivileges = 0 },
		"missing-privilege-is-not-public": func(p *DocstoreImport) { delete(p.Files[0].Facts, "min_privs"); p.Files[0].MinPrivileges = 0 },
		"renamed-source":                  func(p *DocstoreImport) { p.Files[0].Name = "other" },
		"path-traversal":                  func(p *DocstoreImport) { p.Files[0].StorageKey = "../secret"; p.Files[0].Facts["path"] = "../secret" },
		"absolute-path": func(p *DocstoreImport) {
			p.Files[0].StorageKey = "/etc/passwd"
			p.Files[0].Facts["path"] = "/etc/passwd"
		},
		"unlisted-facts":        func(p *DocstoreImport) { p.Files[0].Facts["api_password"] = "excluded" },
		"customer-id-on-shared": func(p *DocstoreImport) { p.Files[0].MemberID = "legacy-members-1" },
		"checksum":              func(p *DocstoreImport) { p.Files[0].Checksum = strings.Repeat("a", 64) },
		"duplicate":             func(p *DocstoreImport) { p.Files = append(p.Files, p.Files[0]) },
		"wrong-entry":           func(p *DocstoreImport) { p.Files[0].Entry = "files/customer-1" },
		"bad-log-namespace": func(p *DocstoreImport) {
			p.Logs = []DocstoreLogSource{{ID: DocstoreID("shared", "log", 1), LegacyID: 1, FileID: DocstoreID("customer", "file", 1), Facts: map[string]string{"id": "1", "docstore_file_id": "1"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := docstoreFixture([]byte("a")).Docstore
			mutate(&p)
			if ValidateDocstore(&p, true) == nil {
				t.Fatal("unsafe metadata accepted")
			}
		})
	}
	p := docstoreFixture([]byte("a")).Docstore
	delete(p.Files[0].Facts, "min_privs")
	p.Files[0].MinPrivileges = -1
	if err := ValidateDocstore(&p, true); err != nil {
		t.Fatal("unknown privilege must remain reviewable", err)
	}
}

func TestDocstoreBigintIdentityDoesNotUsePatchIntegerLimit(t *testing.T) {
	p := docstoreFixture([]byte("a")).Docstore
	f := &p.Files[0]
	f.LegacyID = 4294967296
	f.ID = DocstoreID("shared", "file", f.LegacyID)
	f.Facts["id"] = "4294967296"
	f.Entry = DocstoreFileEntry("shared", f.LegacyID)
	if err := ValidateDocstore(&p, true); err != nil {
		t.Fatal(err)
	}
}

func TestDocstoreTreeRejectsCyclesMissingAndCrossCustomerParents(t *testing.T) {
	directory := func(id int64, parent, customer string) DocstoreDirectorySource {
		facts := map[string]string{"id": "1", "name": "folder", "cust_id": customer}
		if id == 2 {
			facts["id"] = "2"
		}
		if parent != "" {
			facts["parent_dir_id"] = parent
		}
		d := DocstoreDirectorySource{ID: DocstoreID("customer", "directory", id), LegacyID: id, Kind: "customer", Name: "folder", Facts: facts}
		if parent == "1" {
			d.ParentID = DocstoreID("customer", "directory", 1)
		}
		if parent == "2" {
			d.ParentID = DocstoreID("customer", "directory", 2)
		}
		return d
	}
	for name, dirs := range map[string][]DocstoreDirectorySource{
		"cycle":          {directory(1, "2", "1"), directory(2, "1", "1")},
		"missing":        {directory(1, "2", "1")},
		"cross-customer": {directory(1, "", "1"), directory(2, "1", "2")},
	} {
		t.Run(name, func(t *testing.T) {
			p := DocstoreImport{Version: 1, SourceTimezone: "UTC", Directories: dirs}
			if ValidateDocstore(&p, false) == nil {
				t.Fatal("invalid graph accepted")
			}
		})
	}
	p := DocstoreImport{Version: 1, SourceTimezone: "UTC", Directories: []DocstoreDirectorySource{directory(1, "", "1"), directory(2, "1", "1")}}
	if err := ValidateDocstore(&p, false); err != nil {
		t.Fatal(err)
	}
}
