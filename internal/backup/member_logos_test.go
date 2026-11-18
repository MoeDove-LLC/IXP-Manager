package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
)

func TestMemberLogoBackupPreservesPrivatePublicationAndExactBytes(t *testing.T) {
	dir := seed(t)
	var imageData bytes.Buffer
	_ = png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 4, 3)))
	data := imageData.Bytes()
	sum := sha256.Sum256(data)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s, e := app.OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{"private-logo", "public-logo"}
	blobs := []string{"blob-aaaaaaaaaaaaaaaaaaaaaaaa", "blob-bbbbbbbbbbbbbbbbbbbbbbbb"}
	if e = s.Update(func(s *app.State) error {
		s.MemberLogos = map[string]app.MemberLogo{}
		for i, id := range ids {
			s.Resources["members"] = append(s.Resources["members"], app.Record{"id": id, "name": id, "asn": 64501 + i, "status": "active", "createdAt": now, "updatedAt": now})
			s.MemberLogos[id] = app.MemberLogo{MemberID: id, MemberCreatedAt: now, Revision: "revision-" + id, UpdatedAt: now, BlobID: blobs[i], Checksum: hex.EncodeToString(sum[:]), Size: int64(len(data)), Width: 4, Height: 3, Published: i == 1}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	s.Close()
	_ = os.MkdirAll(filepath.Join(dir, "blobs"), 0700)
	for _, b := range blobs {
		if e = os.WriteFile(filepath.Join(dir, "blobs", b), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if e = Create(dir, archive); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if e = Restore(archive, target); e != nil {
		t.Fatal(e)
	}
	a, e := app.New(app.Config{DataDir: target})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	c := &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"name": "Restored logo admin", "email": "logo-restore@example.test", "password": provisionBackupPassword, "ixpName": "Restore logo", "asn": 64500, "setupToken": a.BootstrapToken()})
	for i, id := range ids {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", "/api/public/members/"+id+"/logo", nil))
		if i == 0 && w.Code != 404 {
			t.Fatal("private logo became public on restore")
		}
		if i == 1 && (w.Code != 200 || !bytes.Equal(data, w.Body.Bytes())) {
			t.Fatal("public logo content lost", w.Code)
		}
	}
	if w := c.request("GET", "/api/members/private-logo/logo/content", nil); w.Code != 200 || !bytes.Equal(data, w.Body.Bytes()) {
		t.Fatal("restored private logo missing for authorized reader", w.Code)
	}
	c.call("POST", "/api/members/public-logo/suspend", app.Record{"reason": "verify restored publication follows current lifecycle"})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/api/public/members/public-logo/logo", nil))
	if w.Code != 404 {
		t.Fatal("restored inactive member retained public access")
	}
	// Corruption is rejected on backup, rather than copied as a valid artifact.
	if e = os.WriteFile(filepath.Join(dir, "blobs", blobs[0]), bytes.Repeat([]byte("x"), len(data)), 0600); e != nil {
		t.Fatal(e)
	}
	if e = Create(dir, filepath.Join(t.TempDir(), "corrupt.tar.gz")); e == nil {
		t.Fatal("corrupt member logo backed up")
	}
}
