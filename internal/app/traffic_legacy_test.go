package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/traffic"
)

const legacyHTTPPassword = "very-long-test-password"

func legacyHTTPInput(t *testing.T) traffic.LegacyInput {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "traffic", "testdata", "legacy-traffic-daily.json"))
	if err != nil {
		t.Fatal(err)
	}
	return traffic.LegacyInput{Format: traffic.LegacyTableJSON, Source: traffic.LegacySource{Name: "private original source name", Timezone: "Europe/Dublin", Backend: "mrtg", Table: "traffic_daily"}, Content: string(raw)}
}

func legacyHTTPBody(input traffic.LegacyInput, checksum string) Record {
	return Record{"input": input, "expectedChecksum": checksum, "acknowledgeWindows": true, "currentPassword": legacyHTTPPassword}
}

func legacyHTTPProof(t *testing.T, c *client, memberID string) traffic.LegacyInput {
	t.Helper()
	input := legacyHTTPInput(t)
	s := c.a.store.Snapshot()
	input.Owners = []traffic.LegacyOwnerEvidence{{EntityKind: "customer", EntityID: "42", MemberID: memberID, MemberRevision: c.a.trafficMemberRevision(&s, memberID), ValidFrom: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC), ValidTo: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), ReferenceSHA256: strings.Repeat("b", 64), Description: "private historical ownership proof description"}}
	input.Windows = []traffic.LegacyDeclaredWindow{{RecordID: "101", Period: "day", Start: time.Date(2024, 5, 30, 0, 0, 0, 0, time.UTC), End: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), Evidence: "synthetic original extraction window"}}
	return input
}

func legacyHTTPMember(t *testing.T, c *client, id, email string) *client {
	t.Helper()
	c.create("users", Record{"name": email, "email": email, "password": "legacy-member-test-password", "role": "member", "memberId": id})
	return docLoginClient(t, c.a, email, "legacy-member-test-password")
}

func legacyHTTPBatchCount(t *testing.T, c *client) int {
	t.Helper()
	return len(c.ok("GET", "/api/traffic/legacy/catalog", nil)["batches"].([]any))
}

