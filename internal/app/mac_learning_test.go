package app

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
)

func TestMACLearningAlreadyAuthenticatedRequestCannotApplyAfterLogout(t *testing.T) {
	f := newMACLearningFixture(t)
	view := f.view(t)
	review := f.review(t, view, "02:00:00:00:00:01")
	body, _ := json.Marshal(f.applyBody(view, review))
	request := httptest.NewRequest("POST", "/api/devices/mac-learning/apply", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(f.c.cookie)
	user, session, ok := f.c.a.authenticate(request)
	if !ok {
		t.Fatal("fixture request was not authenticated")
	}
	// This is the context a request already admitted by ServeHTTP retains while
	// queued behind the device mutex. Logout invalidates that exact session.
	f.c.ok("POST", "/api/logout", nil)
	w := httptest.NewRecorder()
	if !f.c.a.macLearning(w, request, user, session) || w.Code != 403 {
		t.Fatal("revoked queued session applied learning review", w.Code, w.Body.String())
	}
	if len(f.c.a.store.Snapshot().Resources["mac-addresses"]) != 0 {
		t.Fatal("logout still allowed a durable MAC registration")
	}
}

type macLearningFixture struct {
	deviceTestFixture
	vlanID, connectionID, otherConnectionID string
	snapshot                                devicesnmp.Snapshot
}

func newMACLearningFixture(t *testing.T) macLearningFixture {
	t.Helper()
	f := deviceFixture(t)
	vlan := f.c.create("vlans", Record{"name": "MAC review LAN", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	connection := f.c.create("connections", Record{"name": "First physical access", "memberId": f.member, "portId": f.port, "vlanId": vlan["id"], "status": "active"})
	other := f.c.create("connections", Record{"name": "Second physical access", "memberId": f.otherMember, "portId": f.core, "vlanId": vlan["id"], "status": "active"})
	f.approve(t, f.propose(t, f.snapshot(time.Now().UTC(), "1000")))
	snapshot := f.stamp(t, f.snapshot(time.Now().UTC().Add(time.Second), "1100"))
	snapshot.FDB = []devicesnmp.FDBEntry{{FDBID: 1, VLANIDs: []uint16{100}, MAC: "02:00:00:00:00:01", BridgePort: 11, IfIndex: 1, Status: "learned"}, {FDBID: 1, VLANIDs: []uint16{100}, MAC: "02:00:00:00:00:02", BridgePort: 12, IfIndex: 2, Status: "learned"}}
	responseRecord(t, bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", snapshot))
	return macLearningFixture{f, str(vlan, "id"), str(connection, "id"), str(other, "id"), snapshot}
}
func (f macLearningFixture) view(t *testing.T) Record {
	t.Helper()
	return f.c.ok("GET", "/api/devices/mac-learning?switchId="+f.switchID, nil)
}
func macLearningItem(t *testing.T, view Record, address string) Record {
	t.Helper()
	for _, value := range view["items"].([]any) {
		row := Record(value.(map[string]any))
		if str(row, "mac") == address {
			return row
		}
	}
	t.Fatal("MAC candidate absent", address, view)
	return nil
}
func (f macLearningFixture) review(t *testing.T, view Record, address string) Record {
	t.Helper()
	row := macLearningItem(t, view, address)
	return f.c.ok("POST", "/api/devices/mac-learning/review", Record{"switchId": f.switchID, "observationChecksum": view["observation"].(map[string]any)["checksum"], "candidateId": row["id"]})
}
func (f macLearningFixture) applyBody(view, review Record) Record {
	return Record{"switchId": f.switchID, "observationChecksum": view["observation"].(map[string]any)["checksum"], "candidateId": review["candidate"].(map[string]any)["id"], "reviewChecksum": review["reviewChecksum"], "expiresAt": review["expiresAt"], "name": "Reviewed learned MAC", "acknowledge": true}
}
func TestMACLearningReviewRegistersOnlyExactCurrentEvidence(t *testing.T) {
	f := newMACLearningFixture(t)
	view := f.view(t)
	row := macLearningItem(t, view, "02:00:00:00:00:01")
	if row["canReview"] != true || row["connectionId"] != f.connectionID || row["registrationStatus"] != "candidate" {
		t.Fatal("missing unique bound candidate", row)
	}
	review := f.review(t, view, str(row, "mac"))
	if len(f.c.a.store.Snapshot().Resources["mac-addresses"]) != 0 {
		t.Fatal("preview registered learned evidence")
	}
	body := f.applyBody(view, review)
	bad := Record{}
	for k, v := range body {
		bad[k] = v
	}
	bad["candidateId"] = macLearningItem(t, view, "02:00:00:00:00:02")["id"]
	if w := f.c.request("POST", "/api/devices/mac-learning/apply", bad); w.Code != 409 {
		t.Fatal("review used for another candidate", w.Code)
	}
	result := f.c.ok("POST", "/api/devices/mac-learning/apply", body)
	registered := Record(result["registered"].(map[string]any))
	if str(registered, "connectionId") != f.connectionID || str(registered, "address") != "02:00:00:00:00:01" || result["receiverReviewRequired"] != true {
		t.Fatal("registration altered approved target", result)
	}
	if w := f.c.request("POST", "/api/resources/mac-addresses", Record{"name": "Duplicate manual owner", "connectionId": f.otherConnectionID, "address": "02:00:00:00:00:01"}); w.Code != 400 {
		t.Fatal("generic resource path bypassed VLAN uniqueness", w.Code)
	}
	for _, address := range []string{"ff:ff:ff:ff:ff:ff", "01:00:5e:00:00:01", "00:00:00:00:00:00"} {
		if w := f.c.request("POST", "/api/resources/mac-addresses", Record{"name": "Invalid manual registration", "connectionId": f.connectionID, "address": address}); w.Code != 400 {
			t.Fatal("group or zero MAC registered as member identity", address, w.Code)
		}
	}
	if w := f.c.request("POST", "/api/devices/mac-learning/apply", body); w.Code != 409 {
		t.Fatal("same review registered twice")
	}
	after := f.view(t)
	if macLearningItem(t, after, str(row, "mac"))["registrationStatus"] != "registered" || len(after["registered"].([]any)) != 1 || after["registered"].([]any)[0].(map[string]any)["observed"] != true {
		t.Fatal("registered/observed comparison missing", after)
	}
	state := f.c.a.store.Snapshot()
	last := state.Audit[len(state.Audit)-1]
	if last.Action != "register-learned-mac" || last.Details["observationChecksum"] != body["observationChecksum"] {
		t.Fatal("registration lacks frozen audit evidence")
	}
	page := f.c.ok("GET", "/api/devices/mac-learning?switchId="+f.switchID+"&limit=1&candidateId="+str(macLearningItem(t, after, "02:00:00:00:00:02"), "id"), nil)
	if page["offset"] != float64(1) || len(page["items"].([]any)) != 1 {
		t.Fatal("deep-linked candidate pagination failed", page)
	}
}
func TestMACLearningRejectsChangedObservationOwnershipAndRegistry(t *testing.T) {
	f := newMACLearningFixture(t)
	view := f.view(t)
	review := f.review(t, view, "02:00:00:00:00:01")
	body := f.applyBody(view, review)
	next := f.snapshot
	next.ObservedAt = next.ObservedAt.Add(time.Second)
	next.UptimeTicks = "100100"
	responseRecord(t, bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", next))
	if w := f.c.request("POST", "/api/devices/mac-learning/apply", body); w.Code != 409 {
		t.Fatal("superseded FDB snapshot applied")
	}
	view = f.view(t)
	review = f.review(t, view, "02:00:00:00:00:01")
	body = f.applyBody(view, review)
	f.c.create("mac-addresses", Record{"name": "Conflicting existing registration", "connectionId": f.otherConnectionID, "address": "02:00:00:00:00:01"})
	if w := f.c.request("POST", "/api/devices/mac-learning/apply", body); w.Code != 409 {
		t.Fatal("review overwrote another connection registration")
	}
	conflict := macLearningItem(t, f.view(t), "02:00:00:00:00:01")
	if conflict["registrationStatus"] != "conflict" || conflict["canReview"] != false {
		t.Fatal("MAC collision not reported", conflict)
	}
	// Interface identity edits do not relabel the earlier device observation.
	if err := f.c.a.store.Update(func(s *State) error { find(s, "ports", f.port)["snmpIfName"] = "replaced-interface"; return nil }); err != nil {
		t.Fatal(err)
	}
	changed := macLearningItem(t, f.view(t), "02:00:00:00:00:01")
	if changed["canReview"] != false || changed["registrationStatus"] != "stale" {
		t.Fatal("changed physical identity remained adoptable", changed)
	}
}
func TestMACLearningRejectsAmbiguityOldEvidenceAndCrossScopeReads(t *testing.T) {
	f := newMACLearningFixture(t)
	view := f.view(t)
	member := memberDiagnosticClient(t, f.c, f.member, "mac-member@example.net")
	if w := member.request("GET", "/api/devices/mac-learning?switchId="+f.switchID, nil); w.Code != 403 {
		t.Fatal("member read raw learning evidence")
	}
	if w := bearerRequest(f.c.a, f.token, "GET", "/api/devices/mac-learning?switchId="+f.otherSwitch, nil); w.Code != 403 {
		t.Fatal("switch token crossed scope")
	}
	read := responseRecord(t, bearerRequest(f.c.a, f.token, "GET", "/api/devices/mac-learning?switchId="+f.switchID, nil))
	if read["capabilities"].(map[string]any)["canApply"] != false {
		t.Fatal("collector can self-approve FDB data")
	}
	for _, item := range read["items"].([]any) {
		if item.(map[string]any)["canReview"] != false {
			t.Fatal("token advertised review mutation")
		}
	}
	encoded, _ := json.Marshal(read)
	if strings.Contains(string(encoded), "private-noc") || strings.Contains(string(encoded), "previous-tenant-alias") {
		t.Fatal("learning view disclosed unrelated SNMP identity")
	}
	next := f.snapshot
	next.ObservedAt = next.ObservedAt.Add(time.Second)
	next.UptimeTicks = "100100"
	next.FDB = append(next.FDB, devicesnmp.FDBEntry{FDBID: 2, VLANIDs: []uint16{100}, MAC: "02:00:00:00:00:01", BridgePort: 12, IfIndex: 2, Status: "learned"}, devicesnmp.FDBEntry{FDBID: 3, VLANIDs: []uint16{100, 200}, MAC: "02:00:00:00:00:03", BridgePort: 11, IfIndex: 1, Status: "learned"})
	responseRecord(t, bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", next))
	after := f.view(t)
	if macLearningItem(t, after, "02:00:00:00:00:01")["registrationStatus"] != "conflict" || macLearningItem(t, after, "02:00:00:00:00:03")["registrationStatus"] != "ambiguous" {
		t.Fatal("ambiguous evidence promoted to authority", after)
	}
	// Age is evaluated at review time, independent of a page's earlier state.
	o, err := f.c.a.latestDevice(f.switchID)
	if err != nil {
		t.Fatal(err)
	}
	state := f.c.a.store.Snapshot()
	_, rows := macLearningProjection(&state, o, true, next.ObservedAt.Add(3*time.Minute))
	for _, row := range rows {
		if row.CanReview {
			t.Fatal("expired observation still adoptable")
		}
	}
	if len(view["items"].([]any)) != 2 {
		t.Fatal("initial fixture drift")
	}
}
