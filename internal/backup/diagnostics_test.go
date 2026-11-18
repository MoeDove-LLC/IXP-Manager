package backup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/lookingglass"
)

func diagnosticWorkerRequest(t *testing.T, server *app.App, token, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}

func TestDiagnosticBackupPreservesSnapshotsAndAtlasCreditHistory(t *testing.T) {
	fixture := makeProvisionBackupFixture(t, false)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v2/probes/" {
			t.Error("unexpected Atlas fixture operation", r.Method, r.URL.Path)
			http.Error(w, "not allowed", 405)
			return
		}
		if r.Header.Get("Authorization") != "Key synthetic-atlas-backup-key" {
			t.Error("Atlas key was not confined to the auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"count": 1, "next": nil, "results": []any{map[string]any{"id": 123, "asn_v4": 64501, "address_v4": "192.0.2.10", "country_code": "CN", "is_anchor": false, "is_public": true, "status": map[string]any{"id": 1, "name": "Connected"}, "last_connected": time.Now().Unix()}}})
	}))
	defer provider.Close()
	configPath := filepath.Join(t.TempDir(), "atlas.json")
	config, _ := json.Marshal(map[string]any{"enabled": true, "baseURL": provider.URL, "apiKey": "synthetic-atlas-backup-key", "allowLoopbackHTTP": true})
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	server, err := app.New(app.Config{DataDir: fixture.dir, AtlasConfig: configPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	c := &provisionBackupClient{t: t, a: server}
	c.call("POST", "/api/login", app.Record{"email": "provision-backup@example.net", "password": provisionBackupPassword})
	peers := c.call("GET", "/api/resources/peers", nil)["items"].([]any)
	peer := peers[0].(map[string]any)
	c.call("POST", "/api/atlas/probes/discover", app.Record{"memberId": peer["memberId"], "af": 4, "anchorOnly": false})
	issued := c.call("POST", "/api/account/tokens", app.Record{"name": "backup diagnostic worker", "currentPassword": provisionBackupPassword, "scopes": []string{"looking-glass:run"}, "routerIds": []string{peer["routerId"].(string)}})
	worker := issued["token"].(string)
	item := c.call("POST", "/api/looking-glass/queries", app.Record{"peerId": peer["id"], "view": "accepted"})["item"].(map[string]any)
	claim := diagnosticWorkerRequest(t, server, worker, "/api/looking-glass/claim", lookingglass.ClaimRequest{RouterID: peer["routerId"].(string)})
	if claim.Code != 200 {
		t.Fatal(claim.Code, claim.Body.String())
	}
	var leased lookingglass.ClaimResponse
	if err := json.Unmarshal(claim.Body.Bytes(), &leased); err != nil || leased.Lease == nil {
		t.Fatal("missing fixture lease", err)
	}
	result := lookingglass.Result{Query: leased.Lease.Query, ObservedAt: time.Now().UTC(), Status: "ok", Complete: true, Identity: lookingglass.Identity{Address: leased.Lease.Query.NeighborAddress, ASN: leased.Lease.Query.NeighborASN, State: "Established"}, Routes: []lookingglass.Route{{Prefix: "198.51.100.0/24", NextHops: []string{"192.0.2.10"}, ASPath: []lookingglass.PathSegment{{Type: "sequence", ASNs: []uint32{64501}}}, Communities: [][2]uint32{}, LargeCommunities: [][3]uint32{}}}}
	done := diagnosticWorkerRequest(t, server, worker, "/api/looking-glass/complete", lookingglass.Completion{LeaseID: leased.Lease.ID, LeaseToken: leased.Lease.Token, Result: result})
	if done.Code != 200 {
		t.Fatal(done.Code, done.Body.String())
	}
	queryID := item["id"].(string)
	before := c.call("GET", "/api/looking-glass/queries/"+queryID+"/result", nil)
	server.Close()
	store, err := app.OpenStore(fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(s *app.State) error {
		s.Atlas.CreditBuckets = []app.AtlasCreditBucket{{Hour: time.Now().Unix() / 3600, Reserved: 1234}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	source := store.Snapshot()
	store.Close()
	archive := filepath.Join(t.TempDir(), "diagnostics.gz")
	if err := Create(fixture.dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	restored, err := app.OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := restored.Snapshot()
	restored.Close()
	if snapshot.Atlas.InstanceID != source.Atlas.InstanceID || len(snapshot.Atlas.CreditBuckets) != 1 || snapshot.Atlas.CreditBuckets[0].Reserved != 1234 || len(snapshot.Atlas.ProbeSnapshots) != 1 {
		t.Fatal("Atlas identity, cached probes or spent-reservation accounting lost")
	}
	for _, entry := range []struct {
		namespace string
		ids       []string
	}{{"atlas", app.AtlasArtifactIDs(source)}, {"looking-glass", app.LookingGlassArtifactIDs(source)}} {
		for _, id := range entry.ids {
			old, err := os.ReadFile(filepath.Join(fixture.dir, entry.namespace, id+".enc"))
			if err != nil {
				t.Fatal(err)
			}
			current, err := os.ReadFile(filepath.Join(target, entry.namespace, id+".enc"))
			if err != nil || !bytes.Equal(old, current) {
				t.Fatal("encrypted diagnostic file changed during restore")
			}
		}
	}
	server, err = app.New(app.Config{DataDir: target})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	c = &provisionBackupClient{t: t, a: server}
	c.call("POST", "/api/login", app.Record{"email": "provision-backup@example.net", "password": provisionBackupPassword})
	after := c.call("GET", "/api/looking-glass/queries/"+queryID+"/result", nil)
	one, _ := json.Marshal(before["result"])
	two, _ := json.Marshal(after["result"])
	if !bytes.Equal(one, two) {
		t.Fatal("restored typed route snapshot changed")
	}
	meta := c.call("GET", "/api/looking-glass/queries/"+queryID, nil)
	c.call("POST", "/api/looking-glass/queries/"+queryID+"/prune", app.Record{"expectedRevision": meta["revision"], "acknowledge": true})
	server.Close()
	prunedArchive := filepath.Join(t.TempDir(), "pruned.gz")
	if err := Create(target, prunedArchive); err != nil {
		t.Fatal(err)
	}
	prunedTarget := filepath.Join(t.TempDir(), "pruned-restore")
	if err := Restore(prunedArchive, prunedTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(prunedTarget, "looking-glass", queryID+".enc")); !os.IsNotExist(err) {
		t.Fatal("pruned route snapshot was restored")
	}
}
