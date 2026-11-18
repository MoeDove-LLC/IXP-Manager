package app

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func mailingTestList(c *client, key string) Record {
	return c.ok("POST", "/api/mailing-lists", Record{"key": key, "name": "Member discussion", "description": "Opt-in exchange discussion", "email": key + "@lists.example.test", "archiveUrl": "https://lists.example.test/archive/" + key, "enabled": true})
}
func mailingTestCopy(in Record) Record {
	out := Record{}
	for key, value := range in {
		out[key] = value
	}
	return out
}
func mailingTestChoice(t *testing.T, c *client, id string, subscribed bool) Record {
	t.Helper()
	view := c.ok("GET", "/api/account/mailing-lists", nil)
	for _, raw := range view["items"].([]any) {
		item := raw.(map[string]any)
		if item["id"] == id {
			return Record{"listRevision": item["listRevision"], "preferenceRevision": item["preferenceRevision"], "email": view["email"], "subscribed": subscribed}
		}
	}
	t.Fatal("fixture mailing list unavailable")
	return nil
}

func TestMailingSelfOnlyCASAndExplicitExportAuthority(t *testing.T) {
	c, _ := setupClient(t)
	list := mailingTestList(c, "members")
	id := str(list, "id")
	member := c.create("members", Record{"name": "Member", "asn": 64505, "status": "active"})
	u := noteTestUser(t, c, "member", "reader@example.test", member)
	if got := u.ok("GET", "/api/account/mailing-lists", nil); got["synchronization"] != "external-not-observed" || got["items"].([]any)[0].(map[string]any)["desired"] != "unset" {
		t.Fatal("default preference invented a subscription or observed synchronization")
	}
	for _, path := range []string{"/api/mailing-lists", "/api/mailing-lists/" + id + "/export", "/api/v4/mailing-list/subscribers/json/members"} {
		if w := u.request("GET", path, nil); w.Code != 403 {
			t.Fatalf("member reached admin list data: %s %d", path, w.Code)
		}
	}
	choice := mailingTestChoice(t, u, id, true)
	copy := mailingTestCopy(choice)
	copy["userId"] = accountID(c.a)
	if w := u.request("PUT", "/api/account/mailing-lists/"+id, copy); w.Code != 400 {
		t.Fatal("caller could select another subscription owner")
	}
	noCSRF := *u
	noCSRF.csrf = ""
	if w := noCSRF.request("PUT", "/api/account/mailing-lists/"+id, choice); w.Code != 403 {
		t.Fatal("subscription write omitted CSRF")
	}
	u.ok("PUT", "/api/account/mailing-lists/"+id, choice)
	if w := u.request("PUT", "/api/account/mailing-lists/"+id, choice); w.Code != 409 {
		t.Fatal("old preference revision replayed")
	}
	exported := c.ok("GET", "/api/mailing-lists/"+id+"/export", nil)
	if exported["externalApplied"] != false || !reflect.DeepEqual(exported["subscribed"], []any{"reader@example.test"}) || len(exported["unsubscribed"].([]any)) != 0 {
		t.Fatal("export included unset accounts or claimed applied")
	}
	for _, role := range []string{"operator", "viewer", "member"} {
		if contains(availableScopes(role), "mailing-lists:read") {
			t.Fatal("non-admin could mint subscription export scope")
		}
	}
	ordinary := c.ok("POST", "/api/account/tokens", Record{"name": "Ordinary export", "currentPassword": "very-long-test-password", "scopes": []string{"exports:read"}})
	export := c.ok("POST", "/api/account/tokens", Record{"name": "Mailing export", "currentPassword": "very-long-test-password", "scopes": []string{"mailing-lists:read"}})
	for _, path := range []string{"/api/mailing-lists/" + id + "/export", "/api/v4/mailing-list/subscribers/json/members", "/api/v4/mailing-list/subscribers/members"} {
		if w := bearerRequest(c.a, str(ordinary, "token"), "GET", path, nil); w.Code != 403 {
			t.Fatal("ordinary exports scope gained private subscription data")
		}
		if w := bearerRequest(c.a, str(export, "token"), "GET", path, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "reader@example.test") {
			t.Fatal("dedicated token did not export explicitly subscribed address", w.Code)
		}
	}
	for _, path := range []string{"/api/mailing-lists", "/api/account/mailing-lists"} {
		if w := bearerRequest(c.a, str(export, "token"), "GET", path, nil); w.Code != 403 {
			t.Fatal("export token reached configuration or personal preference API")
		}
	}
	if w := bearerRequest(c.a, str(export, "token"), "PUT", "/api/account/mailing-lists/"+id, choice); w.Code != 403 {
		t.Fatal("export token changed a preference")
	}
	u.ok("PUT", "/api/account/mailing-lists/"+id, mailingTestChoice(t, u, id, false))
	w := c.request("GET", "/api/v4/mailing-list/unsubscribed/members", nil)
	if w.Code != 200 || w.Body.String() != "reader@example.test\n" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatal("legacy unsubscribe representation incorrect")
	}
	if body := c.ok("GET", "/api/export", nil); strings.Contains(fmt.Sprint(body), "mailingLists") {
		t.Fatal("ordinary inventory export included private subscription state")
	}
}

