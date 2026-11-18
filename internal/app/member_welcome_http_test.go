package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

func TestMemberWelcomeReviewedWorkflowAndStaleDelivery(t *testing.T) {
	admin, _, peer := credentialFixture(t)
	sender := enableTestMail(admin.a)
	const noc, support, login = "welcome-noc@example.test", "welcome-support@example.test", "welcome-member@example.test"
	const operatorNote = "Reviewed operational handoff marker"
	const adminPassword = "very-long-test-password"
	id := str(peer, "memberId")
	admin.ok(http.MethodPut, "/api/resources/members/"+id, Record{"name": "Welcome member", "asn": 64501, "status": "active", "nocEmail": noc})
	admin.ok(http.MethodPut, "/api/settings", Record{"name": "Fixture IXP", "asn": 64500, "supportEmail": support})
	admin.create("users", Record{"name": "Member administrator", "email": login, "password": "welcome-member-fixture-password", "role": "member", "memberId": id, "memberAdminIds": []string{id}})
	other := admin.create("members", Record{"name": "Separate member", "asn": 64502, "status": "active", "nocEmail": "unrelated-noc@example.test"})
	path := "/api/members/" + id + "/welcome-email"
	initialDraft := func() Record {
		t.Helper()
		return Record(admin.ok(http.MethodGet, path, nil)["draft"].(map[string]any))
	}
	queuePayload := func(preview Record) Record {
		t.Helper()
		draft := Record(preview["draft"].(map[string]any))
		draft["reviewToken"], draft["expiresAt"], draft["acknowledge"] = preview["reviewToken"], preview["expiresAt"], true
		return draft
	}
	draft := initialDraft()
	if draft["to"].([]any)[0] != noc || draft["cc"].([]any)[0] != login || draft["bcc"].([]any)[0] != support || !strings.Contains(str(draft, "text"), "192.0.2.10") {
		t.Fatal("initial welcome draft did not preserve scoped operational facts and defaults")
	}
	if strings.Contains(fmtJSON(draft), "unrelated-noc") || strings.Contains(fmtJSON(draft), str(other, "name")) {
		t.Fatal("welcome draft exposed another member")
	}
	to := draft["to"]
	draft["to"] = []string{"unrelated-noc@example.test"}
	if w := admin.request(http.MethodPost, path+"/review", draft); w.Code != http.StatusConflict {
		t.Fatal("unrelated member could be selected as recipient", w.Code)
	}
	draft["to"] = to
	draft["subject"] = "Welcome with reviewed changes"
	draft["text"] = str(draft, "text") + "\r\n" + operatorNote
	preview := admin.ok(http.MethodPost, path+"/review", draft)
	queue := queuePayload(preview)
	if strings.Contains(str(queue, "text"), "\r") || !strings.Contains(str(queue, "text"), operatorNote) {
		t.Fatal("review did not display the normalized edited body")
	}
	queue["acknowledge"] = false
	if w := admin.request(http.MethodPost, path+"/queue", queue); w.Code != http.StatusBadRequest {
		t.Fatal("unconfirmed draft was accepted", w.Code)
	}
	queue["acknowledge"] = true
	second := docLoginClient(t, admin.a, "admin@example.net", adminPassword)
	if w := second.request(http.MethodPost, path+"/queue", queue); w.Code != http.StatusConflict {
		t.Fatal("review proof was usable by another browser session", w.Code)
	}
	text := queue["text"]
	queue["text"] = "Text changed after the preview"
	if w := admin.request(http.MethodPost, path+"/queue", queue); w.Code != http.StatusConflict {
		t.Fatal("unreviewed text was queued", w.Code)
	}
	queue["text"] = text
	first := admin.request(http.MethodPost, path+"/queue", queue)
	if first.Code != http.StatusCreated {
		t.Fatalf("approved welcome message failed to queue: %d %s", first.Code, first.Body.String())
	}
	receipt := responseRecord(t, first)
	state := admin.a.store.Snapshot()
	if len(state.Outbox.Entries) != 1 || len(state.WelcomeMail) != 1 || receipt["status"] != "pending" || receipt["replayed"] != false {
		t.Fatal("queue result did not identify one durable pending delivery", receipt)
	}
	revision := admin.a.store.revision
	replay := admin.request(http.MethodPost, path+"/queue", queue)
	if replay.Code != http.StatusOK || responseRecord(t, replay)["outboxId"] != receipt["outboxId"] || admin.a.store.revision != revision {
		t.Fatal("identical review replay changed state", replay.Code)
	}
	sealed := state.Outbox.Entries[0]
	message, err := notify.OpenMessage(admin.a.store.securityKey, sealed)
	if err != nil || message.To != noc || len(message.Cc) != 1 || message.Cc[0] != login || len(message.Bcc) != 1 || message.Bcc[0] != support || message.Text != text {
		t.Fatal("encrypted outbox message differs from the approved preview", err)
	}
	publicMetadata := fmtJSON(Record{"outbox": state.Outbox, "receipts": state.WelcomeMail, "audit": state.Audit})
	if strings.Contains(publicMetadata, support) || strings.Contains(publicMetadata, operatorNote) || strings.Contains(publicMetadata, str(preview, "reviewToken")) {
		t.Fatal("private view data escaped its encrypted message")
	}
	admin.ok(http.MethodPut, "/api/resources/members/"+id, Record{"name": "Welcome member updated", "asn": 64501, "status": "active", "nocEmail": noc})
	if err := admin.a.deliverOne(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := admin.a.store.Snapshot().Outbox.Entries[0].Status; got != "expired" {
		t.Fatal("changed member data did not invalidate queued welcome message", got)
	}
	latest := queuePayload(admin.ok(http.MethodPost, path+"/review", initialDraft()))
	admin.ok(http.MethodPost, path+"/queue", latest)
	if err := admin.a.deliverOne(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.messages) != 1 || sender.messages[0].To != noc || !strings.Contains(sender.messages[0].Text, "Welcome member updated") || strings.Contains(sender.messages[0].Text, operatorNote) {
		t.Fatal("transport received stale or repeated mail")
	}
}

func TestMemberWelcomeBrowserAdminOnlyAndDisabledPreview(t *testing.T) {
	admin, _ := setupClient(t)
	const email, password = "welcome-viewer@example.test", "welcome-viewer-fixture-password"
	member := admin.create("members", Record{"name": "Pending member", "asn": 64501, "status": "pending", "nocEmail": "pending-noc@example.test"})
	path := "/api/members/" + str(member, "id") + "/welcome-email"
	admin.create("users", Record{"name": "Viewer", "email": email, "password": password, "role": "viewer"})
	viewer := docLoginClient(t, admin.a, email, password)
	if reply := viewer.request(http.MethodGet, path, nil); reply.Code != http.StatusForbidden {
		t.Fatal("viewer obtained the private welcome projection", reply.Code)
	}
	issued := admin.ok(http.MethodPost, "/api/account/tokens", Record{"name": "welcome fixture", "currentPassword": "very-long-test-password", "scopes": []string{"resources:read"}})
	if reply := bearerRequest(admin.a, str(issued, "token"), http.MethodGet, path, nil); reply.Code != http.StatusForbidden {
		t.Fatal("automation credential accessed the private welcome projection", reply.Code)
	}
	context := admin.ok(http.MethodGet, path, nil)
	preview := admin.ok(http.MethodPost, path+"/review", context["draft"])
	if preview["deliveryEnabled"] != false || !externalDigest(strings.SplitN(str(preview, "reviewToken"), ".", 2)[1]) {
		t.Fatal("disabled transport did not allow read-only preview", preview)
	}
	queue := Record(preview["draft"].(map[string]any))
	queue["reviewToken"], queue["expiresAt"], queue["acknowledge"] = preview["reviewToken"], preview["expiresAt"], true
	reply := admin.request(http.MethodPost, path+"/queue", queue)
	var failure Record
	if reply.Code != http.StatusServiceUnavailable || json.Unmarshal(reply.Body.Bytes(), &failure) != nil || str(failure, "error") == "" || len(admin.a.store.Snapshot().Outbox.Entries) != 0 {
		t.Fatal("disabled SMTP accepted a welcome delivery", reply.Code)
	}
}
