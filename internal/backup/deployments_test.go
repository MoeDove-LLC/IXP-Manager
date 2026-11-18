package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/routing"
)

func legacyDeploymentBackup(t *testing.T) (string, string) {
	t.Helper()
	dir := seed(t)
	s, err := app.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	config := "router id 192.0.2.1;\nprotocol bgp example { password \"PrivateBGPKey_123\"; }\n"
	err = s.Update(func(s *app.State) error {
		s.Deployments = []app.Deployment{{ID: "legacy-config", RouterID: "historical-router", Platform: "bird2", Status: "approved", Config: config, Checksum: routing.Checksum(config), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ApprovedBy: "former-admin"}}
		return nil
	})
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	return dir, config
}

func TestBackupMigratesInlineDeploymentAndRestoresEncryptedArtifact(t *testing.T) {
	dir, config := legacyDeploymentBackup(t)
	archive := filepath.Join(t.TempDir(), "deployment.gz")
	if err := Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	stateBytes, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || bytes.Contains(stateBytes, []byte("PrivateBGPKey_123")) {
		t.Fatal("backup migration retained inline secret configuration")
	}
	ciphertext, err := os.ReadFile(filepath.Join(dir, "deployments", "legacy-config.enc"))
	if err != nil || bytes.Contains(ciphertext, []byte("PrivateBGPKey_123")) {
		t.Fatal("configuration artifact missing or not encrypted")
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(target, "deployments", "legacy-config.enc"))
	if err != nil || !bytes.Equal(restored, ciphertext) {
		t.Fatal("encrypted revision bytes changed during backup/restore")
	}
	s, err := app.OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	d := s.Snapshot().Deployments[0]
	s.Close()
	if d.Config != "" || d.ArtifactVersion != 1 || d.Checksum != routing.Checksum(config) || d.Status != "approved" || d.ApprovedBy != "former-admin" {
		t.Fatal("migration or restore changed approval or content identity")
	}
	// Restore already calls App.New, which authenticates ciphertext with the
	// restored key. Missing/corrupt artifacts must prevent making a new backup.
	if err := os.WriteFile(filepath.Join(dir, "deployments", "legacy-config.enc"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Create(dir, filepath.Join(t.TempDir(), "bad.gz")); err == nil {
		t.Fatal("backup accepted corrupted encrypted revision")
	}
}

func TestBackupPreservesPrunedDeploymentHistoryWithoutCiphertext(t *testing.T) {
	dir, _ := legacyDeploymentBackup(t)
	if err := Create(dir, filepath.Join(t.TempDir(), "first.gz")); err != nil {
		t.Fatal(err)
	}
	s, err := app.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(s *app.State) error {
		s.Deployments[0].Pruned = true
		s.Deployments[0].PrunedAt = time.Now().UTC()
		s.Deployments[0].PrunedBy = "admin"
		return nil
	})
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "pruned.gz")
	if err := Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restore")
	if err := Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "deployments", "legacy-config.enc")); !os.IsNotExist(err) {
		t.Fatal("pruned ciphertext reappeared during restore")
	}
	s, err = app.OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	d := s.Snapshot().Deployments[0]
	s.Close()
	if !d.Pruned || d.PrunedBy != "admin" || d.PrunedAt.IsZero() || d.Status != "approved" {
		t.Fatal("pruned deployment history was not preserved")
	}
}

func TestRestoreRejectsUnreferencedDeploymentEvenWithValidManifest(t *testing.T) {
	dir := seed(t)
	path := filepath.Join(t.TempDir(), "extra-artifact.gz")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	manifest := Manifest{Version: 1, CreatedAt: time.Now().UTC()}
	for _, name := range []string{"state.json", "security.key", "deployments/orphan.enc"} {
		data := []byte(base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 64)) + "\n")
		if name != "deployments/orphan.enc" {
			data, err = os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, Entry{Name: name, Size: int64(len(data)), SHA256: routing.Checksum(string(data))})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	for _, close := range []func() error{tw.Close, gz.Close, f.Close} {
		if err := close(); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(path, target); err == nil {
		t.Fatal("unreferenced deployment accepted solely because its manifest hash matched")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("invalid backup published a restore target")
	}
}
