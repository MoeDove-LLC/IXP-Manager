package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

func artifactFixture(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &App{store: store}, dir
}
func artifactConfig(t *testing.T) string {
	t.Helper()
	config, err := routing.RenderFor("bird2", routing.Router{ID: "router", ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "192.0.2.1"}, []routing.Peer{{ID: "peer", ASN: 64501, Address: "192.0.2.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"198.51.100.0/24"}, Password: "legacy-private-bgp-secret", AuthenticationRevision: token(16)}})
	if err != nil {
		t.Fatal(err)
	}
	return config
}
func legacyDeployment(config string) Deployment {
	return Deployment{ID: token(16), RouterID: "router", Platform: "bird2", Status: "draft", Config: config, Checksum: routing.Checksum(config), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
}
func TestDeploymentArtifactMigratesInlineWithoutLeakingSecrets(t *testing.T) {
	a, dir := artifactFixture(t)
	config := artifactConfig(t)
	legacy := legacyDeployment(config)
	if err := a.store.Update(func(s *State) error { s.Deployments = []Deployment{legacy}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDeploymentArtifacts(a.store); err != nil {
		t.Fatal(err)
	}
	d := a.store.Snapshot().Deployments[0]
	if d.ID != legacy.ID || d.Checksum != legacy.Checksum || d.Status != legacy.Status || d.Config != "" || d.ArtifactVersion != 1 {
		t.Fatal("migration changed revision or retained inline content")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile(filepath.Join(dir, "deployments", d.ID+".enc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "legacy-private-bgp-secret") || strings.Contains(string(artifact), "legacy-private-bgp-secret") {
		t.Fatal("plaintext BGP secret persisted")
	}
	info, _ := os.Stat(filepath.Join(dir, "deployments", d.ID+".enc"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("artifact permissions not private")
	}
	actual, err := a.readDeploymentArtifact(d)
	if err != nil || actual != config {
		t.Fatal("migration did not preserve exact published bytes")
	}
	view, err := a.deploymentDetail(d)
	if err != nil {
		t.Fatal(err)
	}
	jsonView, _ := json.Marshal(view)
	if view["redacted"] != true || strings.Contains(string(jsonView), "legacy-private-bgp-secret") || strings.Contains(string(jsonView), d.Checksum) || strings.Contains(string(jsonView), d.ArtifactChecksum) || strings.Contains(string(jsonView), dir) {
		t.Fatal("public preview exposed secret/hash oracle or artifact internals")
	}
	before := string(artifact)
	if err = MigrateDeploymentArtifacts(a.store); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(dir, "deployments", d.ID+".enc"))
	if string(again) != before {
		t.Fatal("repeat migration rewrote immutable ciphertext")
	}
}
func TestDeploymentMigrationRecoversPublishedCipherBeforeMetadataCommit(t *testing.T) {
	a, dir := artifactFixture(t)
	config := artifactConfig(t)
	legacy := legacyDeployment(config)
	a.store.Update(func(s *State) error { s.Deployments = []Deployment{legacy}; return nil })
	os.MkdirAll(filepath.Join(dir, "deployments"), 0700)
	staged := legacy
	if err := a.store.Update(func(s *State) error { return a.writeDeploymentArtifact(&staged, config) }); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "deployments", legacy.ID+".enc"))
	if err := MigrateDeploymentArtifacts(a.store); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "deployments", legacy.ID+".enc"))
	if string(before) != string(after) || a.store.Snapshot().Deployments[0].Config != "" {
		t.Fatal("crash recovery did not adopt exact already-published ciphertext")
	}
}
func TestDeploymentAtomicPublishAndCrashOrphans(t *testing.T) {
	a, dir := artifactFixture(t)
	config := artifactConfig(t)
	if err := a.initDeploymentArtifacts(); err != nil {
		t.Fatal(err)
	}
	d := legacyDeployment(config)
	d.Config = ""
	if err := a.store.Update(func(s *State) error {
		if err := a.writeDeploymentArtifact(&d, config); err != nil {
			return err
		}
		s.Deployments = append(s.Deployments, d)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "deployments", d.ID+".enc")
	before, _ := os.ReadFile(path)
	if err := a.store.Update(func(s *State) error { duplicate := d; return a.writeDeploymentArtifact(&duplicate, config) }); err == nil {
		t.Fatal("immutable final path overwritten")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed duplicate publish altered existing revision")
	}
	orphan := legacyDeployment(config)
	orphan.Config = ""
	if err := a.store.Update(func(s *State) error { return a.writeDeploymentArtifact(&orphan, config) }); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(dir, "deployments", ".deployment-"+token(16)+".tmp")
	os.WriteFile(temp, []byte("partial-ciphertext"), 0600)
	if err := a.initDeploymentArtifacts(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{temp, filepath.Join(dir, "deployments", orphan.ID+".enc")} {
		if _, err := os.Lstat(file); !os.IsNotExist(err) {
			t.Fatal("crashed uncommitted artifact occupied quota")
		}
	}
	if _, err := a.readDeploymentArtifact(d); err != nil {
		t.Fatal("orphan recovery deleted referenced revision")
	}
}
func TestDeploymentIntegrityAndWrongKeyFailClosed(t *testing.T) {
	a, dir := artifactFixture(t)
	config := artifactConfig(t)
	legacy := legacyDeployment(config)
	a.store.Update(func(s *State) error { s.Deployments = []Deployment{legacy}; return nil })
	if err := a.initDeploymentArtifacts(); err != nil {
		t.Fatal(err)
	}
	d := a.store.Snapshot().Deployments[0]
	path := filepath.Join(dir, "deployments", d.ID+".enc")
	original, _ := os.ReadFile(path)
	corrupt := append([]byte(nil), original...)
	corrupt[0] ^= 1
	os.WriteFile(path, corrupt, 0600)
	if _, err := a.readDeploymentArtifact(d); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	os.WriteFile(path, original, 0600)
	key := append([]byte(nil), a.store.securityKey...)
	a.store.securityKey = make([]byte, 32)
	if _, err := a.readDeploymentArtifact(d); err == nil {
		t.Fatal("wrong encryption key accepted")
	}
	a.store.securityKey = key
	os.Remove(path)
	if err := a.initDeploymentArtifacts(); err == nil {
		t.Fatal("missing referenced artifact ignored")
	}
}
func TestDeploymentReaderRejectsFIFOAndDirectorySymlink(t *testing.T) {
	a, dir := artifactFixture(t)
	config := artifactConfig(t)
	d := legacyDeployment(config)
	a.store.Update(func(s *State) error { s.Deployments = []Deployment{d}; return nil })
	if err := a.initDeploymentArtifacts(); err != nil {
		t.Fatal(err)
	}
	d = a.store.Snapshot().Deployments[0]
	path := filepath.Join(dir, "deployments", d.ID+".enc")
	os.Remove(path)
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := a.readDeploymentArtifact(d); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("artifact open blocked on FIFO")
	}
	os.Remove(path)
	b, other := artifactFixture(t)
	os.Symlink(filepath.Join(dir, "deployments"), filepath.Join(other, "deployments"))
	if err := b.initDeploymentArtifacts(); err == nil {
		t.Fatal("symlink deployment directory accepted")
	}
}
func TestClosedDeploymentInstanceCannotReadOrUnlinkReplacement(t *testing.T) {
	a, dir := artifactFixture(t)
	config := artifactConfig(t)
	d := legacyDeployment(config)
	a.store.Update(func(s *State) error { s.Deployments = []Deployment{d}; return nil })
	if err := a.initDeploymentArtifacts(); err != nil {
		t.Fatal(err)
	}
	d = a.store.Snapshot().Deployments[0]
	a.store.Close()
	replacement, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	current := &App{store: replacement}
	if _, err = a.readDeploymentArtifact(d); err == nil {
		t.Fatal("closed instance read replacement-era bytes")
	}
	pruned := d
	pruned.Pruned = true
	pruned.PrunedAt = time.Now()
	pruned.PrunedBy = "operator"
	a.store.mu.Lock()
	_, err = a.removePrunedDeployment(pruned)
	a.store.mu.Unlock()
	if err == nil {
		t.Fatal("closed instance unlinked replacement artifact")
	}
	if _, err = current.readDeploymentArtifact(d); err != nil {
		t.Fatal("replacement artifact changed by stale instance")
	}
}
func TestDeploymentStorageQuotaFailsWithoutPlaintextOrPartialPublish(t *testing.T) {
	a, dir := artifactFixture(t)
	if err := a.initDeploymentArtifacts(); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "deployments", "quota.enc")
	f, err := os.OpenFile(blocker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(deploymentStorageLimit); err != nil {
		t.Fatal(err)
	}
	f.Close()
	config := artifactConfig(t)
	d := legacyDeployment(config)
	d.Config = ""
	if err = a.store.Update(func(s *State) error { return a.writeDeploymentArtifact(&d, config) }); err == nil {
		t.Fatal("storage bound ignored")
	}
	if _, err = os.Stat(filepath.Join(dir, "deployments", d.ID+".enc")); !os.IsNotExist(err) {
		t.Fatal("quota failure published candidate")
	}
	os.Remove(blocker)
}
func TestDeploymentRetentionProtectsCurrentButAllowsDeletedRouter(t *testing.T) {
	c, _ := setupClient(t)
	v := c.create("vlans", Record{"name": "LAN", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	r := c.create("routers", Record{"name": "router", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": v["id"]})
	dto := c.ok("POST", "/api/deployments", Record{"routerId": r["id"]})
	id := str(dto, "id")
	c.a.store.Update(func(s *State) error {
		s.Deployments[0].Status = "approved"
		s.Deployments[0].ApprovedBy = accountIDUnsafe(s)
		return nil
	})
	body := Record{"checksum": dto["checksum"], "acknowledge": true, "reason": "retention test"}
	if w := c.request("POST", "/api/deployments/"+id+"/prune", body); w.Code != 409 {
		t.Fatal("current approved artifact pruned")
	}
	c.ok("DELETE", "/api/resources/routers/"+str(r, "id"), nil)
	c.ok("POST", "/api/deployments/"+id+"/prune", body)
	if w := c.request("GET", "/api/deployments/"+id, nil); w.Code != 410 {
		t.Fatal("pruned preview claimed availability")
	}
	if err := c.a.initDeploymentArtifacts(); err != nil {
		t.Fatal("pruned artifact prevented startup")
	}
	c.ok("DELETE", "/api/deployments/"+id, body)
}
func accountIDUnsafe(s *State) string { return str(s.Resources["users"][0], "id") }
