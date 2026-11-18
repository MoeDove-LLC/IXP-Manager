package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
)

const provisionBackupPassword = "synthetic-provision-backup-password"

type provisionBackupClient struct {
	t      *testing.T
	a      *app.App
	cookie *http.Cookie
	csrf   string
}

func (c *provisionBackupClient) request(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	conditional := (method == "PUT" || method == "DELETE") && strings.HasPrefix(path, "/api/resources/") && !strings.HasPrefix(path, "/api/resources/provisioning-profiles")
	tag := ""
	if conditional {
		latest := c.request("GET", path, nil)
		if latest.Code == 200 {
			tag = latest.Header().Get("ETag")
		}
		if row, ok := body.(app.Record); ok {
			copy := app.Record{}
			for key, value := range row {
				if key != "_etag" {
					copy[key] = value
				}
			}
			body = copy
		}
	}
	var data bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&data).Encode(body); err != nil {
			c.t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &data)
	request.RemoteAddr = "127.0.0.1:10000"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		request.AddCookie(c.cookie)
	}
	request.Header.Set("X-CSRF-Token", c.csrf)
	if tag != "" {
		request.Header.Set("If-Match", tag)
	}
	response := httptest.NewRecorder()
	c.a.ServeHTTP(response, request)
	if cookies := response.Result().Cookies(); len(cookies) > 0 {
		c.cookie = cookies[0]
	}
	return response
}
func (c *provisionBackupClient) call(method, path string, body any) app.Record {
	c.t.Helper()
	response := c.request(method, path, body)
	if response.Code < 200 || response.Code >= 300 {
		c.t.Fatalf("%s %s: HTTP %d %s", method, path, response.Code, response.Body.String())
	}
	result := app.Record{}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		c.t.Fatal(err)
	}
	if csrf, ok := result["csrfToken"].(string); ok {
		c.csrf = csrf
	}
	return result
}
func (c *provisionBackupClient) approve(revision app.Record) {
	c.t.Helper()
	path := "/api/provisioning/" + revision["id"].(string)
	// These are actual monitoring fragments; their validator is structural.
	// No fake executable and no DNS native validation success is injected.
	c.call("POST", path+"/validate", nil)
	c.call("POST", path+"/approve", app.Record{"checksum": revision["checksum"]})
}

type provisionBackupFixture struct {
	dir       string
	state     app.State
	plaintext map[string]string
	prunedID  string
}

