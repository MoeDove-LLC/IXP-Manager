package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResourceImportPreviewLeavesStateUnchangedAndCommitRechecks(t *testing.T) {
	c, dir := setupClient(t)
	payload := Record{
		"version": 1,
		"resources": Record{
			"vlans": []Record{{"id": "legacy-vlan-1", "name": "Legacy fabric", "tag": 100, "ipv4Prefix": "192.0.2.0/24"}},
			"routers": []Record{{"id": "legacy-router-1", "name": "Legacy route server", "platform": "bird1", "asn": 64500,
				"routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": "legacy-vlan-1"}},
		},
		"warnings": []string{"legacy template requires separate review"},
	}
	before := c.a.store.Snapshot()
	statePath := filepath.Join(dir, "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	result := c.ok("POST", "/api/import/preview", payload)
	counts := result["resourceCounts"].(map[string]any)
	if result["checked"] != float64(2) || counts["routers"] != float64(1) || counts["vlans"] != float64(1) || result["warningCount"] != float64(1) {
		t.Fatal("preview did not check the complete batch", result)
	}
	current, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(raw, current) || !reflect.DeepEqual(before, c.a.store.Snapshot()) {
		t.Fatal("preview persisted data or an audit record", err)
	}
	if got := c.ok("POST", "/api/import", Record{"version": 1, "resources": Record{
		"vlans": []Record{{"id": "legacy-vlan-1", "name": "Concurrently registered VLAN", "tag": 100, "ipv4Prefix": "192.0.2.0/24"}},
	}}); got["imported"] != float64(1) {
		t.Fatal(got)
	}
	for _, endpoint := range []string{"/api/import/preview", "/api/import"} {
		if w := c.request("POST", endpoint, payload); w.Code != 400 || !strings.Contains(w.Body.String(), "duplicate id") {
			t.Fatalf("%s accepted the new conflict: %d %s", endpoint, w.Code, w.Body.String())
		}
	}
	if len(c.a.store.Snapshot().Resources["routers"]) != 0 {
		t.Fatal("conflicting import created a partial router record")
	}
	delete(payload["resources"].(Record), "vlans")
	if c.ok("POST", "/api/import/preview", payload)["checked"] != float64(1) || c.ok("POST", "/api/import", payload)["imported"] != float64(1) {
		t.Fatal("fixed import failed to commit")
	}
}

func TestResourceImportPreviewAndCommitRecheckRevokedBrowserSession(t *testing.T) {
	for _, endpoint := range []string{"/api/import/preview", "/api/import"} {
		t.Run(endpoint, func(t *testing.T) {
			c, _ := setupClient(t)
			payload := Record{"version": 1, "resources": Record{
				"members": []Record{{"id": "legacy-member-1", "name": "Review fixture", "asn": 64501, "status": "active"}},
			}}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			w := legacyNotesBodyRequest(c, "POST", endpoint, raw, func() { c.ok("POST", "/api/logout", nil) })
			if w.Code != 401 || len(c.a.store.Snapshot().Resources["members"]) != 0 {
				t.Fatalf("revoked session reached the import pipeline: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