func TestMailingDefinitionChangeAndConcurrentChoice(t *testing.T) {
	c, _ := setupClient(t)
	list := mailingTestList(c, "operations")
	id := str(list, "id")
	choice := mailingTestChoice(t, c, id, true)
	var group sync.WaitGroup
	statuses := make(chan int, 2)
	for _, subscribed := range []bool{true, false} {
		group.Add(1)
		go func(subscribed bool) {
			defer group.Done()
			body := mailingTestCopy(choice)
			body["subscribed"] = subscribed
			statuses <- c.request("PUT", "/api/account/mailing-lists/"+id, body).Code
		}(subscribed)
	}
	group.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent choices did not have one winner", counts)
	}
	oldChoice := mailingTestChoice(t, c, id, true)
	update := Record{"revision": list["revision"], "name": "Paused list", "description": "Review needed", "archiveUrl": "", "enabled": false}
	paused := c.ok("PUT", "/api/mailing-lists/"+id, update)
	if w := c.request("PUT", "/api/account/mailing-lists/"+id, oldChoice); w.Code != 409 {
		t.Fatal("stale list definition did not invalidate user review")
	}
	if w := c.request("PUT", "/api/account/mailing-lists/"+id, mailingTestChoice(t, c, id, true)); w.Code != 400 {
		t.Fatal("disabled list accepted subscription")
	}
	c.ok("PUT", "/api/account/mailing-lists/"+id, mailingTestChoice(t, c, id, false))
	if w := c.request("GET", "/api/mailing-lists/"+id+"/export", nil); w.Code != 409 {
		t.Fatal("disabled list emitted a misleading synchronization snapshot")
	}
	if w := c.request("PUT", "/api/mailing-lists/"+id, update); w.Code != 409 {
		t.Fatal("stale configuration overwrite accepted")
	}
	update["revision"] = paused["revision"]
	update["email"] = "other@example.test"
	if w := c.request("PUT", "/api/mailing-lists/"+id, update); w.Code != 400 {
		t.Fatal("list target silently changed under existing preferences")
	}
	for _, archive := range []string{"javascript:alert(1)", "https://name:password@lists.example.test/private", "//lists.example.test"} {
		if w := c.request("POST", "/api/mailing-lists", Record{"key": "bad", "name": "Bad archive", "email": "bad@example.test", "archiveUrl": archive, "enabled": false}); w.Code != 400 {
			t.Fatal("unsafe archive URL accepted")
		}
	}
}

func TestMailingEmailChangeDeletionAndReusedIdentity(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	user := Record{"id": "owner", "createdAt": now.Format(time.RFC3339Nano), "email": "old@example.test"}
	list := MailingList{ID: "list", Key: "members", Name: "Members", Email: "members@lists.example.test", Enabled: true, Revision: 1, CreatedAt: now.Format(time.RFC3339Nano)}
	s := State{Resources: map[string][]Record{"users": {user}}, MailingLists: &MailingState{Version: 1, Sequence: 1, Lists: []MailingList{list}}}
	if err := saveMailingPreference(&s, list, user, true, 0); err != nil {
		t.Fatal(err)
	}
	p, _ := mailingPreference(&s, list.ID, user)
	user["email"] = "new@example.test"
	view := mailingSelfView(&s, user)["items"].([]Record)[0]
	if view["desired"] != "unset" || view["staleEmail"] != true {
		t.Fatal("email change inherited old consent")
	}
	before, err := exportMailingList(&s, list, now)
	if err != nil || len(before.Subscribed) != 0 || !reflect.DeepEqual(before.Unsubscribed, []string{"old@example.test"}) {
		t.Fatal("changed email kept old subscription or subscribed new email", before, err)
	}
	if err := saveMailingPreference(&s, list, user, true, p.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := exportMailingList(&s, list, now)
	if err != nil || !reflect.DeepEqual(after.Subscribed, []string{"new@example.test"}) || !reflect.DeepEqual(after.Unsubscribed, []string{"old@example.test"}) || after.Checksum == before.Checksum {
		t.Fatal("retired address was lost on new consent", after, err)
	}
	other := Record{"id": "new-owner", "createdAt": now.Add(time.Second).Format(time.RFC3339Nano), "email": "old@example.test"}
	s.Resources["users"] = append(s.Resources["users"], other)
	if err := saveMailingPreference(&s, list, other, true, 0); err != nil {
		t.Fatal(err)
	}
	after, _ = exportMailingList(&s, list, now)
	if len(after.Unsubscribed) != 0 || !reflect.DeepEqual(after.Subscribed, []string{"new@example.test", "old@example.test"}) {
		t.Fatal("old tombstone removed new owner's explicit consent")
	}
	// Same ID with a different entity timestamp is not the same account.
	user["createdAt"] = now.Add(2 * time.Second).Format(time.RFC3339Nano)
	after, _ = exportMailingList(&s, list, now)
	if !reflect.DeepEqual(after.Subscribed, []string{"old@example.test"}) || !reflect.DeepEqual(after.Unsubscribed, []string{"new@example.test"}) {
		t.Fatal("recreated account inherited consent")
	}
	s.Resources["users"] = nil
	after, _ = exportMailingList(&s, list, now)
	if len(after.Subscribed) != 0 || len(after.Unsubscribed) != 2 {
		t.Fatal("deleted users retained subscriptions")
	}
	encoded, _ := json.Marshal(s.MailingLists)
	if strings.Contains(string(encoded), "externalApplied") {
		t.Fatal("persisted preference state fabricated a synchronization receipt")
	}
}