func legacyHTTPDisk(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		files[rel] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestLegacyTrafficHTTPPreviewAndFailedApprovalDoNotPersist(t *testing.T) {
	c, dir := setupClient(t)
	input := legacyHTTPInput(t)
	beforeFiles := legacyHTTPDisk(t, filepath.Join(dir, "legacy-traffic"))
	beforeAudit := len(c.a.store.Snapshot().Audit)
	preview := c.ok("POST", "/api/traffic/legacy/preview", Record{"input": input})
	if number(preview, "rowCount") != 4 || number(preview, "staffOnlyRows") != 4 || len(preview["rows"].([]any)) != 4 {
		t.Fatal("preview lost original rolling reports", preview)
	}
	if !reflect.DeepEqual(beforeFiles, legacyHTTPDisk(t, filepath.Join(dir, "legacy-traffic"))) || len(c.a.store.Snapshot().Audit) != beforeAudit {
		t.Fatal("read-only preview changed archive files or audit")
	}
	checksum := str(preview, "checksum")
	for _, tc := range []struct {
		name   string
		change func(Record)
		status int
	}{
		{"unreviewed checksum", func(b Record) { b["expectedChecksum"] = strings.Repeat("0", 64) }, 409},
		{"missing windows acknowledgement", func(b Record) { b["acknowledgeWindows"] = false }, 409},
		{"wrong current password", func(b Record) { b["currentPassword"] = "wrong-password" }, 403},
		{"content changed after preview", func(b Record) {
			changed := input
			changed.Content = strings.Replace(changed.Content, "9007199254740993", "9007199254740994", 1)
			b["input"] = changed
		}, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := legacyHTTPBody(input, checksum)
			tc.change(body)
			if w := c.request("POST", "/api/traffic/legacy/import", body); w.Code != tc.status {
				t.Fatalf("status %d want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if legacyHTTPBatchCount(t, c) != 0 || len(c.a.store.Snapshot().LegacyImports) != 0 {
				t.Fatal("failed review persisted data or member publication")
			}
			if !reflect.DeepEqual(beforeFiles, legacyHTTPDisk(t, filepath.Join(dir, "legacy-traffic"))) {
				t.Fatal("failed review changed archive files")
			}
		})
	}
	meta := c.ok("POST", "/api/traffic/legacy/import", legacyHTTPBody(input, checksum))
	if meta["id"] != preview["batchId"] || meta["checksum"] != checksum || legacyHTTPBatchCount(t, c) != 1 {
		t.Fatal("approved exact input did not persist", meta)
	}
	state := c.a.store.Snapshot()
	approval := legacyApproval(&state, str(meta, "id"))
	if approval == nil || approval.Checksum != checksum || approval.ApprovedAt.IsZero() || approval.ApprovedBy == "" {
		t.Fatal("import lacks explicit approval evidence")
	}
}

func TestLegacyTrafficHTTPUnknownWindowsStayStaffOnlyAndRolesCannotImport(t *testing.T) {
	c, _ := setupClient(t)
	member := c.create("members", Record{"name": "Original report owner", "asn": 64501, "status": "active"})
	owner := legacyHTTPMember(t, c, str(member, "id"), "legacy-unknown@example.net")
	input := legacyHTTPProof(t, c, str(member, "id"))
	input.Windows = nil // A date label plus owner claim cannot establish an observation interval.
	preview := c.ok("POST", "/api/traffic/legacy/preview", Record{"input": input})
	if number(preview, "staffOnlyRows") != 4 {
		t.Fatal("unknown windows authorized by a present-day owner")
	}
	for _, role := range []string{"operator", "viewer"} {
		email := "legacy-" + role + "@example.net"
		c.create("users", Record{"name": role, "email": email, "role": role, "password": "legacy-role-test-password"})
		staff := docLoginClient(t, c.a, email, "legacy-role-test-password")
		capabilities := staff.ok("GET", "/api/traffic/legacy/catalog", nil)["capabilities"].(map[string]any)
		if capabilities["canImport"] != false || capabilities["canDelete"] != false || capabilities["canPreview"] != (role == "operator") {
			t.Fatal("incorrect staff capability projection", capabilities)
		}
		if w := staff.request("POST", "/api/traffic/legacy/import", legacyHTTPBody(input, str(preview, "checksum"))); w.Code != 403 {
			t.Fatal("non-admin imported history", role, w.Code, w.Body.String())
		}
		if role == "operator" {
			staff.ok("POST", "/api/traffic/legacy/preview", Record{"input": input})
		}
	}
	if w := owner.request("POST", "/api/traffic/legacy/preview", Record{"input": input}); w.Code != 403 {
		t.Fatal("member can preview arbitrary private legacy exports")
	}
	meta := c.ok("POST", "/api/traffic/legacy/import", legacyHTTPBody(input, str(preview, "checksum")))
	query := "/api/traffic/legacy/query?batchId=" + url.QueryEscape(str(meta, "id")) + "&period=day"
	r := c.ok("GET", query, nil)
	rows := r["rows"].([]any)
	if r["sourceKind"] != "legacy-report" || r["noAggregation"] != true || len(rows) != 1 {
		t.Fatal("legacy report was aggregated or mixed", r)
	}
	row := rows[0].(map[string]any)
	window := row["window"].(map[string]any)
	if row["staffOnly"] != true || window["start"] != nil || window["end"] != nil || window["boundaryQuality"] != "unknown" {
		t.Fatal("unknown rolling interval or visibility changed", row)
	}
	if row["in"].(map[string]any)["total"] != "9007199254740993" || row["out"].(map[string]any)["average"] != nil || row["out"].(map[string]any)["maximum"] != nil {
		t.Fatal("exact totals or unknown samples changed")
	}
	windowed := c.ok("GET", query+"&windowFrom=2024-05-01T00%3A00%3A00Z&windowTo=2024-07-01T00%3A00%3A00Z", nil)
	if len(windowed["rows"].([]any)) != 0 {
		t.Fatal("window filter inferred bounds from report label")
	}
	catalog := owner.ok("GET", "/api/traffic/legacy/catalog", nil)
	if len(catalog["batches"].([]any)) != 0 {
		t.Fatal("unknown-boundary rows exposed to member")
	}
	for _, key := range []string{"bytes", "maxBytes", "maxBatches"} {
		if _, ok := catalog[key]; ok {
			t.Fatal("global archive information exposed", key)
		}
	}
	if w := owner.request("GET", query, nil); w.Code != 404 {
		t.Fatal("member accessed staff canonical batch ID", w.Code)
	}
}

func TestLegacyTrafficHTTPOwnerApprovalAliasIsolationAndDeletion(t *testing.T) {
	c, _ := setupClient(t)
	one := c.create("members", Record{"name": "Historical owner", "asn": 64501, "status": "active"})
	two := c.create("members", Record{"name": "Other owner", "asn": 64502, "status": "active"})
	owner := legacyHTTPMember(t, c, str(one, "id"), "legacy-one@example.net")
	other := legacyHTTPMember(t, c, str(two, "id"), "legacy-two@example.net")
	input := legacyHTTPProof(t, c, str(one, "id"))
	preview := c.ok("POST", "/api/traffic/legacy/preview", Record{"input": input})
	if number(preview, "staffOnlyRows") != 3 {
		t.Fatal("evidence escaped the one declared rolling interval")
	}
	body := legacyHTTPBody(input, str(preview, "checksum"))
	if w := c.request("POST", "/api/traffic/legacy/import", body); w.Code != 400 {
		t.Fatal("member publication accepted without ownership acknowledgement")
	}
	if legacyHTTPBatchCount(t, c) != 0 {
		t.Fatal("unacknowledged owner publication persisted")
	}
	body["acknowledgeOwnership"] = true
	meta := c.ok("POST", "/api/traffic/legacy/import", body)
	catalog := owner.ok("GET", "/api/traffic/legacy/catalog", nil)
	items := catalog["batches"].([]any)
	if len(items) != 1 {
		t.Fatal("reviewed owner's batch not visible", catalog)
	}
	view := Record(items[0].(map[string]any))
	alias := str(view, "id")
	if alias == "" || alias == str(meta, "id") || alias == str(meta, "checksum") || number(view, "rowCount") != 1 {
		t.Fatal("member alias/count does not isolate authorized projection", view)
	}
	for _, key := range []string{"checksum", "inputChecksum", "size", "originalDayFrom", "originalDayTo"} {
		if _, ok := view[key]; ok {
			t.Fatal("private batch metadata exposed", key)
		}
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), input.Source.Name) {
		t.Fatal("private source label exposed")
	}
	query := "/api/traffic/legacy/query?batchId=" + url.QueryEscape(alias)
	r := owner.ok("GET", query, nil)
	if r["batchId"] != alias || number(r, "totalRows") != 1 || r["noAggregation"] != true {
		t.Fatal("member query did not preserve alias and own row projection", r)
	}
	row := r["rows"].([]any)[0].(map[string]any)
	if row["memberId"] != one["id"] || row["staffOnly"] != false || row["window"].(map[string]any)["boundaryQuality"] != "declared" {
		t.Fatal("historical ownership or explicit interval changed", row)
	}
	if _, ok := row["window"].(map[string]any)["evidence"]; ok {
		t.Fatal("private window extraction evidence exposed to member")
	}
	staffReport := c.ok("GET", "/api/traffic/legacy/query?batchId="+url.QueryEscape(str(meta, "id"))+"&period=day", nil)
	staffWindow := staffReport["rows"].([]any)[0].(map[string]any)["window"].(map[string]any)
	if staffWindow["evidence"] != input.Windows[0].Evidence || staffWindow["start"] != row["window"].(map[string]any)["start"] || staffWindow["end"] != row["window"].(map[string]any)["end"] {
		t.Fatal("staff extraction evidence or member's proven interval was lost")
	}
	if _, ok := row["ownerEvidence"]; ok {
		t.Fatal("private ownership evidence exposed")
	}
	if _, ok := row["memberRevision"]; ok {
		t.Fatal("private ownership revision exposed")
	}
	encoded, _ = json.Marshal(row)
	if strings.Contains(string(encoded), input.Owners[0].Description) || strings.Contains(string(encoded), input.Owners[0].ReferenceSHA256) {
		t.Fatal("ownership proof leaked in nested response")
	}
	if legacyHTTPBatchCount(t, other) != 0 {
		t.Fatal("foreign member sees batch")
	}
	for _, path := range []string{query, "/api/traffic/legacy/query?batchId=" + str(meta, "id")} {
		if w := other.request("GET", path, nil); w.Code != 404 {
			t.Fatal("foreign member reused another identity's ID", w.Code)
		}
	}
	if w := owner.request("GET", "/api/traffic/legacy/query?batchId="+str(meta, "id"), nil); w.Code != 404 {
		t.Fatal("member bypassed opaque ID with staff batch ID")
	}
	deletePath := "/api/traffic/legacy/batches/" + str(meta, "id")
	deletion := Record{"expectedChecksum": meta["checksum"], "reason": "synthetic retention policy", "acknowledge": true}
	if w := owner.request("DELETE", deletePath, deletion); w.Code != 403 {
		t.Fatal("member deleted archive")
	}
	badDelete := Record{"expectedChecksum": strings.Repeat("0", 64), "reason": "synthetic retention policy", "acknowledge": true}
	if w := c.request("DELETE", deletePath, badDelete); w.Code != 409 {
		t.Fatal("stale retention checksum accepted")
	}
	if legacyHTTPBatchCount(t, owner) != 1 {
		t.Fatal("failed deletion revoked valid publication")
	}
	c.ok("DELETE", deletePath, deletion)
	if legacyHTTPBatchCount(t, owner) != 0 || legacyHTTPBatchCount(t, c) != 0 {
		t.Fatal("deleted archive remains visible")
	}
	if w := owner.request("GET", query, nil); w.Code != 404 {
		t.Fatal("old member alias survived deletion")
	}
	s := c.a.store.Snapshot()
	if legacyApproval(&s, str(meta, "id")) != nil {
		t.Fatal("deleted publication approval remains")
	}
}