func makeProvisionBackupFixture(t *testing.T, prune bool) provisionBackupFixture {
	return makeProvisionBackupSerialFixture(t, prune, 2026110701)
}
func makeProvisionBackupSerialFixture(t *testing.T, prune bool, baseline uint32) provisionBackupFixture {
	t.Helper()
	dir := t.TempDir()
	server, err := app.New(app.Config{DataDir: dir, SetupToken: "provision-backup-setup", NamedCheckzone: "/nonexistent/never-used-by-backup-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	client := &provisionBackupClient{t: t, a: server}
	client.call("POST", "/api/setup", app.Record{"name": "Backup admin", "email": "provision-backup@example.net", "password": provisionBackupPassword, "ixpName": "Backup IXP", "asn": 64500, "setupToken": "provision-backup-setup"})
	location := client.call("POST", "/api/resources/locations", app.Record{"name": "Backup DC"})
	sw := client.call("POST", "/api/resources/switches", app.Record{"name": "Backup switch", "locationId": location["id"], "managementIp": "192.0.2.250"})
	port := client.call("POST", "/api/resources/ports", app.Record{"name": "eth1", "switchId": sw["id"], "speed": 10000, "status": "active"})
	member := client.call("POST", "/api/resources/members", app.Record{"name": "Backup member", "asn": 64501, "status": "active"})
	vlan := client.call("POST", "/api/resources/vlans", app.Record{"name": "Backup LAN", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	client.call("POST", "/api/resources/connections", app.Record{"name": "Backup connection", "memberId": member["id"], "portId": port["id"], "vlanId": vlan["id"], "ipv4": "192.0.2.10", "status": "active", "dnsName4": "member.backup.example.net."})
	router := client.call("POST", "/api/resources/routers", app.Record{"name": "Backup router", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": vlan["id"]})
	client.call("POST", "/api/resources/peers", app.Record{"name": "Backup BGP session", "routerId": router["id"], "memberId": member["id"], "address": "192.0.2.10", "asn": 64501, "enabled": true, "maxPrefixes": 100, "allowedPrefixes": []string{"198.51.100.0/24"}})
	fixture := provisionBackupFixture{dir: dir, plaintext: map[string]string{}}
	for _, kind := range []string{"nagios", "smokeping", "dns-arpa"} {
		options := app.Record{"kind": kind, "vlanId": vlan["id"], "families": []int{4}}
		request := app.Record{"name": "Backup " + kind, "options": options}
		if kind == "dns-arpa" {
			options["dns"] = app.Record{"prefix": "192.0.2.0/24", "primaryNS": "ns1.example.net.", "responsibleMailbox": "hostmaster.example.net.", "nameServers": []string{"ns1.example.net."}, "nameSuffix": "backup.example.net."}
			request["dnsSerialFloor"] = baseline
		}
		profile := client.call("POST", "/api/resources/provisioning-profiles", request)
		revision := client.call("POST", "/api/provisioning", app.Record{"profileId": profile["id"]})
		id := revision["id"].(string)
		detail := client.call("GET", "/api/provisioning/"+id, nil)
		fixture.plaintext[id] = detail["config"].(string)
		if kind != "dns-arpa" {
			client.approve(revision)
		}
		if prune && kind == "nagios" {
			next := client.call("POST", "/api/provisioning", app.Record{"profileId": profile["id"]})
			nextID := next["id"].(string)
			fixture.plaintext[nextID] = client.call("GET", "/api/provisioning/"+nextID, nil)["config"].(string)
			client.approve(next)
			client.call("POST", "/api/provisioning/"+id+"/prune", app.Record{"checksum": revision["checksum"], "acknowledge": true, "reason": "Keep reviewed metadata while retiring the old fragment"})
			fixture.prunedID = id
		}
	}
	if err = server.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := app.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(func(state *app.State) error {
		// This retained high-water mark represents the externally published baseline
		// declared above. The newly generated DNS revision remains a draft, with no
		// native validation or fake approval. Counters can outlive pruned history.
		state.ProvisionApprovedSerials = map[string]uint32{"2.0.192.in-addr.arpa.": baseline}
		return nil
	})
	closeErr := store.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	// Exercise the actual startup graph, ciphertext and serial validation before
	// using this controlled metadata fixture in the backup workflow.
	verified, err := app.New(app.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = verified.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = app.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.state = store.Snapshot()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	next := baseline + 1
	if next == 0 {
		next = 1
	}
	if fixture.state.ProvisionSerials["2.0.192.in-addr.arpa."] != next {
		t.Fatal("DNS draft did not allocate the real next serial")
	}
	return fixture
}

func TestProvisioningBackupRestoresEncryptedArtifactsAndSerialCounters(t *testing.T) {
	fixture := makeProvisionBackupFixture(t, false)
	archive := filepath.Join(t.TempDir(), "provisioning.gz")
	if err := Create(fixture.dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	store, err := app.OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	after := store.Snapshot()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.ProvisionRevisions, fixture.state.ProvisionRevisions) || !reflect.DeepEqual(after.ProvisionSerials, fixture.state.ProvisionSerials) || !reflect.DeepEqual(after.ProvisionApprovedSerials, fixture.state.ProvisionApprovedSerials) {
		t.Fatal("provisioning metadata or DNS issued/approved serial counters changed")
	}
	if !reflect.DeepEqual(after.Resources["provisioning-profiles"], fixture.state.Resources["provisioning-profiles"]) {
		t.Fatal("generation profiles were not retained")
	}
	stateBytes, err := os.ReadFile(filepath.Join(target, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateBytes, []byte("Generated by MoeDove IXP Manager")) {
		t.Fatal("rendered configuration body remained in the hot JSON state")
	}
	for _, revision := range after.ProvisionRevisions {
		name := filepath.Join("provisioning", revision.ID+".enc")
		before, err := os.ReadFile(filepath.Join(fixture.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		restored, err := os.ReadFile(filepath.Join(target, name))
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(target, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, restored) || info.Mode().Perm() != 0600 || int64(len(restored)) != revision.ArtifactSize {
			t.Fatal("encrypted artifact bytes, size or private mode changed")
		}
		if bytes.Contains(restored, []byte(fixture.plaintext[revision.ID])) || bytes.Contains(stateBytes, []byte(fixture.plaintext[revision.ID])) {
			t.Fatal("plaintext provisioning body escaped its encrypted artifact")
		}
		if revision.Kind == "dns-arpa" && (revision.Status != "draft" || revision.Validation != "") {
			t.Fatal("backup fixture invented native DNS validation")
		}
	}
	restoredApp, err := app.New(app.Config{DataDir: target})
	if err != nil {
		t.Fatal(err)
	}
	defer restoredApp.Close()
	client := &provisionBackupClient{t: t, a: restoredApp}
	client.call("POST", "/api/login", app.Record{"email": "provision-backup@example.net", "password": provisionBackupPassword})
	for _, revision := range after.ProvisionRevisions {
		path := "/api/provisioning/" + revision.ID
		if body := client.call("GET", path, nil)["config"]; body != fixture.plaintext[revision.ID] {
			t.Fatal("restored artifact failed authenticated decryption/preview")
		}
		if revision.Kind != "dns-arpa" {
			response := client.request("GET", path+"/config", nil)
			if response.Code != 200 || response.Body.String() != fixture.plaintext[revision.ID] || response.Header().Get("X-Config-SHA256") != revision.Checksum {
				t.Fatal("restored approved fragment cannot be downloaded with original identity")
			}
		}
	}
}

func TestProvisioningBackupSkipsPrunedCiphertextAndRetainsReviewHistory(t *testing.T) {
	fixture := makeProvisionBackupFixture(t, true)
	archive := filepath.Join(t.TempDir(), "pruned-provisioning.gz")
	if err := Create(fixture.dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	store, err := app.OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.ProvisionRevisions, fixture.state.ProvisionRevisions) || !reflect.DeepEqual(state.ProvisionSerials, fixture.state.ProvisionSerials) || !reflect.DeepEqual(state.ProvisionApprovedSerials, fixture.state.ProvisionApprovedSerials) {
		t.Fatal("pruned review metadata or DNS serial high-water marks were lost")
	}
	pruned := false
	for _, revision := range state.ProvisionRevisions {
		if revision.ID == fixture.prunedID {
			pruned = revision.Pruned && !revision.PrunedAt.IsZero() && revision.PrunedBy != "" && revision.Status == "approved" && revision.Checksum != ""
		}
		_, err := os.Stat(filepath.Join(target, "provisioning", revision.ID+".enc"))
		if revision.Pruned && !os.IsNotExist(err) {
			t.Fatal("pruned ciphertext reappeared during restore")
		}
		if !revision.Pruned && err != nil {
			t.Fatal("pruning lost a referenced live artifact", err)
		}
	}
	if !pruned {
		t.Fatal("pruned approved review history not preserved")
	}
}

func TestRestoreRejectsUnreferencedProvisioningEvenWithValidManifest(t *testing.T) {
	dir := seed(t)
	path := filepath.Join(t.TempDir(), "orphan-provisioning.gz")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	manifest := Manifest{Version: 1, CreatedAt: time.Now().UTC()}
	for _, name := range []string{"state.json", "security.key", "provisioning/orphan.enc"} {
		data := []byte(base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 64)) + "\n")
		if name != "provisioning/orphan.enc" {
			data, err = os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
		}
		if err = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(data); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, Entry{Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err = tw.Write(data); err != nil {
		t.Fatal(err)
	}
	for _, close := range []func() error{tw.Close, gz.Close, file.Close} {
		if err = close(); err != nil {
			t.Fatal(err)
		}
	}
	// Read the archive back to ensure the extra entry really has a matching
	// manifest, so rejection cannot be explained by a malformed hash fixture.
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(input)
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(reader)
	sawOrphan := false
	for {
		header, e := tarReader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		payload, e := io.ReadAll(tarReader)
		if e != nil {
			t.Fatal(e)
		}
		if header.Name == "provisioning/orphan.enc" {
			sum := sha256.Sum256(payload)
			sawOrphan = manifest.Files[2].SHA256 == hex.EncodeToString(sum[:])
		}
	}
	reader.Close()
	input.Close()
	if !sawOrphan {
		t.Fatal("orphan manifest fixture invalid")
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err = Restore(path, target); err == nil || !strings.Contains(err.Error(), "unreferenced") {
		t.Fatal("valid-manifest unreferenced provisioning artifact was not rejected as unreferenced", err)
	}
	if _, err = os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("invalid backup published a restore directory")
	}
}

func TestProvisioningBackupPreservesRFC1982WrappedLedger(t *testing.T) {
	fixture := makeProvisionBackupSerialFixture(t, false, ^uint32(0))
	archive := filepath.Join(t.TempDir(), "wrapped.gz")
	if err := Create(fixture.dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	server, err := app.New(app.Config{DataDir: target})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := &provisionBackupClient{t: t, a: server}
	client.call("POST", "/api/login", app.Record{"email": "provision-backup@example.net", "password": provisionBackupPassword})
	var profile string
	for _, revision := range fixture.state.ProvisionRevisions {
		if revision.Kind != "dns-arpa" {
			continue
		}
		profile = revision.ProfileID
		detail := client.call("GET", "/api/provisioning/"+revision.ID, nil)
		if detail["dnsSerial"] != float64(1) || detail["config"] != fixture.plaintext[revision.ID] {
			t.Fatal("wrapped encrypted draft was not preserved")
		}
		if w := client.request("GET", "/api/provisioning/"+revision.ID+"/config", nil); w.Code != 409 {
			t.Fatal("backup invented approval for wrapped draft")
		}
	}
	next := client.call("POST", "/api/provisioning", app.Record{"profileId": profile})
	if next["dnsSerial"] != float64(2) {
		t.Fatal("restored ledger reused wrapped serial")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := app.OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored := store.Snapshot()
	if restored.ProvisionSerials["2.0.192.in-addr.arpa."] != 2 || restored.ProvisionApprovedSerials["2.0.192.in-addr.arpa."] != ^uint32(0) {
		t.Fatal("new draft changed externally established approved baseline")
	}
}
