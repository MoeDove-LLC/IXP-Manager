package app

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func trafficReport(c *client, scope, id, metric string) Record {
	q := url.Values{"scope": {scope}, "id": {id}, "metric": {metric}, "resolution": {"5m"}, "from": {time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)}, "to": {time.Now().UTC().Format(time.RFC3339)}}
	return c.ok("GET", "/api/traffic/query?"+q.Encode(), nil)
}
func directionTotal(r Record, side string) any {
	return r["summary"].(map[string]any)[side].(map[string]any)["total"]
}
func TestTrafficHTTPExactHistorySurvivesReassignmentWithoutRelabeling(t *testing.T) {
	f := monitoringFixture(t)
	owner := memberDiagnosticClient(t, f.c, str(f.member, "id"), "traffic-owner@example.net")
	other := memberDiagnosticClient(t, f.c, str(f.otherMember, "id"), "traffic-other@example.net")
	start := time.Now().UTC().Add(-20 * time.Second)
	for i, value := range []string{"9007199254740993", "9007199254741243"} {
		batch := f.batch(start.Add(time.Duration(i)*2*time.Second), value)
		ports := batch["ports"].([]Record)
		ports[0]["dimensions"] = Record{"memberRevision": strings.Repeat("f", 64), "switchId": "forged-switch"}
		ports[0]["unicast"] = Record{"in": []string{"100", "350"}[i], "out": "0", "bits": 64}
		ports[0]["errors"] = Record{"in": []string{"10", "25"}[i], "out": []string{"20", "40"}[i], "bits": 32}
		if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", batch); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if r := trafficReport(owner, "member", str(f.member, "id"), "octets"); directionTotal(r, "in") != "250" || directionTotal(r, "out") != "0" {
		t.Fatal("precise bigint delta or observed zero lost", r)
	}
	if r := trafficReport(owner, "member", str(f.member, "id"), "unicast"); directionTotal(r, "in") != "250" {
		t.Fatal("packet units lost")
	}
	r := trafficReport(owner, "member", str(f.member, "id"), "errors")
	in := r["summary"].(map[string]any)["in"].(map[string]any)
	if in["total"] != nil || in["averageRate"] != nil || in["lowerBoundTotal"] != "15" {
		t.Fatal("Counter32 uncertainty was lost", in)
	}
	f.c.ok("DELETE", "/api/resources/connections/"+str(f.connection, "id"), nil)
	f.c.create("connections", Record{"name": "New tenant access", "memberId": f.otherMember["id"], "portId": f.port["id"], "vlanId": f.connection["vlanId"], "ipv4": "192.0.2.10", "status": "active"})
	if r := trafficReport(owner, "member", str(f.member, "id"), "octets"); directionTotal(r, "in") != "250" {
		t.Fatal("former owner's history erased")
	}
	if r := trafficReport(other, "member", str(f.otherMember, "id"), "octets"); directionTotal(r, "in") != nil {
		t.Fatal("new owner inherited old traffic")
	}
	if w := owner.request("GET", "/api/traffic/query?scope=port&id="+str(f.port, "id"), nil); w.Code != 404 {
		t.Fatal("former owner read current port")
	}
	if w := owner.request("GET", "/api/traffic/query?scope=member&id="+str(f.otherMember, "id"), nil); w.Code != 404 {
		t.Fatal("foreign member report disclosed")
	}
	dir := filepath.Dir(f.c.a.store.path)
	f.c.a.Close()
	restored, err := New(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	owner = docLoginClient(t, restored, "traffic-owner@example.net", "member-diagnostic-password")
	if r := trafficReport(owner, "member", str(f.member, "id"), "octets"); directionTotal(r, "in") != "250" {
		t.Fatal("history changed across restart")
	}
}

func TestTrafficSourceRequiresReviewedChoiceAndPausesDetachedSource(t *testing.T) {
	f := monitoringFixture(t)
	p := f.c.ok("GET", "/api/resources/ports/"+str(f.port, "id"), nil)
	edit := editableCredentialPeer(p)
	edit["counterSource"] = "auto"
	f.c.ok("PUT", "/api/resources/ports/"+str(p, "id"), edit)
	get := func() Record {
		for _, raw := range f.c.ok("GET", "/api/traffic/catalog", nil)["targets"].([]any) {
			row := Record(raw.(map[string]any))
			if row["scope"] == "port" && row["id"] == p["id"] {
				return Record(row["sourceControl"].(map[string]any))
			}
		}
		t.Fatal("missing source selector")
		return nil
	}
	before := get()
	if before["status"] != "ambiguous" {
		t.Fatal("two producers automatically merged")
	}
	selection := "router:" + str(f.router, "id")
	f.c.ok("POST", "/api/traffic/source", Record{"portId": p["id"], "selection": selection, "expectedRevision": before["revision"]})
	if w := f.c.request("POST", "/api/traffic/source", Record{"portId": p["id"], "selection": "none", "expectedRevision": before["revision"]}); w.Code != 409 {
		t.Fatal("stale source review accepted")
	}
	f.c.ok("DELETE", "/api/resources/connections/"+str(f.connection, "id"), nil)
	if after := get(); after["configured"] != selection || after["status"] != "unavailable" {
		t.Fatal("detaching access rewrote or activated a source", after)
	}
}

func TestTrafficObservationKeyAndTokenSnapshotsAreBound(t *testing.T) {
	f := monitoringFixture(t)
	user, session, ok := f.c.a.authenticateToken(f.token)
	if !ok {
		t.Fatal("fixture token unavailable")
	}
	user["role"] = "member"
	session.Scopes[0] = "resources:write"
	user, session, ok = f.c.a.authenticateToken(f.token)
	if !ok || str(user, "role") != "admin" || contains(session.Scopes, "resources:write") {
		t.Fatal("token snapshot aliases live state")
	}
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", f.batch(time.Now().UTC(), "1000")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	state := f.c.a.store.Snapshot()
	if state.ObservationKeyHash == "" {
		t.Fatal("history key was not anchored")
	}
	dir := filepath.Dir(f.c.a.store.path)
	f.c.a.Close()
	path := filepath.Join(dir, "security.key")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(dir); err == nil {
		s.Close()
		t.Fatal("different key silently relabeled member history")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(dir); err == nil {
		s.Close()
		t.Fatal("missing key silently regenerated for history")
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(state)
	if !strings.Contains(string(encoded), "observationKeyHash") {
		t.Fatal("key marker missing from full state")
	}
}
