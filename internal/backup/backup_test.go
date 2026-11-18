package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/moedove/ixp-manager/internal/loa"
	"github.com/moedove/ixp-manager/internal/notify"
	"github.com/moedove/ixp-manager/internal/policy"
	"github.com/moedove/ixp-manager/internal/telemetry"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
)

func seed(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s, err := app.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Update(func(s *app.State) error { s.Settings["name"] = "Restore test"; return nil }); err != nil {
		t.Fatal(err)
	}
	s.Close()
	return dir
}
func TestRoundTripAndExclusiveDestination(t *testing.T) {
	source := seed(t)
	archive := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := Create(source, archive); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(archive)
	if info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions")
	}
	if err := Create(source, archive); err == nil {
		t.Fatal("existing archive overwritten")
	}
	beforeVerify, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := Verify(archive, work); err != nil {
		t.Fatal(err)
	}
	if afterVerify, err := os.ReadFile(archive); err != nil || !bytes.Equal(beforeVerify, afterVerify) {
		t.Fatal("verification changed the backup archive", err)
	}
	if entries, err := os.ReadDir(work); err != nil || len(entries) != 0 {
		t.Fatal("verification left a temporary copy", err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	s, err := app.OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Settings["name"] != "Restore test" {
		t.Fatal("settings lost")
	}
	s.Close()
	if err := Restore(archive, target); err == nil {
		t.Fatal("existing data directory overwritten")
	}
	a, _ := os.ReadFile(filepath.Join(source, "security.key"))
	b, _ := os.ReadFile(filepath.Join(target, "security.key"))
	if !bytes.Equal(a, b) {
		t.Fatal("encryption key changed")
	}
}
func TestRunningServerBlocksBackup(t *testing.T) {
	dir := seed(t)
	s, err := app.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := Create(dir, filepath.Join(t.TempDir(), "backup.tar.gz")); err == nil {
		t.Fatal("concurrent backup ignored state lock")
	}
}
func TestRestoreRejectsMaliciousPathsAndLinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind byte
	}{{"../escape", tar.TypeReg}, {"/absolute", tar.TypeReg}, {"blobs/link", tar.TypeSymlink}, {"state.json", tar.TypeLink}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.gz")
			f, _ := os.Create(path)
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			tw.WriteHeader(&tar.Header{Name: tc.name, Typeflag: tc.kind, Linkname: "/etc/passwd", Mode: 0600})
			tw.Close()
			gz.Close()
			f.Close()
			target := filepath.Join(t.TempDir(), "new")
			if err := Restore(path, target); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("failed restore published target")
			}
		})
	}
}
func TestRestoreRejectsTamperAndTrailingStream(t *testing.T) {
	source := seed(t)
	original := filepath.Join(t.TempDir(), "original.gz")
	if err := Create(source, original); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(original)
	for _, mode := range []string{"checksum", "trailing", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			b := append([]byte(nil), raw...)
			switch mode {
			case "checksum":
				b[len(b)-5] ^= 0xff
			case "trailing":
				b = append(b, raw...)
			case "truncated":
				b = b[:len(b)/2]
			}
			bad := filepath.Join(t.TempDir(), "bad.gz")
			os.WriteFile(bad, b, 0600)
			work := t.TempDir()
			if err := Verify(bad, work); err == nil {
				t.Fatal("verification accepted damaged archive")
			}
			if entries, err := os.ReadDir(work); err != nil || len(entries) != 0 {
				t.Fatal("failed verification left temporary data", err)
			}
			target := filepath.Join(t.TempDir(), "new")
			if err := Restore(bad, target); err == nil {
				t.Fatal("damaged archive accepted")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("damaged backup published")
			}
		})
	}
}

func TestBackupRestoresBlobAndMatchingMFAKey(t *testing.T) {
	dir := seed(t)
	s, err := app.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := os.ReadFile(filepath.Join(dir, "security.key"))
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	encrypted := base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte("JBSWY3DPEHPK3PXP"), []byte("admin")))
	content := []byte("verified attachment")
	sum := sha256.Sum256(content)
	blob := "blob-" + strings.Repeat("a", 24)
	os.Mkdir(filepath.Join(dir, "blobs"), 0700)
	os.WriteFile(filepath.Join(dir, "blobs", blob), content, 0600)
	err = s.Update(func(s *app.State) error {
		s.Resources["users"] = []app.Record{{"id": "admin", "name": "Administrator", "email": "admin@example.net", "role": "admin"}}
		s.Security["admin"] = app.SecurityUser{MFAEnabled: true, Secret: encrypted}
		s.Documents = []app.Document{{ID: "doc", Name: "document.txt", Visibility: "public", Version: 1, BlobID: blob, Size: int64(len(content)), Checksum: hex.EncodeToString(sum[:]), MediaType: "text/plain"}}
		return nil
	})
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "full.gz")
	if err = Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restore")
	if err = Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Config{DataDir: target})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/api/public/documents/doc/download", nil))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), content) {
		t.Fatal("restored attachment invalid")
	}
}

