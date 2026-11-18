package app

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

const legacyMailingTestSourceID = "9007199254740993"

func legacyMailingTestArchive(t *testing.T) ([]byte, *migrate.MailingArchive) {
	t.Helper()
	a := migrate.MailingArchive{Version: 1, Kind: migrate.MailingArchiveKind, Source: "ixp-manager-legacy", Access: "admin", Users: []migrate.MailingArchiveUser{{LegacyID: legacyMailingTestSourceID, Facts: migrate.NoteArchiveFacts{"id": json.RawMessage(legacyMailingTestSourceID), "email": json.RawMessage(`"old-source@example.test"`)}}}}
	for key, value := range map[string]string{"in": `"1"`, "out": `"0"`, "unknown": `"UNKNOWN-PRIVATE-SOURCE"`} {
		a.Choices = append(a.Choices, migrate.MailingArchiveChoice{ID: "user-" + legacyMailingTestSourceID + "-" + key, SourceUserID: legacyMailingTestSourceID, SourceKey: key, Format: "user.prefs", Value: json.RawMessage(value)})
	}
	raw, err := migrate.EncodeMailingArchive(a)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := migrate.ReadMailingArchive(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return raw, validated
}
func legacyMailingFixture(t *testing.T) (*client, *client, []Record, *migrate.MailingArchive) {
	t.Helper()
	admin, _ := setupClient(t)
	member := admin.create("members", Record{"name": "Source target member", "asn": 64503})
	target := noteTestUser(t, admin, "member", "current-mailing@example.test", member)
	lists := []Record{mailingTestList(admin, "in"), mailingTestList(admin, "out"), mailingTestList(admin, "unknown")}
	raw, archive := legacyMailingTestArchive(t)
	responseRecord(t, legacyNotesBodyRequest(admin, "POST", "/api/import/mailing-archive", raw, nil))
	return admin, target, lists, archive
}
func legacyMailingTestMap(t *testing.T, admin, target *client, lists []Record, archive *migrate.MailingArchive) Record {
	t.Helper()
	s := admin.a.store.Snapshot()
	u := find(&s, "users", target.ok("GET", "/api/session", nil)["user"].(map[string]any)["id"].(string))
	bindings := []Record{}
	for _, l := range lists {
		bindings = append(bindings, Record{"sourceKey": l["key"], "listId": l["id"], "listCreatedAt": l["createdAt"], "listRevision": l["revision"]})
	}
	return Record(admin.ok("POST", "/api/mailing-lists/legacy/mappings", Record{"archiveChecksum": archive.Checksum, "sourceUserId": legacyMailingTestSourceID, "targetUserId": u["id"], "targetUserCreatedAt": u["createdAt"], "lists": bindings})["mapping"].(map[string]any))
}
func legacyMailingTestReviewBody(t *testing.T, target *client, mapping Record, keys ...string) Record {
	t.Helper()
	view := target.ok("GET", "/api/account/mailing-migrations", nil)
	selected := []Record{}
	for _, item := range view["items"].([]any) {
		entry := item.(map[string]any)
		if entry["mapping"].(map[string]any)["id"] != mapping["id"] {
			continue
		}
		for _, raw := range entry["choices"].([]any) {
			c := raw.(map[string]any)
			if contains(keys, c["sourceKey"].(string)) {
				selected = append(selected, Record{"sourceKey": c["sourceKey"], "listRevision": c["listRevision"], "preferenceRevision": c["preferenceRevision"]})
			}
		}
	}
	return Record{"revision": mapping["revision"], "email": view["email"], "choices": selected}
}
func legacyMailingTestReview(t *testing.T, target *client, mapping Record, keys ...string) Record {
	t.Helper()
	return target.ok("POST", "/api/account/mailing-migrations/"+str(mapping, "id")+"/review", legacyMailingTestReviewBody(t, target, mapping, keys...))
}
func legacyMailingTestAcceptBody(review Record) Record {
	return Record{"revision": review["mapping"].(map[string]any)["revision"], "reviewToken": review["reviewToken"], "acknowledge": true}
}
func TestLegacyMailingExplicitSelfAcceptanceOptOutAndPrivateSource(t *testing.T) {
	admin, target, lists, archive := legacyMailingFixture(t)
	if got := target.ok("GET", "/api/account/mailing-migrations", nil); len(got["items"].([]any)) != 0 {
		t.Fatal("email match or import granted source automatically")
	}
	matchingEmail := noteTestUser(t, admin, "viewer", "old-source@example.test", nil)
	if len(matchingEmail.ok("GET", "/api/account/mailing-migrations", nil)["items"].([]any)) != 0 {
		t.Fatal("source email equality implicitly mapped a current account")
	}
	rawSource, _ := migrate.EncodeMailingArchive(*archive)
	if w := legacyNotesBodyRequest(admin, "POST", "/api/import/mailing-archive", rawSource, nil); w.Code != 409 {
		t.Fatal("duplicate archive import was accepted", w.Code)
	}
	mapping := legacyMailingTestMap(t, admin, target, lists, archive)
	for _, path := range []string{"/api/mailing-lists/legacy", "/api/mailing-lists/legacy/archives/" + archive.Checksum, "/api/mailing-lists/legacy/archives/" + archive.Checksum + "?download=1"} {
		if w := target.request("GET", path, nil); w.Code != 403 {
			t.Fatal("member read private source archive", path, w.Code)
		}
	}
	self := target.request("GET", "/api/account/mailing-migrations", nil)
	if self.Code != 200 || strings.Contains(self.Body.String(), "UNKNOWN-PRIVATE-SOURCE") || strings.Contains(self.Body.String(), "old-source@example.test") || !strings.Contains(self.Body.String(), legacyMailingTestSourceID) {
		t.Fatal("unsafe self source view", self.Body.String())
	}
	if w := target.request("POST", "/api/account/mailing-migrations/"+str(mapping, "id")+"/review", legacyMailingTestReviewBody(t, target, mapping, "unknown")); w.Code != 409 {
		t.Fatal("unknown source value became consent", w.Code)
	}
	noCSRF := *target
	noCSRF.csrf = ""
	if w := noCSRF.request("POST", "/api/account/mailing-migrations/"+str(mapping, "id")+"/review", legacyMailingTestReviewBody(t, target, mapping, "in")); w.Code != 403 {
		t.Fatal("review did not require CSRF")
	}
	review := legacyMailingTestReview(t, target, mapping, "in", "out")
	if state := admin.a.store.Snapshot(); len(state.MailingLists.Preferences) != 0 || len(state.Outbox.Entries) != 0 {
		t.Fatal("import/map/review changed current choices or queued mail")
	}
	body := legacyMailingTestAcceptBody(review)
	path := "/api/account/mailing-migrations/" + str(mapping, "id") + "/accept"
	if w := admin.request("POST", path, body); w.Code != 404 {
		t.Fatal("admin accepted another account's choice", w.Code)
	}
	accepted := target.ok("POST", path, body)
	if accepted["externalApplied"] != false {
		t.Fatal("acceptance claimed external mailing state")
	}
	if w := target.request("POST", path, body); w.Code != 409 {
		t.Fatal("review token replay accepted")
	}
	in := admin.ok("GET", "/api/mailing-lists/"+str(lists[0], "id")+"/export", nil)
	out := admin.ok("GET", "/api/mailing-lists/"+str(lists[1], "id")+"/export", nil)
	if !reflect.DeepEqual(in["subscribed"], []any{"current-mailing@example.test"}) || !reflect.DeepEqual(out["unsubscribed"], []any{"current-mailing@example.test"}) {
		t.Fatal("known opt-in/out or current address lost", in, out)
	}
	if strings.Contains(string(admin.request("GET", "/api/export", nil).Body.Bytes()), "legacyMailing") {
		t.Fatal("ordinary inventory export contained private archive")
	}
	download := admin.request("GET", "/api/mailing-lists/legacy/archives/"+archive.Checksum+"?download=1", nil)
	copy, err := migrate.ReadMailingArchive(bytes.NewReader(download.Body.Bytes()))
	if err != nil || !reflect.DeepEqual(copy, archive) {
		t.Fatal("private source evidence changed", err)
	}
	if len(target.ok("GET", "/api/account/mailing-migrations", nil)["items"].([]any)) != 0 {
		t.Fatal("accepted selection did not consume mapping")
	}
	// Unselected keys remain available for a later explicit mapping.
	legacyMailingTestMap(t, admin, target, lists[2:], archive)
	user := target.ok("GET", "/api/session", nil)["user"].(map[string]any)
	snapshot := admin.a.store.Snapshot()
	entity := find(&snapshot, "users", user["id"].(string))
	if w := admin.request("POST", "/api/mailing-lists/legacy/mappings", Record{"archiveChecksum": archive.Checksum, "sourceUserId": legacyMailingTestSourceID, "targetUserId": entity["id"], "targetUserCreatedAt": entity["createdAt"], "lists": []Record{{"sourceKey": "in", "listId": lists[0]["id"], "listCreatedAt": lists[0]["createdAt"], "listRevision": lists[0]["revision"]}}}); w.Code != 409 {
		t.Fatal("accepted source key was mapped twice", w.Code)
	}
}
func TestLegacyMailingReviewRejectsListEmailAndNewPreferenceChanges(t *testing.T) {
	for _, change := range []string{"list", "list-identity", "email", "preference", "recreated-user", "expired"} {
		t.Run(change, func(t *testing.T) {
			admin, target, lists, archive := legacyMailingFixture(t)
			mapping := legacyMailingTestMap(t, admin, target, lists[:1], archive)
			review := legacyMailingTestReview(t, target, mapping, "in")
			session := target.ok("GET", "/api/session", nil)["user"].(map[string]any)
			switch change {
			case "list":
				l := lists[0]
				admin.ok("PUT", "/api/mailing-lists/"+str(l, "id"), Record{"revision": l["revision"], "name": "Renamed after review", "description": "", "archiveUrl": "", "enabled": true})
			case "preference":
				target.ok("PUT", "/api/account/mailing-lists/"+str(lists[0], "id"), mailingTestChoice(t, target, str(lists[0], "id"), false))
			default:
				if err := admin.a.store.Update(func(s *State) error {
					u := find(s, "users", session["id"].(string))
					switch change {
					case "list-identity":
						mailingList(s, str(lists[0], "id")).Email = "retargeted@lists.example.test"
					case "email":
						u["email"] = "new-current@example.test"
					case "recreated-user":
						u["createdAt"] = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
					case "expired":
						r := legacyMailingMapping(s, str(mapping, "id")).Review
						created := time.Now().Add(-10 * time.Minute).UTC()
						r.CreatedAt = created.Format(time.RFC3339Nano)
						r.ExpiresAt = created.Add(legacyMailingReviewLifetime).Format(time.RFC3339Nano)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			beforeAttempt := admin.a.store.Snapshot()
			w := target.request("POST", "/api/account/mailing-migrations/"+str(mapping, "id")+"/accept", legacyMailingTestAcceptBody(review))
			if change == "recreated-user" {
				if w.Code != 401 {
					t.Fatal("old entity's browser cookie was not rejected at authentication", w.Code)
				}
				// Authenticate the replacement entity for real. Matching the old
				// ID/email must still not transfer its source mapping or review.
				replacement := docLoginClient(t, admin.a, "current-mailing@example.test", noteTestPassword)
				current := replacement.ok("GET", "/api/session", nil)["user"].(map[string]any)
				if current["id"] != session["id"] || str(find(&beforeAttempt, "users", session["id"].(string)), "createdAt") == mapping["targetUserCreatedAt"] {
					t.Fatal("fixture did not authenticate the same ID as a new account entity")
				}
				w = replacement.request("POST", "/api/account/mailing-migrations/"+str(mapping, "id")+"/accept", legacyMailingTestAcceptBody(review))
				if w.Code != 404 {
					t.Fatal("replacement entity inherited the old mapping or review", w.Code)
				}
				afterAttempt := admin.a.store.Snapshot()
				if !reflect.DeepEqual(beforeAttempt.MailingLists, afterAttempt.MailingLists) {
					t.Fatal("rejected old/new entity replay changed mailing preferences")
				}
				return
			}
			if w.Code != 409 && w.Code != 404 && w.Code != 403 {
				t.Fatal("changed target accepted stale review", w.Code, w.Body.String())
			}
			s := admin.a.store.Snapshot()
			for _, p := range s.MailingLists.Preferences {
				if p.Subscribed {
					t.Fatal("stale review overwrote current preference")
				}
			}
		})
	}
}
func TestLegacyMailingRetargetNewerChoiceAndRetiredAddress(t *testing.T) {
	admin, target, lists, archive := legacyMailingFixture(t)
	// A stale address is replaceable only after explicit review of the current
	// account address, retaining the old withdrawal intent.
	target.ok("PUT", "/api/account/mailing-lists/"+str(lists[0], "id"), mailingTestChoice(t, target, str(lists[0], "id"), true))
	user := target.ok("GET", "/api/session", nil)["user"].(map[string]any)
	if err := admin.a.store.Update(func(s *State) error {
		find(s, "users", user["id"].(string))["email"] = "replacement@example.test"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mapping := legacyMailingTestMap(t, admin, target, lists[:1], archive)
	review := legacyMailingTestReview(t, target, mapping, "in")
	current := Record(review["mapping"].(map[string]any))
	if w := admin.request("POST", "/api/mailing-lists/legacy/mappings/"+str(mapping, "id")+"/cancel", Record{"revision": mapping["revision"]}); w.Code != 409 {
		t.Fatal("stale mapping revision cancelled a newer review")
	}
	admin.ok("POST", "/api/mailing-lists/legacy/mappings/"+str(mapping, "id")+"/cancel", Record{"revision": current["revision"]})
	if w := target.request("POST", "/api/account/mailing-migrations/"+str(mapping, "id")+"/accept", legacyMailingTestAcceptBody(review)); w.Code != 409 {
		t.Fatal("cancelled mapping token accepted")
	}
	other := noteTestUser(t, admin, "viewer", "different-target@example.test", nil)
	retarget := legacyMailingTestMap(t, admin, other, lists[:1], archive)
	if w := target.request("POST", "/api/account/mailing-migrations/"+str(retarget, "id")+"/review", Record{"revision": 1, "email": "replacement@example.test", "choices": []Record{{"sourceKey": "in", "listRevision": lists[0]["revision"], "preferenceRevision": 0}}}); w.Code != 404 {
		t.Fatal("former target retained mapping authority")
	}
	admin.ok("POST", "/api/mailing-lists/legacy/mappings/"+str(retarget, "id")+"/cancel", Record{"revision": retarget["revision"]})
	final := legacyMailingTestMap(t, admin, target, lists[:1], archive)
	accepted := legacyMailingTestReview(t, target, final, "in")
	target.ok("POST", "/api/account/mailing-migrations/"+str(final, "id")+"/accept", legacyMailingTestAcceptBody(accepted))
	exported := admin.ok("GET", "/api/mailing-lists/"+str(lists[0], "id")+"/export", nil)
	if !reflect.DeepEqual(exported["subscribed"], []any{"replacement@example.test"}) || !reflect.DeepEqual(exported["unsubscribed"], []any{"current-mailing@example.test"}) {
		t.Fatal("new address inherited consent or retired address was lost", exported)
	}
	// A current-email explicit choice is never overwritten, even when made
	// before the administrator proposes the source mapping.
	target.ok("PUT", "/api/account/mailing-lists/"+str(lists[1], "id"), mailingTestChoice(t, target, str(lists[1], "id"), true))
	blocked := legacyMailingTestMap(t, admin, target, lists[1:2], archive)
	if w := target.request("POST", "/api/account/mailing-migrations/"+str(blocked, "id")+"/review", legacyMailingTestReviewBody(t, target, blocked, "out")); w.Code != 409 {
		t.Fatal("migration overwrote existing explicit current preference")
	}
}
func TestLegacyMailingAuthorityDuringImportAndConcurrentAcceptance(t *testing.T) {
	admin, target, lists, archive := legacyMailingFixture(t)
	tok := admin.ok("POST", "/api/account/tokens", Record{"name": "Mailing export", "currentPassword": "very-long-test-password", "scopes": []string{"mailing-lists:read"}})
	for _, path := range []string{"/api/mailing-lists/legacy", "/api/account/mailing-migrations", "/api/mailing-lists/legacy/archives/" + archive.Checksum} {
		if w := bearerRequest(admin.a, str(tok, "token"), "GET", path, nil); w.Code != 403 {
			t.Fatal("export token accessed migration data", w.Code)
		}
	}
	mapping := legacyMailingTestMap(t, admin, target, lists[:1], archive)
	review := legacyMailingTestReview(t, target, mapping, "in")
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- target.request("POST", "/api/account/mailing-migrations/"+str(mapping, "id")+"/accept", legacyMailingTestAcceptBody(review)).Code
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent acceptance did not consume once", counts)
	}
	raw, _ := legacyMailingTestArchive(t)
	adminID := accountID(admin.a)
	w := legacyNotesBodyRequest(admin, "POST", "/api/import/mailing-archive", raw, func() {
		if err := admin.a.store.Update(func(s *State) error { find(s, "users", adminID)["role"] = "viewer"; return nil }); err != nil {
			t.Fatal(err)
		}
	})
	if w.Code != 403 {
		t.Fatal("administrator demoted while reading imported source", w.Code)
	}
}
