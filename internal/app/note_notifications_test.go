package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

const noteTestPassword = "notes-fixture-password"

func noteTestUser(t *testing.T, admin *client, role, email string, member Record) *client {
	t.Helper()
	body := Record{"name": "Note recipient", "email": email, "password": noteTestPassword, "role": role}
	if role == "member" {
		body["memberId"] = member["id"]
	}
	admin.create("users", body)
	return docLoginClient(t, admin.a, email, noteTestPassword)
}
func noteTestPreference(t *testing.T, c *client, mode string, members, notes []Record) Record {
	t.Helper()
	current := c.ok("GET", "/api/account/note-preferences", nil)
	bindings := func(records []Record) []Record {
		out := []Record{}
		for _, r := range records {
			out = append(out, Record{"id": r["id"], "createdAt": r["createdAt"]})
		}
		return out
	}
	return c.ok("PUT", "/api/account/note-preferences", Record{"revision": current["revision"], "mode": mode, "memberWatches": bindings(members), "noteWatches": bindings(notes)})
}
func noteTestCreate(c *client, member Record, title, body string) Record {
	return c.create("notes", Record{"name": title, "body": body, "memberId": member["id"], "visibility": "internal"})
}
func noteTestUpdate(c *client, n Record, body string) Record {
	return c.ok("PUT", "/api/resources/notes/"+str(n, "id"), Record{"name": n["name"], "body": body, "memberId": n["memberId"], "visibility": n["visibility"]})
}
func noteTestMessages(t *testing.T, a *App) []notify.Message {
	t.Helper()
	messages := []notify.Message{}
	for _, e := range a.store.Snapshot().Outbox.Entries {
		if strings.HasPrefix(e.Key, "note:") {
			m, err := notify.OpenMessage(a.store.securityKey, e)
			if err != nil {
				t.Fatal(err)
			}
			messages = append(messages, m)
		}
	}
	return messages
}

func TestNotePreferencesDefaultSelfOnlyCASAndStaleEntity(t *testing.T) {
	c, _ := setupClient(t)
	member := c.create("members", Record{"name": "Member", "asn": 64501})
	viewer := noteTestUser(t, c, "viewer", "note-viewer@example.test", nil)
	ordinary := noteTestUser(t, c, "member", "note-member@example.test", member)
	p := viewer.ok("GET", "/api/account/note-preferences", nil)
	if p["mode"] != "none" || p["revision"] != float64(0) || p["deliveryEnabled"] != false || p["legacyPreferencesImported"] != false {
		t.Fatal("new preferences silently enabled or imported")
	}
	for _, path := range []string{"/api/account/note-preferences", "/api/account/note-summary"} {
		if ordinary.request("GET", path, nil).Code != 403 {
			t.Fatal("member reached internal subscription domain")
		}
	}
	note := noteTestCreate(c, member, "Private", "secret")
	p = noteTestPreference(t, viewer, "watched", []Record{member}, []Record{note})
	if p["revision"] != float64(1) {
		t.Fatal("missing preference CAS revision")
	}
	if w := viewer.request("PUT", "/api/account/note-preferences", Record{"revision": 0, "mode": "all"}); w.Code != 409 {
		t.Fatal("stale CAS accepted", w.Code)
	}
	if w := viewer.request("PUT", "/api/account/note-preferences", Record{"revision": 1, "mode": "all", "userId": "another"}); w.Code != 400 {
		t.Fatal("caller supplied subscription identity")
	}
	if w := viewer.request("PUT", "/api/account/note-preferences", Record{"revision": 1, "mode": "watched", "memberWatches": []Record{{"id": member["id"], "createdAt": "2000-01-01T00:00:00Z"}}}); w.Code != 409 {
		t.Fatal("wrong entity stamp accepted")
	}
	c.ok("DELETE", "/api/resources/notes/"+str(note, "id"), nil)
	stale := viewer.ok("GET", "/api/account/note-preferences", nil)
	watches := stale["noteWatches"].([]any)
	if watches[0].(map[string]any)["stale"] != true {
		t.Fatal("deleted watch not marked stale")
	}
	noteTestPreference(t, viewer, "none", []Record{member}, []Record{note}) // preserve old stale binding while disabling
	p = noteTestPreference(t, viewer, "none", nil, nil)
	if len(p["noteWatches"].([]any)) != 0 {
		t.Fatal("stale watch cannot be removed")
	}
	if len(noteTestMessages(t, c.a)) != 0 {
		t.Fatal("disabled transport created backlog")
	}
}

