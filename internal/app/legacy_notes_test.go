package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

const legacyNoteTestUserID int64 = 9007199254740993

func legacyNotesTestArchive(t *testing.T) ([]byte, *migrate.NoteArchive) {
	t.Helper()
	facts := func(values map[string]any) migrate.NoteArchiveFacts {
		out := migrate.NoteArchiveFacts{}
		for key, value := range values {
			out[key], _ = json.Marshal(value)
		}
		return out
	}
	archive := migrate.NoteArchive{Version: 1, Kind: migrate.NoteArchiveKind, Source: "ixp-manager-legacy", Access: "staff", SourceTimezone: "unknown", Warnings: []string{"Original defaults and formats are not current authorization."}}
	archive.Notes = []migrate.NoteArchiveNote{{ID: migrate.NoteArchiveID("source", 1), LegacyID: 1, Facts: facts(map[string]any{"id": 1, "customer_id": 41, "title": "  Original title  ", "note": " OLD PRIVATE\r\nSOURCE ", "private": "unknown", "created": "2017-01-02"}), Visibility: "internal", PrivacyKnown: false}}
	archive.Users = []migrate.NoteArchiveUser{{ID: migrate.NoteArchiveID("user", legacyNoteTestUserID), LegacyID: legacyNoteTestUserID, Facts: facts(map[string]any{"id": legacyNoteTestUserID, "username": "old-staff", "email": "legacy-note-viewer@example.test", "disabled": true})}}
	archive.Preferences = []migrate.NoteArchivePreference{
		{ID: migrate.NoteArchiveID("modern-pref", legacyNoteTestUserID), Format: "user.prefs", LegacyID: legacyNoteTestUserID, LegacyUserID: legacyNoteTestUserID, PrefsState: "object", NotesState: "object", Facts: migrate.NoteArchiveFacts{}, Notes: facts(map[string]any{"global_notifs": "all", "customer_watching": map[string]any{"41": "2018-01-01"}, "note_watching": map[string]any{"1": "2018-01-01"}, "last_read": map[string]any{"41": "2099-01-01"}, "read_upto": "2099-01-01"})},
		{ID: migrate.NoteArchiveID("legacy-pref", 7), Format: "user_pref", LegacyID: 7, LegacyUserID: legacyNoteTestUserID, Facts: facts(map[string]any{"id": 7, "user_id": legacyNoteTestUserID, "attribute": "customer-notes.notify", "value": "none", "ix": nil, "op": "=", "expire": "2099-01-01"})},
	}
	raw, err := migrate.EncodeNoteArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := migrate.ReadNoteArchive(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return raw, validated
}

func legacyNotesBodyRequest(c *client, method, path string, raw []byte, hook func()) *httptest.ResponseRecorder {
	c.t.Helper()
	var reader io.Reader = bytes.NewReader(raw)
	if hook != nil {
		reader = &legacyPatchReadHook{Reader: reader, hook: hook}
	}
	r := httptest.NewRequest(method, path, reader)
	r.RemoteAddr = "127.0.0.1:10000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", c.csrf)
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	return w
}

func legacyNotesFixture(t *testing.T) (*client, *client, Record, Record, *migrate.NoteArchive) {
	t.Helper()
	admin, _ := setupClient(t)
	member := admin.create("members", Record{"name": "Current member", "asn": 64501})
	note := noteTestCreate(admin, member, "Current note", "current content")
	viewer := noteTestUser(t, admin, "viewer", "legacy-note-viewer@example.test", nil)
	raw, archive := legacyNotesTestArchive(t)
	responseRecord(t, legacyNotesBodyRequest(admin, "POST", "/api/import/note-archive", raw, nil))
	return admin, viewer, member, note, archive
}
func legacyNotesMapping(t *testing.T, admin, target *client, archive *migrate.NoteArchive) Record {
	t.Helper()
	session := target.ok("GET", "/api/session", nil)
	user := session["user"].(map[string]any)
	s := admin.a.store.Snapshot()
	current := find(&s, "users", user["id"].(string))
	return Record(admin.ok("POST", "/api/notes/legacy/mappings", Record{"archiveChecksum": archive.Checksum, "sourceUserId": strconv.FormatInt(legacyNoteTestUserID, 10), "targetUserId": current["id"], "targetUserCreatedAt": current["createdAt"]})["mapping"].(map[string]any))
}
func legacyNotesReview(t *testing.T, viewer *client, mapping Record, member, note Record, mode string) Record {
	t.Helper()
	p := viewer.ok("GET", "/api/account/note-preferences", nil)
	body := Record{"revision": mapping["revision"], "preferenceRevision": p["revision"]}
	if mode != "" {
		body["mode"] = mode
	}
	if member != nil {
		body["memberWatches"] = []Record{{"id": member["id"], "createdAt": member["createdAt"]}}
	}
	if note != nil {
		body["noteWatches"] = []Record{{"id": note["id"], "createdAt": note["createdAt"]}}
	}
	return viewer.ok("POST", "/api/account/note-migrations/"+str(mapping, "id")+"/review", body)
}
func legacyNotesAcceptBody(review Record) Record {
	return Record{"revision": review["mapping"].(map[string]any)["revision"], "reviewToken": review["reviewToken"], "acknowledge": true}
}

