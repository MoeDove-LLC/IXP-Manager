package backup

import (
	"bytes"
	"encoding/json"
	"github.com/moedove/ixp-manager/internal/app"
	external "github.com/moedove/ixp-manager/internal/integrations"
	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupIncludesReviewedSourceArtifacts(t *testing.T) {
	dir := t.TempDir()
	a, err := app.New(app.Config{DataDir: dir, SetupToken: "private-test-setup"})
	if err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	var csrf string
	call := func(method, path string, body any, token string) map[string]any {
		t.Helper()
		var b bytes.Buffer
		json.NewEncoder(&b).Encode(body)
		r := httptest.NewRequest(method, path, &b)
		r.RemoteAddr = "127.0.0.1:1000"
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		} else {
			if cookie != nil {
				r.AddCookie(cookie)
			}
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code < 200 || w.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		if cookies := w.Result().Cookies(); len(cookies) > 0 {
			cookie = cookies[0]
		}
		var result map[string]any
		json.Unmarshal(w.Body.Bytes(), &result)
		if value, ok := result["csrfToken"].(string); ok {
			csrf = value
		}
		return result
	}
	setup := call("POST", "/api/setup", map[string]any{"name": "Admin", "email": "admin@example.net", "password": "artifact-backup-password", "ixpName": "IXP", "asn": 64500, "setupToken": "private-test-setup"}, "")
	csrf = setup["csrfToken"].(string)
	member := call("POST", "/api/resources/members", map[string]any{"name": "Before", "asn": 64501}, "")
	loc := call("POST", "/api/resources/locations", map[string]any{"name": "DC"}, "")
	sw := call("POST", "/api/resources/switches", map[string]any{"name": "Switch", "locationId": loc["id"]}, "")
	token := call("POST", "/api/account/tokens", map[string]any{"name": "Collector", "currentPassword": "artifact-backup-password", "scopes": []string{"devices:write"}, "switchIds": []string{sw["id"].(string)}}, "")["token"].(string)
	sample := devicesnmp.Snapshot{Version: 1, SwitchID: sw["id"].(string), ObservedAt: time.Now().UTC(), Security: "authPriv", EngineIdentity: strings.Repeat("a", 64), EngineEpoch: "boot-one", Identity: devicesnmp.Identity{Name: "Managed"}, UptimeTicks: "1000", Interfaces: []devicesnmp.Interface{{IfIndex: 1, Name: "Ethernet1", Type: 6, MTU: 1500, SpeedMbps: 1000, AdminStatus: "up", OperStatus: "up"}}, Entities: []devicesnmp.Entity{}, Sensors: []devicesnmp.Sensor{}, FDB: []devicesnmp.FDBEntry{}, Warnings: []string{}}
	discovery := call("POST", "/api/devices/discoveries", sample, token)
	snapshot := external.Snapshot{Version: 1, Provider: "peeringdb", FetchedAt: time.Now().UTC(), Network: external.Network{ID: 101, ASN: 64501, Name: "Provider name", Website: "https://example.net", Status: "ok"}, NetIXLANs: []external.NetIXLAN{}, Exchanges: []external.Exchange{}, Facilities: []external.Facility{}}
	document, checksum, err := external.Canonical(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	proposal := call("POST", "/api/integrations/proposals/import", map[string]any{"memberId": member["id"], "document": string(document), "checksum": checksum}, "")
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "sources.gz")
	if err = Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restore")
	if err = Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	store, err := app.OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := store.Snapshot()
	if len(state.DeviceDiscoveries) != 1 || len(state.ExternalProposals) != 1 {
		t.Fatal("source metadata missing")
	}
	if state.DeviceDiscoveries[0].ID != discovery["id"] || state.ExternalProposals[0].ID != proposal["id"] {
		t.Fatal("source IDs changed")
	}
	for _, name := range []string{"devices/" + state.DeviceDiscoveries[0].SnapshotFile, "integrations/snapshots/" + state.ExternalProposals[0].ID + ".json"} {
		before, _ := os.ReadFile(filepath.Join(dir, name))
		after, err := os.ReadFile(filepath.Join(target, name))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("artifact changed: %s", name)
		}
	}
	// Resolving and pruning source bytes is supported retention, not corruption.
	a, err = app.New(app.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	call("POST", "/api/login", map[string]any{"email": "admin@example.net", "password": "artifact-backup-password"}, "")
	discoveryPath := "/api/devices/discoveries/" + discovery["id"].(string)
	call("POST", discoveryPath+"/dismiss", map[string]any{"reason": "Retain reviewed metadata only"}, "")
	call("POST", discoveryPath+"/prune", map[string]any{"reason": "Raw capture retention"}, "")
	proposalPath := "/api/integrations/proposals/" + proposal["id"].(string)
	call("POST", proposalPath+"/dismiss", map[string]any{"checksum": proposal["checksum"], "reason": "Keep local values"}, "")
	call("POST", proposalPath+"/prune", map[string]any{"checksum": proposal["checksum"], "acknowledge": true}, "")
	a.Close()
	prunedArchive := filepath.Join(t.TempDir(), "pruned.gz")
	if err = Create(dir, prunedArchive); err != nil {
		t.Fatal(err)
	}
	prunedTarget := filepath.Join(t.TempDir(), "restore-pruned")
	if err = Restore(prunedArchive, prunedTarget); err != nil {
		t.Fatal(err)
	}
	prunedStore, err := app.OpenStore(prunedTarget)
	if err != nil {
		t.Fatal(err)
	}
	defer prunedStore.Close()
	pruned := prunedStore.Snapshot()
	if !pruned.ExternalProposals[0].SnapshotPruned || pruned.DeviceDiscoveries[0].PrunedAt == nil {
		t.Fatal("retained review metadata lost")
	}

}
