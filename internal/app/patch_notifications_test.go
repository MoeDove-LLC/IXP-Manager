package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

func patchMailFixture(t *testing.T, enabled bool) (*client, patchFixtureIDs) {
	t.Helper()
	c, ids := setupPatchFixture(t)
	c.a.mailConfig = notify.Config{Enabled: enabled, From: "ixp@example.test", BaseURL: "https://patch.example.test"}
	c.a.mailer = &captureSender{}
	if err := c.a.store.Update(func(s *State) error {
		find(s, "members", ids.member)["nocEmail"] = "noc@example.test"
		s.Settings["supportEmail"] = "operations-private@example.test"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	patchAction(c, ids.ports[0], "reserve", Record{"memberId": ids.member, "duplexPeerId": ids.ports[1], "switchPortId": ids.physical, "circuitId": "PATCH-001"})
	return c, ids
}
func patchMailPath(id string) string { return "/api/patching/ports/" + id + "/notifications" }
func patchMailDraft(c *client, id, kind string) Record {
	return Record(c.ok("GET", patchMailPath(id)+"?type="+kind, nil)["draft"].(map[string]any))
}
func patchMailQueueRequest(review Record) Record {
	draft := Record{}
	for key, value := range review["draft"].(map[string]any) {
		draft[key] = value
	}
	draft["reviewToken"], draft["expiresAt"], draft["acknowledge"] = review["reviewToken"], review["expiresAt"], true
	return draft
}
func patchMailReview(c *client, id string, draft Record) Record {
	return c.ok("POST", patchMailPath(id)+"/review", draft)
}
func patchMailEntry(t *testing.T, a *App) (notify.Entry, notify.Message) {
	t.Helper()
	for _, e := range a.store.Snapshot().Outbox.Entries {
		if strings.HasPrefix(e.Key, "patch:") {
			m, err := notify.OpenMessage(a.store.securityKey, e)
			if err != nil {
				t.Fatal(err)
			}
			return e, m
		}
	}
	t.Fatal("patch outbox entry missing")
	return notify.Entry{}, notify.Message{}
}

func TestPatchNotificationReviewQueueEncryptsAttachmentsAndIsIdempotent(t *testing.T) {
	c, ids := patchMailFixture(t, true)
	p := ids.ports[0]
	loa := issuePatchLOA(c, p)
	draft := patchMailDraft(c, p, "connect")
	if draft["loaId"] != loa["id"] || strings.Contains(str(draft, "text"), "staff-only") {
		t.Fatal("default LOA or member-safe body incorrect")
	}
	draft["to"] = []string{"noc@example.test", "manual-engineer@example.test"}
	draft["cc"] = []string{"manual-cc@example.test"}
	draft["subject"] = "人工确认的中文配线通知"
	draft["text"] = "private-edited-notification-body"
	before := c.a.store.Snapshot()
	review := patchMailReview(c, p, draft)
	if len(c.a.store.Snapshot().Outbox.Entries) != len(before.Outbox.Entries) {
		t.Fatal("preview queued mail")
	}
	if len(review["attachments"].([]any)) != 1 {
		t.Fatal("LOA attachment not previewed")
	}
	manual := false
	for _, raw := range review["recipients"].([]any) {
		r := Record(raw.(map[string]any))
		if str(r, "email") == "manual-engineer@example.test" {
			manual = r["manual"] == true && r["sourceId"] == nil
		}
	}
	if !manual {
		t.Fatal("manual recipient fabricated a member source")
	}
	request := patchMailQueueRequest(review)
	first := c.ok("POST", patchMailPath(p)+"/queue", request)
	second := c.ok("POST", patchMailPath(p)+"/queue", request)
	if first["notificationId"] != second["notificationId"] || second["replayed"] != true || len(c.a.store.Snapshot().Outbox.Entries) != 1 {
		t.Fatal("same reviewed request duplicated outbox")
	}
	e, m := patchMailEntry(t, c.a)
	if len(m.ToAdditional) != 1 || len(m.Cc) != 1 || len(m.Bcc) != 1 || m.Bcc[0] != "operations-private@example.test" || len(m.Attachments) != 1 || !bytes.HasPrefix(m.Attachments[0].Data, []byte("%PDF-")) {
		t.Fatal("queued snapshot lost recipients or real PDF")
	}
	state, _ := os.ReadFile(c.a.store.path)
	for _, secret := range []string{m.Cc[0], m.ToAdditional[0], m.Text, string(m.Attachments[0].Data[:20])} {
		if bytes.Contains(state, []byte(secret)) {
			t.Fatal("encrypted snapshot leaked into state or audit")
		}
	}
	stored := c.a.store.Snapshot()
	privateMail, _ := json.Marshal(Record{"outbox": stored.Outbox, "bindings": stored.PatchNotifications, "audit": stored.Audit})
	if bytes.Contains(privateMail, []byte(m.Bcc[0])) {
		t.Fatal("BCC leaked from its pre-existing support-email setting into outbox binding/audit")
	}
	public := c.request("GET", "/api/notifications", nil).Body.String()
	if strings.Contains(public, m.Bcc[0]) || strings.Contains(public, m.Text) {
		t.Fatal("outbox metadata leaked BCC/body")
	}
	if err := c.a.deliverOne(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	sender := c.a.mailer.(*captureSender)
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.messages) != 1 || sender.messages[0].ID != e.ID || c.a.store.Snapshot().Outbox.Entries[0].Status != "sent" {
		t.Fatal("valid immutable mail did not reach fake sender exactly once")
	}
}

func TestPatchNotificationTypesDocumentsAndDisabledPreview(t *testing.T) {
	c, ids := patchMailFixture(t, false)
	p := ids.ports[0]
	context := c.ok("GET", patchMailPath(p)+"?type=connect", nil)
	if context["deliveryEnabled"] != false {
		t.Fatal("disabled configuration misrepresented")
	}
	review := patchMailReview(c, p, Record(context["draft"].(map[string]any)))
	if w := c.request("POST", patchMailPath(p)+"/queue", patchMailQueueRequest(review)); w.Code != 503 || len(c.a.store.Snapshot().Outbox.Entries) != 0 {
		t.Fatal("disabled transport accumulated new mail")
	}
	if w := c.request("POST", patchMailPath(p)+"/review", patchMailDraft(c, p, "loa")); w.Code != 409 {
		t.Fatal("LOA mail without current PDF allowed")
	}
	c.a.mailConfig.Enabled = true
	member := responseRecord(t, c.upload("/api/documents", "current.txt", "member-visible-current-bytes", map[string]string{"memberId": ids.member, "visibility": "member"}))
	staff := responseRecord(t, c.upload("/api/documents", "private.txt", "staff-only-attachment", map[string]string{"visibility": "staff"}))
	patchAction(c, p, "request-connect", Record{"documentIds": []string{str(member, "id"), str(staff, "id")}})
	draft := patchMailDraft(c, p, "info")
	draft["documentIds"] = []string{str(staff, "id")}
	if w := c.request("POST", patchMailPath(p)+"/review", draft); w.Code != 409 {
		t.Fatal("staff-only attachment entered member mail")
	}
	draft["documentIds"] = []string{str(member, "id")}
	review = patchMailReview(c, p, draft)
	c.ok("POST", patchMailPath(p)+"/queue", patchMailQueueRequest(review))
	_, m := patchMailEntry(t, c.a)
	if len(m.Attachments) != 1 || string(m.Attachments[0].Data) != "member-visible-current-bytes" {
		t.Fatal("member attachment was replaced by a link or different bytes")
	}
	if w := c.request("POST", patchMailPath(p)+"/review", patchMailDraft(c, p, "cease")); w.Code != 409 {
		t.Fatal("cease request from reservation accepted")
	}
	patchAction(c, p, "connect", nil)
	patchMailReview(c, p, patchMailDraft(c, p, "cease"))
	if w := c.request("POST", patchMailPath(p)+"/review", patchMailDraft(c, p, "connect")); w.Code != 409 {
		t.Fatal("new connect request on connected allocation accepted")
	}
}

func TestPatchNotificationReviewRejectsStaleAuthorityAndTampering(t *testing.T) {
	for _, mode := range []string{"allocation", "noc", "member-entity", "port-entity", "physical-entity", "expiry", "edited-body", "logout", "actor-generation"} {
		t.Run(mode, func(t *testing.T) {
			c, ids := patchMailFixture(t, true)
			p := ids.ports[0]
			review := patchMailReview(c, p, patchMailDraft(c, p, "connect"))
			request := patchMailQueueRequest(review)
			switch mode {
			case "allocation":
				patchAction(c, p, "release", nil)
			case "noc", "member-entity", "port-entity", "physical-entity", "actor-generation":
				err := c.a.store.Update(func(s *State) error {
					switch mode {
					case "noc":
						find(s, "members", ids.member)["nocEmail"] = "changed@example.test"
					case "member-entity":
						find(s, "members", ids.member)["createdAt"] = "2001-01-01T00:00:00Z"
					case "port-entity":
						find(s, "patch-ports", p)["createdAt"] = "2001-01-01T00:00:00Z"
					case "physical-entity":
						find(s, "ports", ids.physical)["createdAt"] = "2001-01-01T00:00:00Z"
					case "actor-generation":
						id := accountIDUnsafe(s)
						security := s.Security[id]
						security.Generation++
						s.Security[id] = security
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			case "expiry":
				request["expiresAt"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
			case "edited-body":
				request["text"] = "edited after review"
			case "logout":
				c.ok("POST", "/api/logout", nil)
			}
			w := c.request("POST", patchMailPath(p)+"/queue", request)
			if w.Code < 400 || len(c.a.store.Snapshot().Outbox.Entries) != 0 {
				t.Fatal("stale review authorized mail", mode, w.Code)
			}
		})
	}
}

func TestPatchNotificationPendingRevocationAndRetryAreFailClosed(t *testing.T) {
	for _, mode := range []string{"reassigned", "contact", "loa-revoked", "actor-role", "document-revoked"} {
		t.Run(mode, func(t *testing.T) {
			c, ids := patchMailFixture(t, true)
			p := ids.ports[0]
			kind := "connect"
			var loa, doc Record
			if mode == "loa-revoked" {
				loa = issuePatchLOA(c, p)
			}
			if mode == "document-revoked" {
				kind = "info"
				doc = responseRecord(t, c.upload("/api/documents", "current.txt", "member-private-data", map[string]string{"memberId": ids.member, "visibility": "member"}))
				patchAction(c, p, "request-connect", Record{"documentIds": []string{str(doc, "id")}})
			}
			draft := patchMailDraft(c, p, kind)
			if doc != nil {
				draft["documentIds"] = []string{str(doc, "id")}
			}
			review := patchMailReview(c, p, draft)
			c.ok("POST", patchMailPath(p)+"/queue", patchMailQueueRequest(review))
			e, _ := patchMailEntry(t, c.a)
			switch mode {
			case "reassigned":
				patchAction(c, p, "release", nil)
				patchAction(c, p, "reserve", Record{"memberId": ids.other})
			case "loa-revoked":
				c.ok("POST", "/api/patching/loas/"+str(loa, "id")+"/revoke", Record{"reason": "fixture revoke"})
			default:
				if err := c.a.store.Update(func(s *State) error {
					switch mode {
					case "contact":
						find(s, "members", ids.member)["nocEmail"] = "different@example.test"
					case "actor-role":
						find(s, "users", accountIDUnsafe(s))["role"] = "viewer"
					case "document-revoked":
						documentByID(s, str(doc, "id")).Visibility = "staff"
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.a.deliverOne(context.Background(), time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			sender := c.a.mailer.(*captureSender)
			sender.mu.Lock()
			count := len(sender.messages)
			sender.mu.Unlock()
			if count != 0 || c.a.store.Snapshot().Outbox.Entries[0].Status != "expired" {
				t.Fatal("obsolete queued content reached fake SMTP")
			}
			if err := c.a.store.Update(func(s *State) error { s.Outbox.Entries[0].Status = "failed"; return nil }); err != nil {
				t.Fatal(err)
			}
			if mode != "actor-role" {
				if w := c.request("POST", "/api/notifications/"+e.ID+"/retry", nil); w.Code != 409 {
					t.Fatal("HTTP retry revived revoked patch mail", w.Code)
				}
			}
		})
	}
}

func TestPatchNotificationAdminScopeAndBodyReadLogout(t *testing.T) {
	c, ids := patchMailFixture(t, true)
	p := ids.ports[0]
	state := c.a.store.Snapshot()
	member := find(&state, "members", ids.member)
	for _, role := range []string{"operator", "viewer", "member"} {
		other := noteTestUser(t, c, role, "patch-"+role+"@example.test", member)
		if w := other.request("GET", patchMailPath(p), nil); w.Code != 403 {
			t.Fatal("nonadministrator saw compose", role)
		}
	}
	if err := c.a.store.Update(func(s *State) error {
		id := accountIDUnsafe(s)
		sec := s.Security[id]
		sec.Tokens = append(sec.Tokens, APIToken{ID: "patch", Hash: digest("mdt.patch.opaque"), Scopes: []string{"resources:read", "resources:write"}, ExpiresAt: time.Now().Add(time.Hour)})
		s.Security[id] = sec
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := bearerRequest(c.a, "mdt.patch.opaque", "GET", patchMailPath(p), nil); w.Code != 403 {
		t.Fatal("API token reached manual mail workflow")
	}
	review := patchMailReview(c, p, patchMailDraft(c, p, "connect"))
	raw, _ := json.Marshal(patchMailQueueRequest(review))
	body := &legacyPatchReadHook{Reader: bytes.NewReader(raw), hook: func() { c.a.mu.Lock(); delete(c.a.sessions, c.cookie.Value); c.a.mu.Unlock() }}
	r := httptest.NewRequest("POST", patchMailPath(p)+"/queue", body)
	r.AddCookie(c.cookie)
	r.Header.Set("X-CSRF-Token", c.csrf)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	if w.Code != 401 || len(c.a.store.Snapshot().Outbox.Entries) != 0 {
		t.Fatal("logout during body read queued mail")
	}
}

func TestPatchNotificationAttachmentTamperAndReplayAfterChange(t *testing.T) {
	c, ids := patchMailFixture(t, true)
	p := ids.ports[0]
	letter := issuePatchLOA(c, p)
	draft := patchMailDraft(c, p, "loa")
	review := patchMailReview(c, p, draft)
	request := patchMailQueueRequest(review)
	queued := c.ok("POST", patchMailPath(p)+"/queue", request)
	patchAction(c, p, "release", nil)
	again := c.ok("POST", patchMailPath(p)+"/queue", request)
	if again["notificationId"] != queued["notificationId"] || again["replayed"] != true || len(c.a.store.Snapshot().Outbox.Entries) != 1 {
		t.Fatal("lost response replay created a new event after allocation changed")
	}
	patchAction(c, p, "reserve", Record{"memberId": ids.member})
	letter = issuePatchLOA(c, p)
	draft = patchMailDraft(c, p, "loa")
	review = patchMailReview(c, p, draft)
	s := c.a.store.Snapshot()
	l := loaByID(&s, str(letter, "id"))
	if err := os.WriteFile(filepath.Join(c.a.blobDir(), l.PDFBlobID), []byte("corrupted reviewed PDF"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := c.request("POST", patchMailPath(p)+"/queue", patchMailQueueRequest(review)); w.Code != 409 || len(c.a.store.Snapshot().Outbox.Entries) != 1 {
		t.Fatal("tampered reviewed attachment queued")
	}
}

func TestPatchNotificationInactiveMemberCanReceiveCurrentCeaseAndInfo(t *testing.T) {
	for _, action := range []string{"suspend", "close"} {
		t.Run(action, func(t *testing.T) {
			c, ids := patchMailFixture(t, true)
			p := ids.ports[0]
			doc := responseRecord(t, c.upload("/api/documents", "decommission.txt", "current member decommission order", map[string]string{"memberId": ids.member, "visibility": "member"}))
			patchAction(c, p, "request-connect", Record{"documentIds": []string{str(doc, "id")}})
			patchAction(c, p, "connect", nil)
			allocation := currentPatch(c, p)["allocationId"]
			c.ok("POST", "/api/members/"+ids.member+"/"+action, Record{"reason": "current physical connection still needs decommissioning"})
			if currentPatch(c, p)["allocationId"] != allocation || currentPatch(c, p)["status"] != "connected" {
				t.Fatal("member lifecycle silently reassigned physical allocation")
			}
			for _, kind := range []string{"cease", "info"} {
				draft := patchMailDraft(c, p, kind)
				draft["documentIds"] = []string{str(doc, "id")}
				review := patchMailReview(c, p, draft)
				c.ok("POST", patchMailPath(p)+"/queue", patchMailQueueRequest(review))
				if err := c.a.deliverOne(context.Background(), time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			sender := c.a.mailer.(*captureSender)
			sender.mu.Lock()
			count := len(sender.messages)
			for _, message := range sender.messages {
				if len(message.Attachments) != 1 || string(message.Attachments[0].Data) != "current member decommission order" {
					t.Error("inactive member mail lost the current authorized work order")
				}
			}
			sender.mu.Unlock()
			if count != 2 {
				t.Fatal("inactive current member could not receive cease/info")
			}
			// A fresh explicit notification still expires when that allocation ends.
			review := patchMailReview(c, p, patchMailDraft(c, p, "info"))
			c.ok("POST", patchMailPath(p)+"/queue", patchMailQueueRequest(review))
			patchAction(c, p, "cease", nil)
			patchAction(c, p, "release", nil)
			patchAction(c, p, "reserve", Record{"memberId": ids.other})
			if err := c.a.deliverOne(context.Background(), time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			sender.mu.Lock()
			count = len(sender.messages)
			sender.mu.Unlock()
			if count != 2 || c.a.store.Snapshot().Outbox.Entries[2].Status != "expired" {
				t.Fatal("historical inactive-member context survived reassignment")
			}
		})
	}
}

func TestPatchNotificationLOARequiresCurrentAuthorizationAfterConnection(t *testing.T) {
	c, ids := patchMailFixture(t, true)
	p := ids.ports[0]
	letter := issuePatchLOA(c, p)
	draft := patchMailDraft(c, p, "loa")
	patchMailReview(c, p, draft)
	patchAction(c, p, "connect", nil)
	if w := c.request("GET", "/api/patching/loas/"+str(letter, "id")+"/download", nil); w.Code != 409 {
		t.Fatal("fixture does not match existing LOA validity rule")
	}
	context := c.ok("GET", patchMailPath(p)+"?type=loa", nil)
	if len(context["loas"].([]any)) != 0 {
		t.Fatal("connected reservation PDF was relabeled as a current LOA")
	}
	draft = Record(context["draft"].(map[string]any))
	draft["loaId"] = letter["id"]
	if w := c.request("POST", patchMailPath(p)+"/review", draft); w.Code != 409 {
		t.Fatal("stale connected LOA became sendable")
	}
	// Info remains available, but the stale authorization is not attached.
	info := patchMailReview(c, p, patchMailDraft(c, p, "info"))
	if len(info["attachments"].([]any)) != 0 {
		t.Fatal("info implicitly attached a historical LOA")
	}
	patchAction(c, p, "cease", nil)
	patchAction(c, p, "release", nil)
	patchAction(c, p, "reserve", Record{"memberId": ids.member})
	c.ok("POST", "/api/members/"+ids.member+"/suspend", Record{"reason": "fixture suspended reservation"})
	for _, kind := range []string{"connect", "loa"} {
		if w := c.request("POST", patchMailPath(p)+"/review", patchMailDraft(c, p, kind)); w.Code != 409 {
			t.Fatal("inactive reservation gained connect/LOA authority")
		}
	}
}