func TestLegacyNotesArchiveIsStaffOnlyProvenance(t *testing.T) {
	admin, _ := setupClient(t)
	member := admin.create("members", Record{"name": "Member", "asn": 64501})
	viewer := noteTestUser(t, admin, "viewer", "legacy-note-viewer@example.test", nil)
	ordinary := noteTestUser(t, admin, "member", "legacy-note-member@example.test", member)
	before := admin.a.store.Snapshot()
	raw, archive := legacyNotesTestArchive(t)
	result := responseRecord(t, legacyNotesBodyRequest(admin, "POST", "/api/import/note-archive", raw, nil))
	if result["livePreferencesChanged"] != false || result["mailQueued"] != false {
		t.Fatal("source import claimed live authorization")
	}
	after := admin.a.store.Snapshot()
	if !reflect.DeepEqual(before.Resources, after.Resources) || !reflect.DeepEqual(before.NoteNotifications, after.NoteNotifications) || !reflect.DeepEqual(before.Outbox, after.Outbox) || len(after.LegacyNotes.Mappings) != 0 {
		t.Fatal("source import changed current inventory/preferences/reads/mail or guessed a mapping")
	}
	if len(viewer.ok("GET", "/api/account/note-migrations", nil)["items"].([]any)) != 0 {
		t.Fatal("matching old email silently mapped a current account")
	}
	listing := viewer.ok("GET", "/api/notes/legacy", nil)
	if listing["canManage"] != false || len(listing["batches"].([]any)) != 1 {
		t.Fatal("staff archive listing or management boundary wrong")
	}
	download := viewer.request("GET", "/api/notes/legacy/archives/"+archive.Checksum+"?download=1", nil)
	if download.Code != 200 || !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("source archive download unavailable")
	}
	restored, err := migrate.ReadNoteArchive(bytes.NewReader(download.Body.Bytes()))
	if err != nil || !reflect.DeepEqual(archive, restored) {
		t.Fatal("source archive download lost raw values or int64 identity", err)
	}
	for _, path := range []string{"/api/notes/legacy", "/api/notes/legacy/archives/" + archive.Checksum, "/api/account/note-migrations"} {
		if w := ordinary.request("GET", path, nil); w.Code != 403 {
			t.Fatal("member reached internal source archive", path, w.Code)
		}
	}
	if w := legacyNotesBodyRequest(viewer, "POST", "/api/import/note-archive", raw, nil); w.Code != 403 {
		t.Fatal("viewer imported an archive")
	}
	if w := legacyNotesBodyRequest(admin, "POST", "/api/import/note-archive", raw, nil); w.Code != 409 {
		t.Fatal("duplicate archive accepted")
	}
	if len(admin.a.store.Snapshot().LegacyNotes.Archives) != 1 {
		t.Fatal("duplicate import partially changed state")
	}
}

