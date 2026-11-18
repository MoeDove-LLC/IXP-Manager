package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type patchFixtureIDs struct {
	member, other, panel, physical, secondPhysical string
	ports                                          []string
}

func setupPatchFixture(t *testing.T) (*client, patchFixtureIDs) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "测试网络有限公司", "asn": 64501, "status": "active"})
	other := c.create("members", Record{"name": "第二网络", "asn": 64502, "status": "active"})
	location := c.create("locations", Record{"name": "上海数据中心", "city": "上海", "address": "测试路1号"})
	rack := c.create("racks", Record{"name": "机柜A", "locationId": location["id"]})
	panel := c.create("patch-panels", Record{"name": "光纤配线架", "rackId": rack["id"], "portCount": 8})
	sw := c.create("switches", Record{"name": "sw-a", "locationId": location["id"]})
	physical := c.create("ports", Record{"name": "xe-1", "switchId": sw["id"], "speed": 10000})
	second := c.create("ports", Record{"name": "xe-2", "switchId": sw["id"], "speed": 10000})
	ids := patchFixtureIDs{member: str(m, "id"), other: str(other, "id"), panel: str(panel, "id"), physical: str(physical, "id"), secondPhysical: str(second, "id")}
	for i := 1; i <= 4; i++ {
		p := c.create("patch-ports", Record{"name": "光纤端口", "panelId": panel["id"], "number": i, "status": "available", "notes": "staff-only installation note"})
		ids.ports = append(ids.ports, str(p, "id"))
	}
	return c, ids
}
func currentPatch(c *client, id string) Record {
	c.t.Helper()
	items := c.ok("GET", "/api/patching/ports", nil)["items"].([]any)
	for _, raw := range items {
		p := Record(raw.(map[string]any))
		if str(p, "id") == id {
			return p
		}
	}
	c.t.Fatalf("visible patch port missing %s", id)
	return nil
}
func patchAction(c *client, id, action string, extra Record) Record {
	c.t.Helper()
	body := Record{"action": action, "revision": currentPatch(c, id)["revision"], "reason": "人工核对后的操作"}
	for key, value := range extra {
		body[key] = value
	}
	if peer := str(body, "duplexPeerId"); peer != "" {
		body["duplexPeerRevision"] = currentPatch(c, peer)["revision"]
	}
	return c.ok("POST", "/api/patching/ports/"+id+"/transition", body)
}
func issuePatchLOA(c *client, id string) Record {
	c.t.Helper()
	return c.ok("POST", "/api/patching/ports/"+id+"/loas", Record{"revision": currentPatch(c, id)["revision"], "reason": "允许本次双工光纤交叉连接", "expiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)})
}

func TestPatchLifecycleDuplexAndImmutableHistory(t *testing.T) {
	c, id := setupPatchFixture(t)
	p := id.ports[0]
	peer := id.ports[1]
	patchAction(c, p, "reserve", Record{"memberId": id.member, "duplexPeerId": peer, "switchPortId": id.physical, "circuitId": "CIR-001"})
	first := currentPatch(c, p)
	paired := currentPatch(c, peer)
	if first["allocationId"] != paired["allocationId"] || paired["duplexPeerId"] != p || first["memberId"] != id.member {
		t.Fatal("duplex reservation was not atomic")
	}
	snapshot := c.a.store.Snapshot()
	before := patchCopies(snapshot.PatchHistory[len(snapshot.PatchHistory)-1].After)
	for _, action := range []string{"request-connect", "connect", "request-cease", "cease"} {
		patchAction(c, p, action, nil)
	}
	patchAction(c, p, "release", Record{"keepPrewired": true})
	released := currentPatch(c, p)
	if released["status"] != "prewired" || str(released, "memberId") != "" || str(released, "allocationId") != "" || released["duplexPeerId"] != peer {
		t.Fatal("release did not preserve only physical prewire")
	}
	for _, action := range released["availableActions"].([]any) {
		if action == "unpair" {
			t.Fatal("prewired pair offered an impossible unpair action")
		}
	}
	if !bytes.Equal(mustJSON(before), mustJSON(c.a.store.Snapshot().PatchHistory[len(snapshot.PatchHistory)-1].After)) {
		t.Fatal("historical snapshot changed with live record")
	}
	patchAction(c, p, "reserve", Record{"memberId": id.other})
	reassigned := currentPatch(c, p)
	if reassigned["allocationId"] == first["allocationId"] || reassigned["memberId"] != id.other {
		t.Fatal("reassignment reused old tenant allocation")
	}
	generic := Record{"name": "renamed", "panelId": id.panel, "number": 1, "status": "available"}
	if w := c.request("PUT", "/api/resources/patch-ports/"+p, generic); w.Code != 400 {
		t.Fatal("generic CRUD bypassed workflow")
	}
	if w := c.request("DELETE", "/api/resources/patch-ports/"+p, nil); w.Code != 400 {
		t.Fatal("allocated port deleted")
	}
	if w := c.request("POST", "/api/patching/history", Record{}); w.Code != 404 {
		t.Fatal("history mutable endpoint exposed")
	}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func TestPatchDuplexFailureRollsBackAndRejectsStalePartner(t *testing.T) {
	c, id := setupPatchFixture(t)
	p, peer := id.ports[0], id.ports[1]
	patchAction(c, id.ports[2], "prewire", Record{"switchPortId": id.physical})
	before := c.a.store.Snapshot()
	body := Record{"action": "reserve", "revision": currentPatch(c, p)["revision"], "duplexPeerId": peer, "duplexPeerRevision": currentPatch(c, peer)["revision"], "reason": "错误绑定测试", "memberId": id.member, "switchPortId": id.physical}
	if w := c.request("POST", "/api/patching/ports/"+p+"/transition", body); w.Code != 409 {
		t.Fatal("switch-port collision accepted")
	}
	after := c.a.store.Snapshot()
	if len(before.PatchHistory) != len(after.PatchHistory) || str(find(&after, "patch-ports", p), "duplexPeerId") != "" || str(find(&after, "patch-ports", peer), "memberId") != "" {
		t.Fatal("failed duplex operation partially committed")
	}
	stalePeer := currentPatch(c, peer)["revision"]
	c.ok("PUT", "/api/resources/patch-ports/"+peer, Record{"name": "partner edited", "panelId": id.panel, "number": 2})
	body["switchPortId"] = id.secondPhysical
	body["duplexPeerRevision"] = stalePeer
	if w := c.request("POST", "/api/patching/ports/"+p+"/transition", body); w.Code != 409 || !strings.Contains(w.Body.String(), "partner changed") {
		t.Fatal("stale duplex partner overwritten")
	}
	if w := c.request("POST", "/api/resources/patch-ports", Record{"name": "bypass", "panelId": id.panel, "number": 5, "status": "connected", "memberId": id.member}); w.Code != 400 {
		t.Fatal("generic create allocated a patch port")
	}
}
func TestPatchLOAChineseBlobTenancyExpiryAndRevocation(t *testing.T) {
	c, id := setupPatchFixture(t)
	p := id.ports[0]
	patchAction(c, p, "reserve", Record{"memberId": id.member, "duplexPeerId": id.ports[1], "switchPortId": id.physical, "circuitId": "CIR-中文"})
	c.create("users", Record{"name": "Own member", "email": "patch-own@example.net", "role": "member", "memberId": id.member, "password": "own-member-long-password"})
	c.create("users", Record{"name": "Other member", "email": "patch-other@example.net", "role": "member", "memberId": id.other, "password": "other-member-long-password"})
	own := docLoginClient(t, c.a, "patch-own@example.net", "own-member-long-password")
	other := docLoginClient(t, c.a, "patch-other@example.net", "other-member-long-password")
	if w := own.request("GET", "/api/patching/history", nil); w.Code != 403 {
		t.Fatal("member can read historical tenant state")
	}
	if w := own.request("POST", "/api/patching/ports/"+p+"/loas", Record{}); w.Code != 403 {
		t.Fatal("member can issue authorization")
	}
	letter := issuePatchLOA(c, p)
	path := "/api/patching/loas/" + str(letter, "id") + "/download"
	download := own.request("GET", path, nil)
	if download.Code != 200 || !bytes.HasPrefix(download.Body.Bytes(), []byte("%PDF-")) || download.Header().Get("X-Content-SHA256") != str(letter, "checksum") {
		t.Fatal("own current PDF unavailable")
	}
	if w := other.request("GET", path, nil); w.Code != 404 {
		t.Fatal("other tenant read LOA")
	}
	html := own.request("GET", path+"?format=html", nil)
	if html.Code != 200 || !strings.Contains(html.Body.String(), "测试网络有限公司") || !strings.Contains(html.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("safe print artifact unavailable")
	}
	s := c.a.store.Snapshot()
	stored := loaByID(&s, str(letter, "id"))
	if stored == nil || loaStatus(&s, *stored, time.Now().Add(48*time.Hour)) != "expired" {
		t.Fatal("expiry not enforced")
	}
	stateFile, err := os.ReadFile(c.a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateFile, []byte("%PDF")) || bytes.Contains(stateFile, []byte("<!doctype")) {
		t.Fatal("binary/HTML artifact bloated hot state")
	}
	initialPDF := append([]byte(nil), download.Body.Bytes()...)
	c.ok("PUT", "/api/resources/patch-ports/"+p, Record{"name": "新的端口标签", "panelId": id.panel, "number": 1})
	if w := own.request("GET", path, nil); w.Code != 409 {
		t.Fatal("changed reservation facts left old LOA current")
	}
	raw, err := readLOABlob(c.a, stored.PDFBlobID, stored.PDFSize, stored.Checksum)
	if err != nil || !bytes.Equal(raw, initialPDF) {
		t.Fatal("immutable original artifact changed")
	}
	second := issuePatchLOA(c, p)
	c.ok("POST", "/api/patching/loas/"+str(second, "id")+"/revoke", Record{"reason": "授权取消"})
	if w := own.request("GET", "/api/patching/loas/"+str(second, "id")+"/download", nil); w.Code != 409 {
		t.Fatal("revoked letter downloadable")
	}
	patchAction(c, p, "release", nil)
	patchAction(c, p, "reserve", Record{"memberId": id.other})
	if w := own.request("GET", "/api/patching/ports", nil); strings.Contains(w.Body.String(), p) {
		t.Fatal("previous tenant still sees reassigned port")
	}
	if w := other.request("GET", path, nil); w.Code != 404 {
		t.Fatal("new tenant inherited old LOA")
	}
}
func TestPatchAttachmentOwnershipAndReleasedInventoryHistory(t *testing.T) {
	c, id := setupPatchFixture(t)
	p := id.ports[0]
	own := responseRecord(t, c.upload("/api/documents", "member-crossconnect.txt", "private original tenant", map[string]string{"memberId": id.member, "visibility": "member"}))
	staff := responseRecord(t, c.upload("/api/documents", "staff-wiring.txt", "internal-only", map[string]string{"visibility": "staff"}))
	patchAction(c, p, "reserve", Record{"memberId": id.member, "documentIds": []string{str(own, "id"), str(staff, "id")}})
	memberView := visiblePatchPorts(ptrState(c.a.store.Snapshot()), Record{"role": "member", "memberId": id.member})
	if len(memberView) != 1 || len(memberView[0]["attachments"].([]Record)) != 1 || str(memberView[0], "notes") != "" {
		t.Fatal("attachment or note scope widened")
	}
	if w := c.request("DELETE", "/api/documents/"+str(own, "id"), nil); w.Code < 400 {
		t.Fatal("linked attachment deleted without unlinking")
	}
	patchAction(c, p, "release", nil)
	patchAction(c, p, "reserve", Record{"memberId": id.other})
	view := currentPatch(c, p)
	if len(view["attachments"].([]any)) != 0 {
		t.Fatal("old tenant documents survived release")
	}
	body := Record{"action": "request-connect", "revision": view["revision"], "reason": "cross-tenant attachment attempt", "documentIds": []string{str(own, "id")}}
	if w := c.request("POST", "/api/patching/ports/"+p+"/transition", body); w.Code != 409 {
		t.Fatal("old tenant attachment relinked to new owner")
	}
	patchAction(c, p, "release", nil)
	history := len(c.a.store.Snapshot().PatchHistory)
	c.ok("DELETE", "/api/resources/patch-ports/"+p, nil)
	if len(c.a.store.Snapshot().PatchHistory) != history+1 {
		t.Fatal("deleting released inventory erased immutable history")
	}
}
func ptrState(s State) *State { return &s }
func TestLOABlobIntegrityFailureIsClosed(t *testing.T) {
	c, id := setupPatchFixture(t)
	patchAction(c, id.ports[0], "reserve", Record{"memberId": id.member})
	letter := issuePatchLOA(c, id.ports[0])
	s := c.a.store.Snapshot()
	stored := loaByID(&s, str(letter, "id"))
	if err := os.WriteFile(filepath.Join(c.a.blobDir(), stored.PDFBlobID), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := c.request("GET", "/api/patching/loas/"+stored.ID+"/download", nil); w.Code != 500 {
		t.Fatal("corrupt LOA served")
	}
}
