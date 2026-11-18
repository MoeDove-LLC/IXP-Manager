package sflow

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestMembersDirectoryScopesEntityRevisionsWithoutQueryLimit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sflow"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := testAttributed("directory-first")
	if err = s.Ingest(first); err != nil {
		t.Fatal(err)
	}
	other := testAttributed("directory-other-switch")
	other.SwitchID = "elsewhere"
	other.ExporterID = "other-exporter"
	other.Flows[0].SourceMemberID = "outside-source"
	other.Flows[0].DestinationMemberID = "outside-destination"
	if err = s.Ingest(other); err != nil {
		t.Fatal(err)
	}
	legacy := testAttributed("directory-legacy")
	legacy.Flows[0].SourceMemberID = "legacy-source"
	legacy.Flows[0].DestinationMemberID = "legacy-destination"
	legacy.Flows[0].SourceMemberRevision = ""
	legacy.Flows[0].DestinationMemberRevision = ""
	if err = s.Ingest(legacy); err != nil {
		t.Fatal(err)
	}
	unattributed := testAttributed("directory-unattributed")
	unattributed.Flows[0].SourceMemberID = "unattributed-source"
	unattributed.Flows[0].UnattributedReason = "ambiguous-mac"
	if err = s.Ingest(unattributed); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Members([]string{"switch"})
	if err != nil || len(rows) != 2 || rows[0].ID != "member-a" || rows[1].ID != "member-b" {
		t.Fatal("directory escaped switch/revision/attribution scope", rows, err)
	}
	if rows, err = s.Members([]string{"unknown-switch"}); err != nil || len(rows) != 0 {
		t.Fatal("empty switch scope disclosed identities", rows, err)
	}
	if rows, err = s.Members(nil); err != nil || len(rows) != 4 {
		t.Fatal("staff unrestricted directory missing identities", rows, err)
	}
	// Build a bounded in-memory history beyond the statistics response row limit.
	// The directory must not accidentally reuse Query(limit=10000).
	s.mu.Lock()
	var template Bucket
	for _, row := range s.state.Rows {
		if row.SourceMemberID == "member-a" {
			template = row
			break
		}
	}
	for i := 0; i < 10001; i++ {
		row := template
		row.SourceMemberID = fmt.Sprintf("history-%05d", i)
		row.SourceMemberRevision = strings.Repeat("c", 64)
		s.state.Rows[bucketKey(row)] = row
	}
	s.mu.Unlock()
	rows, err = s.Members([]string{"switch"})
	if err != nil || len(rows) != 10003 {
		t.Fatal("authorization directory was truncated by report limit", len(rows), err)
	}
	for _, ids := range [][]string{{"switch", "switch"}, {"../switch"}} {
		if _, err = s.Members(ids); err == nil {
			t.Fatal("invalid authorization set accepted")
		}
	}
}