func TestLegacyNotesMappingRequiresExplicitEntityAndOwner(t *testing.T) {
	admin, viewer, _, _, archive := legacyNotesFixture(t)
	other := noteTestUser(t, admin, "viewer", "other-note-viewer@example.test", nil)
	s := admin.a.store.Snapshot()
	var user Record
	for _, u := range s.Resources["users"] {
		if str(u, "email") == "legacy-note-viewer@example.test" {
			user = u
		}
	}
	body := Record{"archiveChecksum": archive.Checksum, "sourceUserId": strconv.FormatInt(legacyNoteTestUserID, 10), "targetUserId": user["id"], "targetUserCreatedAt": user["createdAt"]}
	if w := viewer.request("POST", "/api/notes/legacy/mappings", body); w.Code != 403 {
		t.Fatal("non-admin assigned a source account")
	}
	for _, change := range []Record{{"sourceUserId": "9007199254740992"}, {"targetUserCreatedAt": "2000-01-01T00:00:00Z"}, {"targetUserId": "missing-current-account"}} {
		bad := Record{}
		for k, v := range body {
			bad[k] = v
		}
		for k, v := range change {
			bad[k] = v
		}
		if w := admin.request("POST", "/api/notes/legacy/mappings", bad); w.Code != 409 {
			t.Fatal("unverified source/current account mapping accepted", w.Code)
		}
	}
	mapping := Record(admin.ok("POST", "/api/notes/legacy/mappings", body)["mapping"].(map[string]any))
	if mapping["sourceUserId"] != "9007199254740993" {
		t.Fatal("source account ID lost precision")
	}
	if w := admin.request("POST", "/api/notes/legacy/mappings", body); w.Code != 409 {
		t.Fatal("one source account obtained two active mappings")
	}
	self := viewer.ok("GET", "/api/account/note-migrations", nil)["items"].([]any)
	if len(self) != 1 || !strings.Contains(self[0].(map[string]any)["sourceJSON"].(string), "9007199254740993") {
		t.Fatal("assigned account cannot see exact source values")
	}
	if len(other.ok("GET", "/api/account/note-migrations", nil)["items"].([]any)) != 0 {
		t.Fatal("another account received the pending mapping")
	}
	if w := other.request("POST", "/api/account/note-migrations/"+str(mapping, "id")+"/review", Record{"revision": 1, "preferenceRevision": 0}); w.Code != 404 {
		t.Fatal("another staff account reviewed someone else's mapping")
	}
	path := "/api/notes/legacy/mappings/" + str(mapping, "id") + "/cancel"
	if w := admin.request("POST", path, Record{"revision": 2}); w.Code != 409 {
		t.Fatal("stale cancellation accepted")
	}
	admin.ok("POST", path, Record{"revision": 1})
	if len(viewer.ok("GET", "/api/account/note-migrations", nil)["items"].([]any)) != 0 {
		t.Fatal("cancelled mapping still pending")
	}
	admin.ok("POST", "/api/notes/legacy/mappings", body) // an explicit new draft may replace a cancelled draft
}

func TestLegacyNotesAcceptancePreservesLiveReadsAndDoesNotQueue(t *testing.T) {
	admin, viewer, member, note, archive := legacyNotesFixture(t)
	enableTestMail(admin.a)
	noteTestPreference(t, viewer, "all", nil, nil)
	note = noteTestUpdate(admin, note, "current queued change")
	mapping := legacyNotesMapping(t, admin, viewer, archive)
	review := legacyNotesReview(t, viewer, mapping, member, note, "watched")
	if review["email"] != "legacy-note-viewer@example.test" || review["before"].(map[string]any)["mode"] != "all" || review["after"].(map[string]any)["mode"] != "watched" {
		t.Fatal("review did not show current recipient and explicit before/after choices")
	}
	before := admin.a.store.Snapshot()
	if len(before.Outbox.Entries) != 1 {
		t.Fatal("fixture did not create the existing pending message")
	}
	// Reading while the review is open is independent of subscription CAS.
	summary := viewer.ok("GET", "/api/account/note-summary", nil)
	viewer.ok("POST", "/api/account/note-read", Record{"revision": summary["revision"]})
	readState := admin.a.store.Snapshot()
	var previousReads []NoteReadMark
	for _, p := range readState.NoteNotifications.Preferences {
		if p.UserID == before.LegacyNotes.Mappings[0].TargetUserID {
			previousReads = p.Reads
		}
	}
	accepted := viewer.ok("POST", "/api/account/note-migrations/"+str(mapping, "id")+"/accept", legacyNotesAcceptBody(review))
	if accepted["mailQueued"] != false || accepted["legacyReadCursorsApplied"] != false || accepted["legacyBodiesApplied"] != false || accepted["preferences"].(map[string]any)["mode"] != "watched" {
		t.Fatal("acceptance imported old state or ignored the user's chosen mode")
	}
	after := admin.a.store.Snapshot()
	if !reflect.DeepEqual(before.Resources["notes"], after.Resources["notes"]) || !reflect.DeepEqual(before.Outbox, after.Outbox) || before.NoteNotifications.Sequence != after.NoteNotifications.Sequence {
		t.Fatal("acceptance changed note contents/events or queued mail")
	}
	for _, p := range after.NoteNotifications.Preferences {
		if p.UserID == before.LegacyNotes.Mappings[0].TargetUserID && !reflect.DeepEqual(previousReads, p.Reads) {
			t.Fatal("legacy read cursors replaced current reads")
		}
	}
	if noteMailApplicable(&after, after.Outbox.Entries[0]) {
		t.Fatal("changed subscriptions left the old queue authorized")
	}
	if m := legacyNoteMapping(&after, str(mapping, "id")); m.Status != "accepted" || m.Review != nil || m.AcceptedSelection.Mode != "watched" {
		t.Fatal("acceptance receipt or consumed token missing")
	}
	if w := viewer.request("POST", "/api/account/note-migrations/"+str(mapping, "id")+"/accept", legacyNotesAcceptBody(review)); w.Code != 409 {
		t.Fatal("accepted review token was replayed")
	}
}

