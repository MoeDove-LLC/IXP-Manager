package app

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
)

func handoffRequest(c *client, id, review, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/deployments/"+id+"/routeros-handoff", nil)
	r.Header.Set(routerOSReviewHeader, review)
	if bearer == "" {
		r.AddCookie(c.cookie)
	} else {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	return w
}

func TestRouterOSHandoffUsesExactDraftWithoutCredentialsOrStateChange(t *testing.T) {
	c, dir, peer := credentialFixture(t)
	routerID := str(peer, "routerId")
	if err := c.a.store.Update(func(s *State) error {
		find(s, "routers", routerID)["platform"] = "routeros7"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	credentialPath := "/api/peer-credentials/" + str(peer, "id")
	change := credentialChange(c.ok("GET", credentialPath, nil))
	const secret = "Handoff_Fixture_Password_345"
	change["secret"] = secret
	credential := c.ok("PUT", credentialPath, change)
	dto := c.ok("POST", "/api/deployments", Record{"routerId": routerID})
	s := c.a.store.Snapshot()
	d := *deploymentByID(&s, str(dto, "id"))
	review := c.a.deploymentReviewToken(d)
	if ready := readinessResponse(t, c, d); !ready.Actions["routerosHandoff"].Allowed || ready.Actions["download"].Allowed || ready.Actions["validate"].Allowed {
		t.Fatal("RouterOS draft planning and publication permissions disagree")
	}
	before, _ := json.Marshal(c.a.store.Snapshot())
	for _, invalid := range []string{"", d.Checksum} {
		if w := handoffRequest(c, d.ID, invalid, ""); w.Code != 409 {
			t.Fatalf("invalid review identifier status = %d", w.Code)
		}
	}
	w := handoffRequest(c, d.ID, review, "")
	if w.Code != 200 || w.Header().Get(routerOSReviewHeader) != review || w.Header().Get("Cache-Control") != "no-store" ||
		w.Header().Get("Content-Disposition") != `attachment; filename="routeros-handoff-`+d.ID+`.json"` {
		t.Fatalf("unexpected handoff response status %d", w.Code)
	}
	for _, forbidden := range []string{secret, d.Checksum, d.ArtifactChecksum, "password", "tcp-md5-key", "exclusiveAccessAcknowledged", "deviceId"} {
		if forbidden != "" && strings.Contains(w.Body.String(), forbidden) {
			t.Fatal("planning handoff contains protected data or target authority")
		}
	}
	var handoff routeros.PlanningHandoff
	if json.Unmarshal(w.Body.Bytes(), &handoff) != nil || handoff.Validate() != nil || handoff.RouterID != routerID ||
		handoff.DeploymentID != d.ID || handoff.ReviewIdentifier != review || handoff.Desired.RouterID != "192.0.2.1" ||
		handoff.Desired.ASN != 64500 || len(handoff.Desired.Peers) != 1 {
		t.Fatal("handoff lost its router, revision or policy identity")
	}
	p := handoff.Desired.Peers[0]
	if p.ID != str(peer, "id") || p.Address != "192.0.2.10" || p.ASN != 64501 || p.MaxPrefixes != 10 ||
		!p.Authentication.Enabled || p.Authentication.Revision != str(credential, "revision") || len(p.AllowedPrefixes) != 1 || p.AllowedPrefixes[0] != "198.51.100.0/24" {
		t.Fatal("handoff lost the enabled peer, credential revision or policy limits")
	}
	after, _ := json.Marshal(c.a.store.Snapshot())
	if !bytes.Equal(before, after) {
		t.Fatal("handoff changed the deployment or store")
	}
	artifact := filepath.Join(dir, "deployments", d.ID+".enc")
	ciphertext, err := os.ReadFile(artifact)
	if err != nil || os.WriteFile(artifact, []byte("corrupt fixture"), 0600) != nil {
		t.Fatal("tampered artifact fixture setup failed", err)
	}
	if w = handoffRequest(c, d.ID, review, ""); w.Code != 500 {
		t.Fatalf("unverified ciphertext produced handoff status %d", w.Code)
	}
	if err = os.WriteFile(artifact, ciphertext, 0600); err != nil {
		t.Fatal(err)
	}
	change = credentialChange(c.ok("GET", credentialPath, nil))
	change["secret"] = "New_Handoff_Fixture_Password_345"
	c.ok("PUT", credentialPath, change)
	if w = handoffRequest(c, d.ID, review, ""); w.Code != 409 || readinessResponse(t, c, d).Actions["routerosHandoff"].Allowed {
		t.Fatal("stale authentication revision remained downloadable")
	}
}

func TestRouterOSHandoffBrowserRolesAndRetention(t *testing.T) {
	c, _, d := readinessFixture(t, "routeros7")
	review := c.a.deploymentReviewToken(d)
	for role, wanted := range map[string]int{"operator": 200, "viewer": 403} {
		c.create("users", Record{"name": role, "email": role + "@example.net", "role": role, "password": "Handoff-fixture-password"})
		actor := docLoginClient(t, c.a, role+"@example.net", "Handoff-fixture-password")
		if w := handoffRequest(actor, d.ID, review, ""); w.Code != wanted || readinessResponse(t, actor, d).Actions["routerosHandoff"].Allowed != (wanted == 200) {
			t.Fatalf("%s handoff status %d, want %d", role, w.Code, wanted)
		}
	}
	const raw = "mdt.handoff.fixture"
	if err := c.a.store.Update(func(s *State) error {
		id := accountIDUnsafe(s)
		security := s.Security[id]
		security.Tokens = append(security.Tokens, APIToken{ID: "handoff", Hash: digest(raw), Scopes: []string{"deployments:read", "deployments:pull"}, RouterIDs: []string{d.RouterID}, ExpiresAt: time.Now().Add(time.Hour)})
		s.Security[id] = security
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := handoffRequest(c, d.ID, review, raw); w.Code != 403 {
		t.Fatalf("bearer token handoff status %d", w.Code)
	}
	c.ok("POST", "/api/deployments/"+d.ID+"/prune", Record{"checksum": review, "acknowledge": true, "reason": "fixture retention"})
	if w := handoffRequest(c, d.ID, review, ""); w.Code != 410 || readinessResponse(t, c, d).Actions["routerosHandoff"].Allowed {
		t.Fatal("pruned draft remained downloadable")
	}
}
