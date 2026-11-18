package app

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestConsoleConnectionsFilterByMemberAndServer(t *testing.T) {
	c, id := consoleContractFixture(t)
	otherMember := str(c.create("members", Record{"name": "Other console member", "asn": 64502, "status": "active"}), "id")
	server := str(c.create("console-servers", Record{"name": "A", "locationId": id.location, "hostname": "a.example"}), "id")
	otherServer := str(c.create("console-servers", Record{"name": "B", "locationId": id.otherLocation, "hostname": "b.example"}), "id")
	first := c.create("console-connections", Record{"name": "First", "description": "Console endpoint", "consoleServerId": server, "memberId": id.member, "portLabel": "001"})
	c.create("console-connections", Record{"name": "Second", "consoleServerId": otherServer, "memberId": id.member, "portLabel": "001"})
	c.create("console-connections", Record{"name": "Unrelated", "consoleServerId": server, "memberId": otherMember, "portLabel": "002"})

	path := "/api/resources/console-connections?"
	memberQuery := url.Values{"memberId": {id.member}}
	items := c.ok("GET", path+memberQuery.Encode(), nil)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("member filter returned %d connections", len(items))
	}
	memberQuery.Set("consoleServerId", server)
	items = c.ok("GET", path+memberQuery.Encode(), nil)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("combined filter returned %d connections", len(items))
	}
	selected := Record(items[0].(map[string]any))
	if str(selected, "id") != str(first, "id") || str(selected, "locationName") != "Console DC" ||
		str(selected, "description") != "Console endpoint" || str(selected, "portLabel") != "001" {
		t.Fatalf("unexpected console connection: %v", selected)
	}
	if items := c.ok("GET", path+"memberId=unknown", nil)["items"].([]any); len(items) != 0 {
		t.Fatal("unknown member returned unrelated connections")
	}
	if w := c.request("DELETE", "/api/resources/members/"+id.member, nil); w.Code != 400 {
		t.Fatalf("deleted member with console connections: %d %s", w.Code, w.Body.String())
	}

	const email, password = "console-filter-member@example.test", "console-filter-member-password"
	c.create("users", Record{"name": "Console member login", "email": email, "password": password, "role": "member", "memberId": id.member})
	member := docLoginClient(t, c.a, email, password)
	w := member.request("GET", path+memberQuery.Encode(), nil)
	// Member resource lists can be empty or denied according to their access policy.
	if w.Code == 200 {
		var body struct{ Items []Record }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Items) != 0 {
			t.Fatal("member filter exposed staff console connections")
		}
	} else if w.Code != 403 && w.Code != 404 {
		t.Fatalf("unexpected status %d for member console filter", w.Code)
	}
}