func TestLegacyNotesReviewExpiryRebindingAndConcurrentAccept(t *testing.T) {
	admin, viewer, member, note, archive := legacyNotesFixture(t)
	mapping := legacyNotesMapping(t, admin, viewer, archive)
	review := legacyNotesReview(t, viewer, mapping, member, note, "watched")
	base := admin.a.store.Snapshot()
	admin.a.mu.Lock()
	sessions := map[string]Session{}
	for k, v := range admin.a.sessions {
		sessions[k] = v
	}
	admin.a.mu.Unlock()
	reset := func(t *testing.T) {
		t.Helper()
		if err := admin.a.store.Update(func(s *State) error { *s = cloneState(base); return nil }); err != nil {
			t.Fatal(err)
		}
		admin.a.mu.Lock()
		admin.a.sessions = map[string]Session{}
		for k, v := range sessions {
			admin.a.sessions[k] = v
		}
		admin.a.mu.Unlock()
	}
	id := str(mapping, "id")
	path := "/api/account/note-migrations/" + id + "/accept"
	for name, mutate := range map[string]func(*State){
		"expired": func(s *State) {
			r := legacyNoteMapping(s, id).Review
			created := time.Now().UTC().Add(-10 * time.Minute)
			r.CreatedAt, r.ExpiresAt = created.Format(time.RFC3339Nano), created.Add(legacyNoteReviewLifetime).Format(time.RFC3339Nano)
		},
		"email": func(s *State) {
			find(s, "users", legacyNoteMapping(s, id).TargetUserID)["email"] = "changed@example.test"
		},
		"user-entity": func(s *State) {
			find(s, "users", legacyNoteMapping(s, id).TargetUserID)["createdAt"] = "2000-01-01T00:00:00Z"
		},
		"member-entity": func(s *State) { find(s, "members", str(member, "id"))["createdAt"] = "2000-01-01T00:00:00Z" },
		"note-entity":   func(s *State) { find(s, "notes", str(note, "id"))["createdAt"] = "2000-01-01T00:00:00Z" },
		"role": func(s *State) {
			u := find(s, "users", legacyNoteMapping(s, id).TargetUserID)
			u["role"], u["memberId"] = "member", member["id"]
		},
		"generation": func(s *State) {
			id := legacyNoteMapping(s, id).TargetUserID
			value := s.Security[id]
			value.Generation++
			s.Security[id] = value
		},
	} {
		t.Run(name, func(t *testing.T) {
			reset(t)
			if err := admin.a.store.Update(func(s *State) error { mutate(s); return nil }); err != nil {
				t.Fatal(err)
			}
			before := admin.a.store.Snapshot()
			if w := viewer.request("POST", path, legacyNotesAcceptBody(review)); w.Code < 400 {
				t.Fatal("obsolete review was accepted", name)
			}
			after := admin.a.store.Snapshot()
			if !reflect.DeepEqual(before.NoteNotifications, after.NoteNotifications) || !reflect.DeepEqual(before.LegacyNotes, after.LegacyNotes) || !reflect.DeepEqual(before.Outbox, after.Outbox) {
				t.Fatal("failed acceptance partially mutated state")
			}
		})
	}
	reset(t)
	noteTestPreference(t, viewer, "all", nil, nil)
	if w := viewer.request("POST", path, legacyNotesAcceptBody(review)); w.Code != 409 {
		t.Fatal("concurrent preference edit was overwritten")
	}
	reset(t)
	badToken := legacyNotesAcceptBody(review)
	badToken["reviewToken"] = strings.Repeat("x", 43)
	if w := viewer.request("POST", path, badToken); w.Code != 409 {
		t.Fatal("unreviewed token accepted")
	}
	missingAcknowledgement := legacyNotesAcceptBody(review)
	missingAcknowledgement["acknowledge"] = false
	if w := viewer.request("POST", path, missingAcknowledgement); w.Code != 400 {
		t.Fatal("migration accepted without explicit acknowledgement")
	}
	admin.ok("POST", "/api/notes/legacy/mappings/"+id+"/cancel", Record{"revision": review["mapping"].(map[string]any)["revision"]})
	if w := viewer.request("POST", path, legacyNotesAcceptBody(review)); w.Code != 409 {
		t.Fatal("admin cancellation did not invalidate the review")
	}
	reset(t)
	otherSession := docLoginClient(t, admin.a, "legacy-note-viewer@example.test", noteTestPassword)
	if w := otherSession.request("POST", path, legacyNotesAcceptBody(review)); w.Code != 409 {
		t.Fatal("review token transferred to another session")
	}
	reset(t)
	newMapping := Record(review["mapping"].(map[string]any))
	newReview := legacyNotesReview(t, viewer, newMapping, nil, nil, "")
	if newReview["after"].(map[string]any)["mode"] != "none" {
		t.Fatal("omitted selection adopted old source all preference")
	}
	if w := viewer.request("POST", path, legacyNotesAcceptBody(review)); w.Code != 409 {
		t.Fatal("new review did not invalidate old token")
	}
	reset(t)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- viewer.request("POST", path, legacyNotesAcceptBody(review)).Code }()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent acceptance had more than one winner", counts)
	}
}