func TestLegacyTrafficHTTPRequiresCurrentEntityAndExplicitOnlineReview(t *testing.T) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "Historical owner", "asn": 64501, "status": "active"})
	owner := legacyHTTPMember(t, c, str(m, "id"), "legacy-epoch@example.net")
	input := legacyHTTPProof(t, c, str(m, "id"))
	stale := input
	stale.Owners = append([]traffic.LegacyOwnerEvidence(nil), input.Owners...)
	stale.Owners[0].MemberRevision = strings.Repeat("f", 64)
	preview := c.ok("POST", "/api/traffic/legacy/preview", Record{"input": stale})
	body := legacyHTTPBody(stale, str(preview, "checksum"))
	body["acknowledgeOwnership"] = true
	if w := c.request("POST", "/api/traffic/legacy/import", body); w.Code != 403 {
		t.Fatal("stale member entity evidence was published", w.Code, w.Body.String())
	}
	if legacyHTTPBatchCount(t, c) != 0 {
		t.Fatal("stale entity import persisted")
	}

	// Exercise the documented offline CLI store path without fabricating an
	// application approval. Even correct owner evidence needs online review.
	batch, err := traffic.ParseLegacy(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.a.legacyTraffic.Put(batch, batch.Checksum); err != nil {
		t.Fatal(err)
	}
	staffCatalog := c.ok("GET", "/api/traffic/legacy/catalog", nil)
	if len(staffCatalog["batches"].([]any)) != 1 || staffCatalog["batches"].([]any)[0].(map[string]any)["reviewed"] != false {
		t.Fatal("offline batch not marked unreviewed")
	}
	if legacyHTTPBatchCount(t, owner) != 0 {
		t.Fatal("offline owner claims bypassed admin review")
	}
	body = legacyHTTPBody(input, batch.Checksum)
	body["acknowledgeOwnership"] = true
	c.ok("POST", "/api/traffic/legacy/import", body)
	items := owner.ok("GET", "/api/traffic/legacy/catalog", nil)["batches"].([]any)
	if len(items) != 1 {
		t.Fatal("reviewing identical offline batch did not publish authorized row")
	}
	oldAlias := str(Record(items[0].(map[string]any)), "id")

	// Emulate restoring/reusing a legacy member ID with a different entity
	// creation marker. Public CRUD deliberately cannot edit this marker.
	err = c.a.store.Update(func(s *State) error {
		recreated := find(s, "members", str(m, "id"))
		recreated["createdAt"] = "2000-01-01T00:00:00Z"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacyHTTPBatchCount(t, owner) != 0 {
		t.Fatal("reused member ID inherited past entity's history")
	}
	if w := owner.request("GET", "/api/traffic/legacy/query?batchId="+url.QueryEscape(oldAlias), nil); w.Code != 404 {
		t.Fatal("old entity alias remained valid")
	}
	if w := c.request("POST", "/api/traffic/legacy/import", body); w.Code != 403 {
		t.Fatal("old proof approved for replacement entity", w.Code)
	}
}

func TestLegacyTrafficHTTPSharedBatchUsesDistinctTenantAliasesAndRowProjections(t *testing.T) {
	c, _ := setupClient(t)
	one := c.create("members", Record{"name": "Historic tenant A", "asn": 64501, "status": "active"})
	two := c.create("members", Record{"name": "Historic tenant B", "asn": 64502, "status": "active"})
	first := legacyHTTPMember(t, c, str(one, "id"), "legacy-shared-a@example.net")
	second := legacyHTTPMember(t, c, str(two, "id"), "legacy-shared-b@example.net")
	input := legacyHTTPProof(t, c, str(one, "id"))
	var rows []Record
	if err := json.Unmarshal([]byte(input.Content), &rows); err != nil {
		t.Fatal(err)
	}
	rows = append(rows, Record{"id": 102, "cust_id": 43, "day": "2024-06-01", "category": "bits", "day_tot_in": "777"})
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	input.Content = string(encoded)
	secondOwner := input.Owners[0]
	secondOwner.EntityID, secondOwner.MemberID = "43", str(two, "id")
	s := c.a.store.Snapshot()
	secondOwner.MemberRevision = c.a.trafficMemberRevision(&s, str(two, "id"))
	input.Owners = append(input.Owners, secondOwner)
	secondWindow := input.Windows[0]
	secondWindow.RecordID = "102"
	input.Windows = append(input.Windows, secondWindow)
	preview := c.ok("POST", "/api/traffic/legacy/preview", Record{"input": input})
	body := legacyHTTPBody(input, str(preview, "checksum"))
	body["acknowledgeOwnership"] = true
	meta := c.ok("POST", "/api/traffic/legacy/import", body)
	aliases := []string{}
	for i, member := range []*client{first, second} {
		catalog := member.ok("GET", "/api/traffic/legacy/catalog", nil)
		items := catalog["batches"].([]any)
		if len(items) != 1 {
			t.Fatal("authorized shared batch missing", i, catalog)
		}
		item := Record(items[0].(map[string]any))
		if number(item, "rowCount") != 1 {
			t.Fatal("member learns another tenant's row count", item)
		}
		alias := str(item, "id")
		aliases = append(aliases, alias)
		report := member.ok("GET", "/api/traffic/legacy/query?batchId="+alias, nil)
		visible := report["rows"].([]any)
		if len(visible) != 1 || report["batchId"] != alias {
			t.Fatal("cross-tenant rows in shared batch", report)
		}
		row := visible[0].(map[string]any)
		if row["entityId"] != []string{"42", "43"}[i] || row["in"].(map[string]any)["total"] != []string{"9007199254740993", "777"}[i] {
			t.Fatal("historical tenant projection changed", row)
		}
		foreignEntity := []string{"43", "42"}[i]
		filtered := member.ok("GET", "/api/traffic/legacy/query?batchId="+alias+"&entityId="+foreignEntity, nil)
		if number(filtered, "totalRows") != 0 || len(filtered["rows"].([]any)) != 0 {
			t.Fatal("explicit entity filter escaped the member projection")
		}
	}
	if aliases[0] == aliases[1] || aliases[0] == str(meta, "id") || aliases[1] == str(meta, "id") {
		t.Fatal("shared batch has linkable or canonical member identifiers")
	}
	for i, member := range []*client{first, second} {
		if w := member.request("GET", "/api/traffic/legacy/query?batchId="+aliases[1-i], nil); w.Code != 404 {
			t.Fatal("another tenant's alias accepted", i, w.Code)
		}
	}
	c.ok("DELETE", "/api/traffic/legacy/batches/"+str(meta, "id"), Record{"expectedChecksum": meta["checksum"], "reason": "shared synthetic archive retired", "acknowledge": true})
	for _, member := range []*client{first, second} {
		if legacyHTTPBatchCount(t, member) != 0 {
			t.Fatal("shared batch deletion failed to revoke all tenant projections")
		}
	}
}
