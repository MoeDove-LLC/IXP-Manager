package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func TestLogoCLIOrdinaryInventoryAndExplicitPrivateBundle(t *testing.T) {
	var imageData bytes.Buffer
	_ = png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 3, 2)))
	sum := sha1.Sum(imageData.Bytes())
	key := hex.EncodeToString(sum[:]) + ".png"
	root := t.TempDir()
	file := filepath.Join(root, "logos", key[:1], key[1:2], key)
	_ = os.MkdirAll(filepath.Dir(file), 0700)
	_ = os.WriteFile(file, imageData.Bytes(), 0600)
	input, _ := json.Marshal(legacyExport{Tables: map[string][]row{"cust": {{"id": 1, "name": "Member", "autsys": 64501, "status": 1}}, "logos": {{"id": 2, "customer_id": 1, "stored_name": key, "original_name": "../../source.png", "uploaded_by": "old-user-not-authority", "created_at": "2010-01-01", "width": 3, "height": 2, "type": "image/png"}}}})
	var stdout, stderr bytes.Buffer
	if e := run(nil, bytes.NewReader(input), &stdout, &stderr); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(stderr.String(), "logo images are excluded") || strings.Contains(stdout.String(), "old-user-not-authority") || strings.Contains(stdout.String(), key) {
		t.Fatal("ordinary JSON retained logo bytes/source or lost warning")
	}
	stdout.Reset()
	stderr.Reset()
	if e := run([]string{"--strict"}, bytes.NewReader(input), &stdout, &stderr); e == nil || stdout.Len() != 0 {
		t.Fatal("strict allowed omitted logo archive")
	}
	out := filepath.Join(t.TempDir(), "logo.json")
	args := []string{"--logo-bundle", "--logo-public-root", root, "--output", out}
	if e := run(args, bytes.NewReader(input), &stdout, &stderr); e != nil {
		t.Fatal(e)
	}
	info, _ := os.Stat(out)
	if info.Mode().Perm() != 0600 {
		t.Fatal("private output mode")
	}
	raw, _ := os.ReadFile(out)
	b, e := migrate.ReadLogoBundle(bytes.NewReader(raw))
	if e != nil || len(b.Items) != 1 || b.Items[0].Source.MemberID != "legacy-members-1" {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte(`"resources"`)) || bytes.Contains(raw, []byte(`"published"`)) {
		t.Fatal("logo bundle mixed inventory or publication grants")
	}
	if e = run(args, bytes.NewReader(input), &stdout, &stderr); e == nil {
		t.Fatal("existing bundle overwritten")
	}
	if e = run([]string{"--logo-bundle", "--patch-bundle"}, bytes.NewReader(input), &stdout, &stderr); e == nil {
		t.Fatal("mixed bundle modes accepted")
	}
}