func TestNoteChangesNotifyUnionAndKeepImmutableBoundedSnapshots(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	one := c.create("members", Record{"name": "One", "asn": 64501})
	two := c.create("members", Record{"name": "Two", "asn": 64502})
	viewer := noteTestUser(t, c, "viewer", "notes-watcher@example.test", nil)
	n := noteTestCreate(c, one, "Watched", "before snapshot")
	noteTestPreference(t, c, "all", nil, nil)
	noteTestPreference(t, viewer, "watched", []Record{one}, []Record{n})
	other := noteTestCreate(c, two, "Unwatched", "other")
	if len(noteTestMessages(t, c.a)) != 1 {
		t.Fatal("watcher received unrelated member")
	}
	oldTag := str(n, "_etag")
	n = noteTestUpdate(c, n, "after snapshot")
	if len(noteTestMessages(t, c.a)) != 3 {
		t.Fatal("member+note watch duplicated, or actor subscription excluded")
	}
	if w := requestResourceRevision(c, "PUT", "/api/resources/notes/"+str(n, "id"), Record{"name": n["name"], "body": "after snapshot", "memberId": n["memberId"], "visibility": n["visibility"]}, fmt.Sprintf("%q", oldTag)); w.Code != 412 {
		t.Fatal("replayed mutation bypassed revision", w.Code)
	}
	if len(noteTestMessages(t, c.a)) != 3 {
		t.Fatal("replayed mutation duplicated notification")
	}
	first := noteTestMessages(t, c.a)[1]
	if !strings.Contains(first.Text, "before snapshot") || !strings.Contains(first.Text, "after snapshot") || !strings.Contains(first.Text, "https://manager.example.test/#notes?") {
		t.Fatal("immutable old/new content or configured origin missing")
	}
	n = noteTestUpdate(c, n, "after snapshot")
	if len(noteTestMessages(t, c.a)) != 3 {
		t.Fatal("no-op update emitted mail")
	}
	n["visibility"] = "member"
	n = noteTestUpdate(c, n, "after snapshot")
	if len(noteTestMessages(t, c.a)) != 5 {
		t.Fatal("visibility change omitted")
	}
	long := strings.Repeat("文", 21845) // 65535 bytes, valid API content larger than one mail excerpt
	n = noteTestUpdate(c, n, long)
	last := noteTestMessages(t, c.a)[6]
	if len(last.Text) > 64<<10 || !strings.Contains(last.Text, "仅显示摘录") {
		t.Fatal("large legal note broke bounded notification")
	}
	if noteTestMessages(t, c.a)[1].Text != first.Text {
		t.Fatal("later edit rewrote queued snapshot")
	}
	c.ok("DELETE", "/api/resources/notes/"+str(n, "id"), nil)
	if len(noteTestMessages(t, c.a)) != 9 {
		t.Fatal("delete did not notify watched old snapshot")
	}
	encoded, _ := json.Marshal(c.a.store.Snapshot().Outbox)
	if strings.Contains(string(encoded), "before snapshot") || strings.Contains(string(encoded), "after snapshot") {
		t.Fatal("outbox persisted plaintext body")
	}
	c.a.mailConfig.Enabled = false
	noteTestUpdate(c, other, "transport disabled")
	if len(noteTestMessages(t, c.a)) != 9 {
		t.Fatal("disabled transport accumulated old notifications")
	}
}

