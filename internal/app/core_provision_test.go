package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/core"
	"github.com/moedove/ixp-manager/internal/provision"
)

func TestCoreProvisionReviewBindsSwitchAndRejectsStalePlan(t *testing.T) {
	f := newCoreAPIFixture(t)
	b := f.bundle()
	b.SideA.MTU, b.SideB.MTU = 9000, 9000
	b = f.preview(t, b, nil)
	f.c.ok("POST", "/api/core/bundles", coreMutation{Bundle: b, PortETags: f.tags(t)})
	profile := f.c.ok("POST", "/api/resources/provisioning-profiles", Record{"name": "Switch A core plan", "options": Record{"kind": "switch-core", "switchId": f.swA}})
	draft := f.c.ok("POST", "/api/provisioning", Record{"profileId": profile["id"]})
	if draft["validationLevel"] != "structural" || draft["complete"] != false {
		t.Fatal("switch JSON advertised native completeness")
	}
	path := approveProvision(f.c, draft)
	response := f.c.request("GET", path+"/config", nil)
	if response.Code != 200 || response.Header().Get("X-Provisioning-Kind") != "switch-core" || response.Header().Get("X-Provisioning-Switch-ID") != f.swA || response.Header().Get("X-Config-SHA256") != digest(response.Body.String()) {
		t.Fatal(response.Code, response.Body.String())
	}
	var plan core.SwitchPlan
	if json.Unmarshal(response.Body.Bytes(), &plan) != nil || core.ValidateSwitchPlan(plan) != nil || plan.SwitchID != f.swA || plan.BGP == nil || len(plan.BGP.Neighbors) != 2 || plan.NativeValidated || !plan.RequiresTargetAdapter {
		t.Fatal("download did not contain its typed dual-stack plan")
	}
	token := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "core plan pull", "currentPassword": "very-long-test-password", "scopes": []string{"provisioning:pull"}, "profileIds": []string{str(profile, "id")}}), "token")
	if w := bearerRequest(f.c.a, token, "GET", path+"/config", nil); w.Code != 200 {
		t.Fatal("bound plan download rejected", w.Code, w.Body.String())
	}
	other := f.c.ok("POST", "/api/resources/provisioning-profiles", Record{"name": "Switch B core plan", "options": Record{"kind": "switch-core", "switchId": f.swB}})
	otherDraft := f.c.ok("POST", "/api/provisioning", Record{"profileId": other["id"]})
	otherPath := approveProvision(f.c, otherDraft)
	if w := bearerRequest(f.c.a, token, "GET", otherPath+"/config", nil); w.Code != 403 {
		t.Fatal("plan pull token escaped its selected profile")
	}
	state := f.c.a.store.Snapshot()
	edit := editableCredentialPeer(find(&state, "switches", f.swB))
	edit["active"] = false
	f.c.ok("PUT", "/api/resources/switches/"+f.swB, edit)
	if w := f.c.request("GET", path+"/config", nil); w.Code != 409 {
		t.Fatal("changed remote switch still served an approved old plan", w.Code)
	}
	next := f.c.ok("POST", "/api/provisioning", Record{"profileId": profile["id"]})
	nextPath := approveProvision(f.c, next)
	out := f.c.request("GET", nextPath+"/config", nil)
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &plan) != nil {
		t.Fatal("updated plan unavailable")
	}
	for _, neighbor := range plan.BGP.Neighbors {
		if !neighbor.Shutdown {
			t.Fatal("inactive remote switch generated an enabled adjacency")
		}
	}
	if len(plan.Floodlist) != 0 {
		t.Fatal("inactive switch remained in flood list")
	}
	if w := f.c.request("DELETE", "/api/resources/switches/"+f.swA, nil); w.Code < 400 {
		t.Fatal("switch referenced by bundle/profile deleted")
	}
}

func TestCoreMonitoringProjectsOnlyExplicitBundles(t *testing.T) {
	f := newCoreAPIFixture(t)
	created := f.create(t)
	state := f.c.a.store.Snapshot()
	sw := editableCredentialPeer(find(&state, "switches", f.swA))
	sw["managementIp"] = "198.51.100.10"
	f.c.ok("PUT", "/api/resources/switches/"+f.swA, sw)
	state = f.c.a.store.Snapshot()
	in, err := provisionInput(&state, provision.Options{Kind: "nagios"})
	if err != nil || len(in.CoreBundles) != 1 || in.CoreBundles[0].ID != str(created, "id") {
		t.Fatal("real core bundle absent from monitoring", err)
	}
	artifact, err := provision.Render(in)
	if err != nil || !strings.Contains(artifact.Content, "check_moedove_core!"+str(created, "id")) {
		t.Fatal("core probe command missing", err)
	}
	if err := provision.ValidateArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	current := f.c.ok("GET", "/api/core/bundles/"+str(created, "id"), nil)
	b := coreBundleRecord(t, current)
	b.Enabled = false
	f.c.ok("PUT", "/api/core/bundles/"+b.ID, coreMutation{Bundle: b, ExpectedRevision: str(current, "revision"), PortETags: f.tags(t)})
	state = f.c.a.store.Snapshot()
	in, err = provisionInput(&state, provision.Options{Kind: "nagios"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err = provision.Render(in)
	if err != nil || strings.Contains(artifact.Content, "check_moedove_core!"+b.ID) {
		t.Fatal("disabled bundle still emitted active monitoring", err)
	}
}