func TestBackupRestoresPolicyAndTelemetry(t *testing.T) {
	dir := seed(t)
	now := time.Now().UTC()
	snapshot := policy.ROASnapshot{Version: 1, Source: "fixture", FetchedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), ROAs: []policy.ROA{{Prefix: "192.0.2.0/24", MaxLength: 24, ASN: 64501}}}
	raw, _ := json.Marshal(snapshot)
	hash := sha256.Sum256(raw)
	os.Mkdir(filepath.Join(dir, "policies"), 0700)
	os.WriteFile(filepath.Join(dir, "policies", "fixture.json"), raw, 0600)
	s, err := app.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(s *app.State) error {
		s.PolicySnapshots = []app.PolicySnapshot{{ID: "fixture", Kind: "rpki", Checksum: hex.EncodeToString(hash[:]), Size: int64(len(raw)), Source: "fixture", FetchedAt: snapshot.FetchedAt, ExpiresAt: snapshot.ExpiresAt, EntryCount: 1}}
		return nil
	})
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	observations, err := telemetry.Open(filepath.Join(dir, "telemetry"))
	if err != nil {
		t.Fatal(err)
	}
	err = observations.Ingest(telemetry.Batch{RouterID: "router", ObservedAt: now.Add(-time.Second), Ports: []telemetry.PortSample{{PortID: "port", MemberID: "member", InOctets: "9007199254740993", OutOctets: "10", OperStatus: "up"}}}, now)
	observations.Close()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "all.gz")
	if err = Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err = Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "policies", "fixture.json"))
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("policy snapshot lost")
	}
	restored, err := telemetry.Open(filepath.Join(target, "telemetry"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.Snapshot(now).Ports["port"].InOctets != "9007199254740993" {
		t.Fatal("counter lost precision or history during backup")
	}
}

func TestBackupRetainsLOABlobsAndEncryptedOutbox(t *testing.T) {
	dir := seed(t)
	now := time.Now().UTC().Truncate(time.Second)
	snapshot := loa.Data{ID: "loa-backup", ExchangeName: "萌鸽", MemberName: "中文成员", MemberASN: 64501, Ports: []string{"端口1"}, AllocationID: "allocation", IssuedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}
	pdf, html, err := loa.Generate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	pdfID := "blob-" + strings.Repeat("p", 24)
	htmlID := "blob-" + strings.Repeat("h", 24)
	os.Mkdir(filepath.Join(dir, "blobs"), 0700)
	os.WriteFile(filepath.Join(dir, "blobs", pdfID), pdf, 0600)
	os.WriteFile(filepath.Join(dir, "blobs", htmlID), []byte(html), 0600)
	pdfHash, htmlHash := sha256.Sum256(pdf), sha256.Sum256([]byte(html))
	key, _ := os.ReadFile(filepath.Join(dir, "security.key"))
	s, err := app.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(s *app.State) error {
		s.LOAs = []app.LOA{{ID: snapshot.ID, PortIDs: []string{"old-port"}, AllocationID: snapshot.AllocationID, MemberID: "old-member", CreatedAt: snapshot.IssuedAt, ExpiresAt: snapshot.ExpiresAt, Snapshot: snapshot, PDFBlobID: pdfID, HTMLBlobID: htmlID, PDFSize: int64(len(pdf)), HTMLSize: int64(len(html)), Checksum: hex.EncodeToString(pdfHash[:]), HTMLChecksum: hex.EncodeToString(htmlHash[:])}}
		_, err := s.Outbox.Enqueue("test", notify.Message{From: "from@example.net", To: "to@example.net", Subject: "恢复测试", Text: "opaque-test-reset-link"}, key, now, now.Add(time.Hour))
		return err
	})
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "artifacts.gz")
	if err = Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restore")
	if err = Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(target, "blobs", pdfID))
	if err != nil || !bytes.Equal(b, pdf) {
		t.Fatal("immutable PDF changed or removed")
	}
	restored, err := app.OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	state := restored.Snapshot()
	restoredKey, _ := os.ReadFile(filepath.Join(target, "security.key"))
	msg, err := notify.OpenMessage(restoredKey, state.Outbox.Entries[0])
	if err != nil || msg.Text != "opaque-test-reset-link" {
		t.Fatal("outbox encryption key did not restore")
	}
}
