package app

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConsoleSourceTimestampsPreservedOnImportEditExportAndRoundtrip(t *testing.T) {
	c, fixture := consoleContractFixture(t)
	const serverKind, connectionKind = "console-servers", "console-connections"
	const serverID, connectionID = "legacy-console-servers-801", "legacy-console-connections-9007199254740993"
	const wall = "2020-10-25 01:30:59.000007"
	const offset = "2020-10-25T01:30:59.100+01:00"
	times := map[string]map[string]any{
		serverKind:     {"sourceId": "801", "createdAt": wall, "updatedAt": nil},
		connectionKind: {"sourceId": "9007199254740993", "updatedAt": offset},
	}
	ids := map[string]string{serverKind: serverID, connectionKind: connectionID}
	base := map[string]Record{
		serverKind:     {"name": "Imported console with source history", "locationId": fixture.location, "hostname": "console-source.example.test"},
		connectionKind: {"name": "Imported serial with source history", "consoleServerId": serverID, "memberId": fixture.member, "portLabel": "01"},
	}
	resources := Record{}
	for kind, source := range times {
		row := Record{"id": ids[kind], "legacyTimestamps": source, "createdAt": wall, "updatedAt": offset}
		for key, value := range base[kind] {
			row[key] = value
		}
		resources[kind] = []Record{row}
	}
	payload := Record{"version": 1, "resources": resources}
	if checked := c.ok(http.MethodPost, "/api/import/preview", payload); number(checked, "checked") != 2 {
		t.Fatal("import preview did not validate both timestamp facts")
	}
	c.ok(http.MethodPost, "/api/import", payload)
	current := map[string]Record{}
	for kind, source := range times {
		path := "/api/resources/" + kind + "/" + ids[kind]
		before := c.ok(http.MethodGet, path, nil)
		epoch := str(before, "createdAt")
		if _, err := time.Parse(time.RFC3339Nano, epoch); err != nil || epoch == offset || epoch == wall || epoch != before["updatedAt"] ||
			!reflect.DeepEqual(source, before["legacyTimestamps"]) {
			t.Fatal("import confused current Go timestamp with original evidence")
		}
		edit := base[kind]
		edit["name"] = "Edited live " + kind
		after := c.ok(http.MethodPut, path, edit)
		if !reflect.DeepEqual(after["legacyTimestamps"], source) || after["createdAt"] != before["createdAt"] || after["updatedAt"] == before["updatedAt"] {
			t.Fatal("ordinary edit did not preserve historical facts and current identity")
		}
		edit["legacyTimestamps"] = source
		current[kind] = c.ok(http.MethodPut, path, edit)
		if !reflect.DeepEqual(current[kind]["legacyTimestamps"], source) {
			t.Fatal("exact source metadata echo did not preserve evidence")
		}
		for _, replacement := range []any{nil, "", Record{"sourceId": source["sourceId"], "createdAt": "2010-01-02 03:04:05"}} {
			edit["legacyTimestamps"] = replacement
			if w := c.request(http.MethodPut, path, edit); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "read-only") {
				t.Fatalf("ordinary edit rewrote imported source facts: HTTP %d %s", w.Code, w.Body.String())
			}
		}
		delete(edit, "legacyTimestamps")
	}
	if w := c.request(http.MethodPost, "/api/import", payload); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "duplicate id") {
		t.Fatalf("repeat import replaced existing current identity: HTTP %d %s", w.Code, w.Body.String())
	}
	export := c.ok(http.MethodGet, "/api/export", nil)
	exportedResources := export["resources"].(map[string]any)
	for kind, source := range times {
		var got Record
		for _, raw := range exportedResources[kind].([]any) {
			row := Record(raw.(map[string]any))
			if str(row, "id") == ids[kind] {
				got = row
			}
		}
		if !reflect.DeepEqual(got["legacyTimestamps"], source) {
			t.Fatalf("inventory export lost scoped history for %s", kind)
		}
	}
	other, _ := setupClient(t)
	other.ok(http.MethodPost, "/api/import", export)
	for kind, source := range times {
		loaded := other.ok(http.MethodGet, "/api/resources/"+kind+"/"+ids[kind], nil)
		if !reflect.DeepEqual(loaded["legacyTimestamps"], source) || loaded["createdAt"] == current[kind]["createdAt"] {
			t.Fatalf("round-trip did not preserve facts and mint a fresh current entity epoch for %s", kind)
		}
	}
}

func TestConsoleSourceTimestampsRejectGraftedAndMalformedImportFacts(t *testing.T) {
	c, fixture := consoleContractFixture(t)
	const kind = "console-servers"
	const path = "/api/resources/" + kind
	const good = "2024-02-29T03:04:05Z"
	for _, tc := range []struct {
		name  string
		id    string
		facts any
	}{
		{"native ID", "native-console", Record{"sourceId": "802", "createdAt": good}},
		{"copied to another legacy object", "legacy-console-servers-803", Record{"sourceId": "802", "createdAt": good}},
		{"wrong parent kind in ID", "legacy-console-connections-802", Record{"sourceId": "802", "createdAt": good}},
		{"ambiguous legacy ID", "legacy-console-servers-0802", Record{"sourceId": "0802", "createdAt": good}},
		{"source missing", "legacy-console-servers-802", Record{"createdAt": good}},
		{"no time facts", "legacy-console-servers-802", Record{"sourceId": "802"}},
		{"time numeric", "legacy-console-servers-802", Record{"sourceId": "802", "updatedAt": 964872342}},
		{"time nested", "legacy-console-servers-802", Record{"sourceId": "802", "updatedAt": Record{"time": good}}},
		{"invalid calendar", "legacy-console-servers-802", Record{"sourceId": "802", "createdAt": "2023-02-29 03:04:05"}},
		{"whole object null", "legacy-console-servers-802", nil},
		{"unknown key", "legacy-console-servers-802", Record{"sourceId": "802", "createdAt": good, "timezone": "guess"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := Record{"version": 1, "resources": Record{kind: []Record{{"id": tc.id, "name": "Invalid time fixture", "locationId": fixture.location, "hostname": "bad-time.example.test", "legacyTimestamps": tc.facts}}}}
			for _, endpoint := range []string{"/api/import/preview", "/api/import"} {
				if w := c.request(http.MethodPost, endpoint, body); w.Code != http.StatusBadRequest {
					t.Fatalf("%s accepted invalid timestamp provenance: HTTP %d %s", endpoint, w.Code, w.Body.String())
				}
			}
			if w := c.request(http.MethodGet, path+"/"+tc.id, nil); w.Code != http.StatusNotFound {
				t.Fatal("failed import left a partial resource")
			}
		})
	}
	if w := c.request(http.MethodPost, path, Record{"name": "Cannot fabricate legacy provenance", "locationId": fixture.location,
		"hostname": "nonlegacy.example.test", "legacyTimestamps": Record{"sourceId": "802", "createdAt": good}}); w.Code != http.StatusBadRequest {
		t.Fatalf("normal create accepted source-only facts: HTTP %d", w.Code)
	}
	if w := c.request(http.MethodPost, "/api/resources/locations", Record{"name": "Outside source scope", "legacyTimestamps": Record{"sourceId": "802", "createdAt": good}}); w.Code != http.StatusBadRequest {
		t.Fatalf("source timestamps accepted for a different resource: HTTP %d", w.Code)
	}
}