func TestNoteReadRevisionCASAndMemberScope(t *testing.T) {
	c, _ := setupClient(t)
	one := c.create("members", Record{"name": "One", "asn": 64501})
	two := c.create("members", Record{"name": "Two", "asn": 64502})
	viewer := noteTestUser(t, c, "viewer", "notes-reader@example.test", nil)
	n := noteTestCreate(c, one, "One note", "original")
	noteTestCreate(c, two, "Two note", "other")
	summary := viewer.ok("GET", "/api/account/note-summary", nil)
	if summary["unreadCount"] != float64(2) {
		t.Fatal("fresh staff unread count wrong")
	}
	if c.ok("GET", "/api/account/note-summary", nil)["unreadCount"] != float64(0) {
		t.Fatal("own edits incorrectly unread")
	}
	summary = viewer.ok("POST", "/api/account/note-read", Record{"revision": summary["revision"], "memberId": one["id"]})
	if summary["unreadCount"] != float64(1) {
		t.Fatal("member read cleared unrelated notes")
	}
	oldRevision := summary["revision"]
	noteTestUpdate(c, n, "new content")
	if w := viewer.request("POST", "/api/account/note-read", Record{"revision": oldRevision}); w.Code != 409 {
		t.Fatal("stale read swallowed newer change", w.Code)
	}
	summary = viewer.ok("GET", "/api/account/note-summary", nil)
	if summary["unreadCount"] != float64(2) {
		t.Fatal("new revision did not become unread")
	}
	summary = viewer.ok("POST", "/api/account/note-read", Record{"revision": summary["revision"]})
	if summary["unreadCount"] != float64(0) {
		t.Fatal("explicit current read-all failed")
	}
	if len(noteTestMessages(t, c.a)) != 0 {
		t.Fatal("read state generated mail")
	}
	// A baseline inventory record is visible as unread, with no invented event.
	if err := c.a.store.Update(func(s *State) error {
		s.Resources["notes"] = append(s.Resources["notes"], Record{"id": "legacy-notes-123", "name": "Imported source", "memberId": one["id"], "body": "source", "visibility": "internal", "createdAt": "2020-01-01T00:00:00Z", "updatedAt": "2020-01-01T00:00:00Z"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	summary = viewer.ok("GET", "/api/account/note-summary", nil)
	if summary["unreadCount"] != float64(1) {
		t.Fatal("new imported baseline hidden by global read cursor")
	}
	for _, raw := range summary["items"].([]any) {
		item := raw.(map[string]any)
		if item["id"] == "legacy-notes-123" && item["revision"] != float64(0) {
			t.Fatal("source import fabricated a change revision")
		}
	}
}

func TestNotePendingMailRechecksAuthoritySubscriptionAndEntities(t *testing.T) {
	c, _ := setupClient(t)
	sender := enableTestMail(c.a)
	member := c.create("members", Record{"name": "Member", "asn": 64501})
	viewer := noteTestUser(t, c, "viewer", "notes-revoke@example.test", nil)
	noteTestPreference(t, viewer, "watched", []Record{member}, nil)
	note := noteTestCreate(c, member, "Revocable", "private")
	base := c.a.store.Snapshot()
	if len(base.Outbox.Entries) != 1 {
		t.Fatal("expected one recipient")
	}
	binding := base.NoteNotifications.Mail[0]
	for name, mutate := range map[string]func(*State){
		"downgrade":      func(s *State) { find(s, "users", binding.UserID)["role"] = "member" },
		"email-change":   func(s *State) { find(s, "users", binding.UserID)["email"] = "different@example.test" },
		"user-recreated": func(s *State) { find(s, "users", binding.UserID)["createdAt"] = "2030-01-01T00:00:00Z" },
		"security-generation": func(s *State) {
			security := s.Security[binding.UserID]
			security.Generation++
			s.Security[binding.UserID] = security
		},
		"unsubscribe": func(s *State) {
			for i := range s.NoteNotifications.Preferences {
				p := &s.NoteNotifications.Preferences[i]
				if p.UserID == binding.UserID {
					p.Mode = "none"
					p.Revision++
				}
			}
		},
		"unsubscribe-resubscribe": func(s *State) {
			for i := range s.NoteNotifications.Preferences {
				p := &s.NoteNotifications.Preferences[i]
				if p.UserID == binding.UserID {
					p.Revision += 2
				}
			}
		},
		"crashed-lease-revoked": func(s *State) {
			e := &s.Outbox.Entries[0]
			e.Status = "sending"
			e.Attempts = 1
			e.Lease = "synthetic-crashed-lease"
			e.LeaseUntil = time.Now().Add(-time.Second)
			for i := range s.NoteNotifications.Preferences {
				p := &s.NoteNotifications.Preferences[i]
				if p.UserID == binding.UserID {
					p.Mode = "none"
					p.Revision++
				}
			}
		},
		"member-recreated": func(s *State) { find(s, "members", str(member, "id"))["createdAt"] = "2030-01-01T00:00:00Z" },
		"note-recreated":   func(s *State) { find(s, "notes", str(note, "id"))["createdAt"] = "2030-01-01T00:00:00Z" },
		"note-deleted":     func(s *State) { s.Resources["notes"] = nil },
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.a.store.Update(func(s *State) error { *s = cloneState(base); mutate(s); return nil }); err != nil {
				t.Fatal(err)
			}
			if err := c.a.deliverOne(context.Background(), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if len(sender.messages) != 0 || c.a.store.Snapshot().Outbox.Entries[0].Status != "expired" {
				t.Fatal("obsolete private snapshot sent")
			}
		})
	}
	if err := c.a.store.Update(func(s *State) error { *s = cloneState(base); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := c.a.deliverOne(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 || c.a.store.Snapshot().Outbox.Entries[0].Status != "sent" {
		t.Fatal("current authorized snapshot not delivered")
	}
}

func TestNoteDeliveryRetriesStableMessageAndRefusesRevokedRetry(t *testing.T) {
	c, _ := setupClient(t)
	sender := enableTestMail(c.a)
	sender.fail = true
	member := c.create("members", Record{"name": "Member", "asn": 64501})
	noteTestPreference(t, c, "all", nil, nil)
	noteTestCreate(c, member, "Retry snapshot", "immutable private text")
	for attempt := 0; attempt < notify.MaxAttempts; attempt++ {
		e := c.a.store.Snapshot().Outbox.Entries[0]
		if err := c.a.deliverOne(context.Background(), e.NextAttemptAt); err != nil {
			t.Fatal(err)
		}
	}
	e := c.a.store.Snapshot().Outbox.Entries[0]
	if e.Status != "failed" || len(sender.messages) != notify.MaxAttempts {
		t.Fatal("retry bound failed")
	}
	for _, m := range sender.messages {
		if m.ID != sender.messages[0].ID || m.Text != sender.messages[0].Text || m.To != sender.messages[0].To {
			t.Fatal("retry changed immutable snapshot")
		}
	}
	noteTestPreference(t, c, "none", nil, nil)
	if w := c.request("POST", "/api/notifications/"+e.ID+"/retry", nil); w.Code != 409 {
		t.Fatal("manual retry revived unsubscribed mail", w.Code)
	}
	if c.a.store.Snapshot().Outbox.Entries[0].Status != "failed" {
		t.Fatal("rejected retry changed queue state")
	}
}

func TestNotePreferencesConcurrentCASAndTransactionLogout(t *testing.T) {
	c, _ := setupClient(t)
	var wait sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, mode := range []string{"all", "watched"} {
		wait.Add(1)
		go func(mode string) {
			defer wait.Done()
			responses <- c.request("PUT", "/api/account/note-preferences", Record{"revision": 0, "mode": mode})
		}(mode)
	}
	wait.Wait()
	close(responses)
	counts := map[int]int{}
	for response := range responses {
		counts[response.Code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent preference update lost CAS", counts)
	}
	user := c.ok("GET", "/api/session", nil)["user"].(map[string]any)
	c.a.mu.Lock()
	session := c.a.sessions[c.cookie.Value]
	delete(c.a.sessions, c.cookie.Value)
	c.a.mu.Unlock()
	if err := c.a.store.Update(func(s *State) error {
		return c.a.noteChanged(s, Record(user), session, nil, Record{"id": "synthetic-note", "createdAt": time.Now().UTC().Format(time.RFC3339Nano)})
	}); err == nil {
		t.Fatal("logged-out note writer passed transaction authorization")
	}
}

func TestNoteQueueCapacityRollsBackWholeMutation(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	member := c.create("members", Record{"name": "Member", "asn": 64501})
	viewer := noteTestUser(t, c, "viewer", "notes-capacity@example.test", nil)
	noteTestPreference(t, c, "all", nil, nil)
	noteTestPreference(t, viewer, "all", nil, nil)
	if err := c.a.store.Update(func(s *State) error {
		for i := 0; i < notify.MaxEntries-1; i++ {
			if _, e := s.Outbox.Enqueue(fmt.Sprintf("fixture:%d", i), notify.Message{From: "ixp@example.test", To: "recipient@example.test", Subject: "Capacity fixture", Text: "synthetic"}, c.a.store.securityKey, time.Now(), time.Time{}); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := c.a.store.Snapshot()
	if w := c.request("POST", "/api/resources/notes", Record{"name": "Must roll back", "body": "private", "memberId": member["id"], "visibility": "internal"}); w.Code < 400 {
		t.Fatal("capacity overflow succeeded")
	}
	after := c.a.store.Snapshot()
	if len(after.Resources["notes"]) != 0 || len(after.Outbox.Entries) != len(before.Outbox.Entries) || len(after.Audit) != len(before.Audit) || after.NoteNotifications.Sequence != before.NoteNotifications.Sequence {
		t.Fatal("partial note or recipient committed")
	}
}

func TestNoteNotificationStateSurvivesRestart(t *testing.T) {
	c, dir := setupClient(t)
	enableTestMail(c.a)
	member := c.create("members", Record{"name": "Member", "asn": 64501})
	noteTestPreference(t, c, "all", nil, nil)
	noteTestCreate(c, member, "Persisted", "source")
	if err := c.a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{DataDir: dir, BirdBinary: "/nonexistent/bird"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := a.store.Snapshot()
	if s.NoteNotifications == nil || len(s.NoteNotifications.Mail) != 1 || len(noteTestMessages(t, a)) != 1 {
		t.Fatal("restart lost note state")
	}
}

func TestNoteBindingCollectedAfterSharedQueuePruning(t *testing.T) {
	for _, path := range []string{"cleanup-job", "unrelated-enqueue"} {
		t.Run(path, func(t *testing.T) {
			c, dir := setupClient(t)
			enableTestMail(c.a)
			member := c.create("members", Record{"name": "Member", "asn": 64501})
			noteTestPreference(t, c, "all", nil, nil)
			noteTestCreate(c, member, "Old mail", "snapshot")
			now := time.Now().UTC()
			if err := c.a.store.Update(func(s *State) error {
				e := &s.Outbox.Entries[0]
				e.Status = "sent"
				e.CreatedAt = now.Add(-31 * 24 * time.Hour)
				e.SentAt = e.CreatedAt
				e.ExpiresAt = now.Add(-24 * time.Hour)
				for i := range s.Jobs {
					s.Jobs[i].NextRunAt = now.Add(time.Hour)
					if s.Jobs[i].Kind == "cleanup" {
						s.Jobs[i].NextRunAt = now
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if path == "cleanup-job" {
				if err := c.a.runDueJob(now); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := c.a.store.Update(func(s *State) error {
					_, err := s.Outbox.Enqueue("unrelated-fixture", notify.Message{From: "ixp@example.test", To: "recipient@example.test", Subject: "Unrelated workflow", Text: "synthetic"}, c.a.store.securityKey, now, time.Time{})
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if len(c.a.store.Snapshot().NoteNotifications.Mail) != 0 {
				t.Fatal("shared queue pruning left a binding")
			}
			if err := c.a.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(dir)
			if err != nil {
				t.Fatal("strict startup validation rejected cleaned state", err)
			}
			reopened.Close()
		})
	}
}
