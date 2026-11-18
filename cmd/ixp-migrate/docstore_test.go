package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func cliDocstoreFixture(t *testing.T, root string) ([]byte, []byte) {
	t.Helper()
	content := []byte("synthetic docstore bytes\x00\xff")
	digest := sha256.Sum256(content)
	checksum := hex.EncodeToString(digest[:])
	for _, name := range []string{"shared/opaque-file", "customer/1/opaque-file"} {
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	source := legacyExport{Tables: map[string][]row{
		"cust":                          {{"id": 1, "name": "Legacy member", "autsys": 64501, "status": 1}},
		"docstore_directories":          {{"id": 1, "name": "Shared folder", "description": "exact source description", "parent_dir_id": nil, "created_at": "2020-01-01 12:00:00"}},
		"docstore_customer_directories": {{"id": 1, "cust_id": 1, "name": "Customer folder", "parent_dir_id": nil}},
		"docstore_files":                {{"id": 1, "docstore_directory_id": 1, "name": "../../original.txt", "disk": "docstore", "path": "opaque-file", "sha256": checksum, "min_privs": 2, "file_last_updated": "2020-01-02 03:04:05", "created_by": 7}},
		"docstore_customer_files":       {{"id": 1, "cust_id": 1, "docstore_customer_directory_id": 1, "name": "Customer.txt", "disk": "docstore_customers", "path": "1/opaque-file", "sha256": checksum, "min_privs": 1}},
		"docstore_logs":                 {{"id": 1, "docstore_file_id": 1, "downloaded_by": 7, "created_at": "2020-01-02 03:05:06"}, {"id": 2, "docstore_file_id": 1, "downloaded_by": nil, "created_at": "2020-01-02"}},
	}}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	return raw, content
}
func readDocstoreCLI(t *testing.T, output string) (*migrate.DocstoreBundle, map[string][]byte) {
	t.Helper()
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	contents := map[string][]byte{}
	b, err := migrate.ReadDocstoreBundle(f, func(file migrate.DocstoreFileSource, r io.Reader) error {
		contents[file.ID], err = io.ReadAll(r)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return b, contents
}

func TestDocstoreCLIOriginalDiskMappingNamespacesAndAccessRecords(t *testing.T) {
	root := t.TempDir()
	raw, content := cliDocstoreFixture(t, root)
	output := filepath.Join(t.TempDir(), "docstore.tar.gz")
	var stdout, stderr bytes.Buffer
	args := []string{"--docstore-bundle", "--docstore-disk-root", "docstore=" + filepath.Join(root, "shared"), "--docstore-disk-root", "docstore_customers=" + filepath.Join(root, "customer"), "--source-timezone", "UTC", "--output", output}
	if err := run(args, bytes.NewReader(raw), &stdout, &stderr); err != nil {
		t.Fatalf("%v %s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatal("private archive or metadata leaked to stdout")
	}
	stat, err := os.Stat(output)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("output is not private")
	}
	b, contents := readDocstoreCLI(t, output)
	if len(b.Docstore.Files) != 2 || len(b.Docstore.Directories) != 2 || len(b.Docstore.Logs) != 2 || b.Docstore.SourceTimezone != "UTC" {
		t.Fatal("metadata lost")
	}
	if b.Docstore.Files[0].ID == b.Docstore.Files[1].ID || b.Docstore.Directories[0].ID == b.Docstore.Directories[1].ID {
		t.Fatal("shared/customer source IDs collided")
	}
	for _, f := range b.Docstore.Files {
		if !bytes.Equal(contents[f.ID], content) {
			t.Fatal("wrong disk-relative file read")
		}
	}
	shared, customer := b.Docstore.Files[0], b.Docstore.Files[1]
	if shared.MinPrivileges != 2 || shared.MemberID != "" || customer.MinPrivileges != 1 || customer.MemberID != "legacy-members-1" {
		t.Fatal("access requirement changed")
	}
	if shared.Name != "../../original.txt" || shared.Facts["file_last_updated"] != "2020-01-02 03:04:05" {
		t.Fatal("source naming/date precision was rewritten")
	}
	log := b.Docstore.Logs[0]
	if log.FileID != shared.ID || log.DownloadedBy == nil || *log.DownloadedBy != 7 || b.Docstore.Logs[1].DownloadedBy != nil || b.Docstore.Logs[1].CreatedAt != "2020-01-02" {
		t.Fatal("source access facts changed or bound to customer file")
	}
	encoded, _ := json.Marshal(b)
	for _, forbidden := range []string{"\"resources\"", "\"audit\"", "\"success\"", "\"versions\""} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("invented source semantics %s", forbidden)
		}
	}
	before, _ := os.ReadFile(output)
	if err = run(args, bytes.NewReader(raw), io.Discard, io.Discard); err == nil {
		t.Fatal("existing archive overwritten")
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(before, after) {
		t.Fatal("existing archive changed")
	}
}

func TestDocstoreCLIOrdinaryInventoryWarnsAndStrictRefusesOmission(t *testing.T) {
	raw, _ := cliDocstoreFixture(t, t.TempDir())
	var out, log bytes.Buffer
	if err := run([]string{"--source-timezone", "UTC"}, bytes.NewReader(raw), &out, &log); err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["docstore"]; ok {
		t.Fatal("archive metadata mixed into ordinary resource import")
	}
	if !bytes.Contains(result["resources"], []byte("legacy-members-1")) || !strings.Contains(log.String(), "--docstore-bundle") {
		t.Fatal("resource extraction or explicit omission warning missing")
	}
	out.Reset()
	log.Reset()
	if err := run([]string{"--strict", "--source-timezone", "UTC"}, bytes.NewReader(raw), &out, &log); err == nil || out.Len() != 0 {
		t.Fatal("strict mode silently omitted archive")
	}
}

func TestDocstoreCLIUnknownPrivilegeAndMemberStayReviewOnly(t *testing.T) {
	raw, _ := cliDocstoreFixture(t, t.TempDir())
	source := decodeSample(t, string(raw))
	delete(source.Tables["docstore_files"][0], "min_privs")
	delete(source.Tables, "cust")
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	if result.Docstore.Files[0].MinPrivileges != -1 || result.Docstore.Files[1].MemberID != "" || len(result.Warnings) == 0 {
		t.Fatal("unknown authorization widened")
	}
}

func TestDocstoreCLIRequiresExplicitRootsAndAtomicNewOutput(t *testing.T) {
	for _, kind := range []string{"no-root", "no-output", "duplicate-root", "path-traversal", "source-hash", "cross-customer-tree", "mode-conflict"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			raw, _ := cliDocstoreFixture(t, root)
			output := filepath.Join(t.TempDir(), "docstore.tar.gz")
			args := []string{"--docstore-bundle", "--source-timezone", "UTC", "--output", output, "--docstore-disk-root", "docstore=" + filepath.Join(root, "shared"), "--docstore-disk-root", "docstore_customers=" + filepath.Join(root, "customer")}
			source := decodeSample(t, string(raw))
			switch kind {
			case "no-root":
				args = args[:5]
			case "no-output":
				args[4] = "-"
			case "duplicate-root":
				args = append(args, "--docstore-disk-root", "docstore="+root)
			case "path-traversal":
				source.Tables["docstore_files"][0]["path"] = "../outside"
			case "source-hash":
				source.Tables["docstore_files"][0]["sha256"] = strings.Repeat("f", 64)
			case "cross-customer-tree":
				source.Tables["docstore_customer_directories"][0]["cust_id"] = json.Number("2")
			case "mode-conflict":
				args = append(args, "--patch-bundle")
			}
			raw, err := json.Marshal(source)
			if err != nil {
				t.Fatal(err)
			}
			var out, log bytes.Buffer
			if err = run(args, bytes.NewReader(raw), &out, &log); err == nil {
				t.Fatal("unsafe export accepted")
			}
			if out.Len() != 0 {
				t.Fatal("failed export leaked content")
			}
			entries, err := os.ReadDir(filepath.Dir(output))
			if err != nil || len(entries) != 0 {
				t.Fatal("failed export left partial files")
			}
		})
	}
}