func TestLegacyNotesSlowBodiesRecheckSessionAndPreserveAtomicity(t *testing.T) {
	admin, viewer, member, note, archive := legacyNotesFixture(t)
	mapping := legacyNotesMapping(t, admin, viewer, archive)
	review := legacyNotesReview(t, viewer, mapping, member, note, "watched")
	raw, _ := json.Marshal(legacyNotesAcceptBody(review))
	before := admin.a.store.Snapshot()
	w := legacyNotesBodyRequest(viewer, "POST", "/api/account/note-migrations/"+str(mapping, "id")+"/accept", raw, func() {
		viewer.ok("POST", "/api/logout", nil)
	})
	if w.Code != 401 || !reflect.DeepEqual(before.LegacyNotes, admin.a.store.Snapshot().LegacyNotes) {
		t.Fatal("logged-out in-flight acceptance committed", w.Code)
	}
	archive.Warnings = append(archive.Warnings, "another source snapshot")
	raw, err := migrate.EncodeNoteArchive(*archive)
	if err != nil {
		t.Fatal(err)
	}
	w = legacyNotesBodyRequest(admin, "POST", "/api/import/note-archive", raw, func() { admin.ok("POST", "/api/logout", nil) })
	if w.Code != 403 || len(admin.a.store.Snapshot().LegacyNotes.Archives) != 1 {
		t.Fatal("logged-out archive import committed", w.Code)
	}
}

func TestLegacyNotesRestartKeepsSourcesButInvalidatesSessionReview(t *testing.T) {
	admin, viewer, member, note, archive := legacyNotesFixture(t)
	mapping := legacyNotesMapping(t, admin, viewer, archive)
	review := legacyNotesReview(t, viewer, mapping, member, note, "watched")
	directory := filepath.Dir(admin.a.store.path)
	before := admin.a.store.Snapshot()
	raw, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil || bytes.Contains(raw, []byte(str(review, "reviewToken"))) {
		t.Fatal("persisted source state contains the bearer review token", err)
	}
	if err := admin.a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Config{DataDir: directory, BirdBinary: "/nonexistent/bird"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after := reopened.store.Snapshot()
	if !reflect.DeepEqual(before.LegacyNotes, after.LegacyNotes) || !reflect.DeepEqual(before.NoteNotifications, after.NoteNotifications) || !reflect.DeepEqual(before.Outbox, after.Outbox) {
		t.Fatal("restart changed source values, pending mapping, or current note state")
	}
	oldSession := &client{a: reopened, t: t, cookie: viewer.cookie, csrf: viewer.csrf}
	path := "/api/account/note-migrations/" + str(mapping, "id") + "/accept"
	if w := oldSession.request("POST", path, legacyNotesAcceptBody(review)); w.Code != 401 {
		t.Fatal("restart recovered an old browser session", w.Code)
	}
	newSession := docLoginClient(t, reopened, "legacy-note-viewer@example.test", noteTestPassword)
	if w := newSession.request("POST", path, legacyNotesAcceptBody(review)); w.Code != 409 {
		t.Fatal("restored review was accepted by a new session", w.Code)
	}
	items := newSession.ok("GET", "/api/account/note-migrations", nil)["items"].([]any)
	currentMapping := Record(items[0].(map[string]any)["mapping"].(map[string]any))
	newReview := legacyNotesReview(t, newSession, currentMapping, member, note, "watched")
	newSession.ok("POST", path, legacyNotesAcceptBody(newReview))
}
