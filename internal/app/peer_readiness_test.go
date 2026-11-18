package app

import (
	"reflect"
	"strings"
	"testing"
)

func TestPeerReadinessChecksDraftWithoutEnablingOrExposingCredentials(t *testing.T) {
	c, _, peer := credentialFixture(t)
	id := str(peer, "id")
	draft := editableCredentialPeer(peer)
	draft["enabled"] = false
	draft["allowedPrefixes"] = []string{}
	c.ok("PUT", "/api/resources/peers/"+id, draft)
	if err := c.a.store.Update(func(s *State) error {
		s.Resources["connections"][0]["status"] = "pending"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	endpoint := "/api/peer-readiness/" + id
	before := c.a.store.Snapshot()
	blocked := c.ok("GET", endpoint, nil)
	if blocked["enabled"] != false || blocked["canGenerateConfiguration"] != false || !reflect.DeepEqual(blocked["issues"], []any{"active-attachment-missing", "allowlist-required"}) {
		t.Fatal("draft diagnostics did not match current data", blocked)
	}
	if !reflect.DeepEqual(before, c.a.store.Snapshot()) {
		t.Fatal("read-only readiness changed the draft or audit history")
	}
	if err := c.a.store.Update(func(s *State) error {
		s.Resources["connections"][0]["status"] = "active"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	draft["allowedPrefixes"] = []string{"198.51.100.0/24"}
	c.ok("PUT", "/api/resources/peers/"+id, draft)
	credentialURL := "/api/peer-credentials/" + id
	setup := credentialChange(c.ok("GET", credentialURL, nil))
	setup["secret"] = "ReadinessOnly_TestCredential_123"
	c.ok("PUT", credentialURL, setup)
	ready := c.ok("GET", endpoint, nil)
	if ready["enabled"] != false || ready["canGenerateConfiguration"] != true || len(ready["issues"].([]any)) != 0 {
		t.Fatal("draft was not recognized as renderable once complete", ready)
	}
	w := c.request("GET", endpoint, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), setup["secret"].(string)) || strings.Contains(w.Body.String(), "ciphertext") || strings.Contains(w.Body.String(), "private-key") {
		t.Fatal("readiness exposed credential data or failed", w.Code)
	}
	if updated := c.ok("GET", "/api/resources/peers/"+id, nil); updated["enabled"] != false {
		t.Fatal("ready status enabled the saved draft")
	}
	if w := c.request("POST", endpoint, nil); w.Code != 405 {
		t.Fatal("readiness accepted a write", w.Code)
	}
	state := c.a.store.Snapshot()
	viewer := noteTestUser(t, c, "viewer", "peer-preview-viewer@example.test", nil)
	if w := viewer.request("GET", endpoint, nil); w.Code != 200 {
		t.Fatal("staff read-only role could not inspect configuration conditions", w.Code)
	}
	member := noteTestUser(t, c, "member", "peer-preview-member@example.test", state.Resources["members"][0])
	if w := member.request("GET", endpoint, nil); w.Code != 403 {
		t.Fatal("member reached staff-only configuration checks", w.Code)
	}
}
