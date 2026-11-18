package sflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreMemberEntityRevisionAndSwitchACL(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sflow"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := testAttributed("old-member-entity")
	if err = s.Ingest(old); err != nil {
		t.Fatal(err)
	}
	current := testAttributed("new-member-entity")
	current.Flows[0].SourceMemberRevision = strings.Repeat("c", 64)
	if err = s.Ingest(current); err != nil {
		t.Fatal(err)
	}
	legacy := testAttributed("legacy-member-entity")
	legacy.Flows[0].SourceMemberRevision = ""
	legacy.Flows[0].DestinationMemberRevision = ""
	if err = s.Ingest(legacy); err != nil {
		t.Fatal(err)
	}
	other := testAttributed("other-switch")
	other.ExporterID = "other-exporter"
	other.SwitchID = "other-switch"
	if err = s.Ingest(other); err != nil {
		t.Fatal(err)
	}
	q := queryNow("member-a")
	q.MemberRevision = ""
	if _, err = s.Query(q); err == nil {
		t.Fatal("member ID without incarnation accepted")
	}
	q = queryNow("member-a")
	q.MemberRevision = strings.Repeat("c", 64)
	r, err := s.Query(q)
	if err != nil || len(r.Items) != 1 || r.Items[0].SourceMemberRevision != q.MemberRevision {
		t.Fatal("reused member ID read previous incarnation", r, err)
	}
	q = queryNow("member-b")
	q.SwitchIDs = []string{"switch"}
	r, err = s.Query(q)
	if err != nil || len(r.Items) != 2 {
		t.Fatal("destination-side ID+revision match failed or legacy leaked", r, err)
	}
	q = queryNow("member-a")
	q.MemberRevision = strings.Repeat("b", 64)
	r, err = s.Query(q)
	if err != nil || len(r.Items) != 0 {
		t.Fatal("matched one side's ID against other side's revision", r, err)
	}
	q = queryNow("")
	q.SwitchIDs = []string{"switch"}
	r, err = s.Query(q)
	if err != nil || len(r.Items) != 3 || len(r.Quality) != 1 || r.Quality[0].SwitchID != "switch" {
		t.Fatal("staff switch binding escaped in data/quality", r, err)
	}
	q.SwitchIDs = []string{"not-allowed"}
	r, _ = s.Query(q)
	if len(r.Items) != 0 || len(r.Quality) != 0 || r.OldestRetainedAt != nil {
		t.Fatal("unauthorized switch retention metadata leaked")
	}
}

func TestStoreLegacySnapshotWithoutMemberRevisionStaysStaffOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sflow")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := testAttributed("legacy")
	b.Flows[0].SourceMemberRevision = ""
	b.Flows[0].DestinationMemberRevision = ""
	if err = s.Ingest(b); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-cursor snapshot while retaining its existing aggregate keys.
	raw, err := os.ReadFile(filepath.Join(dir, "flows.snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk flowDiskSnapshot
	json.Unmarshal(raw, &disk)
	disk.State.Cursors = nil
	disk.Checksum = digest(disk.State)
	raw, _ = json.Marshal(disk)
	os.WriteFile(filepath.Join(dir, "flows.snapshot.json"), raw, 0600)
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	member, err := s.Query(queryNow("member-a"))
	if err != nil || len(member.Items) != 0 {
		t.Fatal("legacy member data disclosed", member, err)
	}
	staff, _ := s.Query(queryNow(""))
	if len(staff.Items) != 1 {
		t.Fatal("legacy staff history was discarded")
	}
	newBaseline := testAttributed("after-upgrade")
	c, err := s.IngestWithResult(newBaseline)
	if err != nil || c.AcceptFlows || c.SequenceState != "legacy-baseline" {
		t.Fatal("legacy source restarted without durable replay baseline", c, err)
	}
}

func TestStoreCursorSurvivesReopenSeenExpiryAndMappingChange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sflow")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	head := testAttributed("head")
	head.Sequence = 100
	head.UptimeMS = 100000
	first, err := s.IngestWithResult(head)
	if err != nil || !first.AcceptFlows {
		t.Fatal(first, err)
	}
	retry, err := s.IngestWithResult(head)
	if err != nil || retry.AcceptFlows {
		t.Fatal("HTTP retry counted twice", retry, err)
	}
	s.mu.Lock()
	s.state.Seen = map[string]seenBatch{}
	s.mu.Unlock()
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	replay := head
	replay.ID = "new-http-id"
	replay.ReceivedAt = time.Now().UTC()
	replay.MappingRevision = strings.Repeat("c", 22)
	replay.Quality.SequenceState = "first"
	got, err := s.IngestWithResult(replay)
	if err != nil || got.SequenceState != "duplicate" || got.AcceptFlows {
		t.Fatal("store reopen/mapping change reset cursor", got, err)
	}
	conflict := replay
	conflict.ID = "conflict"
	conflict.DatagramDigest = digest("different content")
	got, err = s.IngestWithResult(conflict)
	if err != nil || got.SequenceState != "sequence-conflict" || got.AcceptFlows {
		t.Fatal(got, err)
	}
	older := testAttributed("older")
	older.Sequence = 99
	older.UptimeMS = 99000
	got, err = s.IngestWithResult(older)
	if err != nil || got.SequenceState != "out-of-order" || got.AcceptFlows {
		t.Fatal("older UDP counted after restart", got, err)
	}
	next := testAttributed("next")
	next.Sequence = 101
	next.UptimeMS = 100100
	got, err = s.IngestWithResult(next)
	if err != nil || !got.AcceptFlows || got.SequenceState != "in-order" {
		t.Fatal("new valid sequence blocked", got, err)
	}
	r, _ := s.Query(queryNow("member-a"))
	if len(r.Items) != 1 || r.Items[0].SampleCount != "2" {
		t.Fatal("replay inflated totals", r)
	}
	status, err := s.Status()
	if err != nil || len(status) != 1 || status[0].Sequence != 101 {
		t.Fatal(status, err)
	}
}

func TestStoreRestartCandidateAndUptimeWrapAreNotBlindBootClaims(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sflow")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := testAttributed("old-boot")
	b.Sequence = 100
	b.UptimeMS = 100000
	s.Ingest(b)
	low := testAttributed("candidate")
	low.Sequence = 1
	low.UptimeMS = 100
	got, err := s.IngestWithResult(low)
	if err != nil || got.AcceptFlows || got.SequenceState != "restart-candidate" {
		t.Fatal("one arbitrary low packet counted as certain reboot", got, err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	status, _ := s.Status()
	if !status[0].PendingRestart || status[0].Sequence != 100 || status[0].LastDatagramSequence != 1 {
		t.Fatal("candidate replaced accepted checkpoint")
	}
	consistent := testAttributed("candidate-next")
	consistent.Sequence = 2
	consistent.UptimeMS = 200
	got, err = s.IngestWithResult(consistent)
	if err != nil || !got.AcceptFlows || got.SequenceState != "restart-inferred" {
		t.Fatal("consistent new stream not resumed", got, err)
	}
	wrapStore, err := Open(filepath.Join(t.TempDir(), "sflow"))
	if err != nil {
		t.Fatal(err)
	}
	defer wrapStore.Close()
	wrap := testAttributed("pre-wrap")
	wrap.Sequence = ^uint32(0)
	wrap.UptimeMS = ^uint32(0) - 10
	wrapStore.Ingest(wrap)
	wrap = testAttributed("post-wrap")
	wrap.Sequence = 0
	wrap.UptimeMS = 10
	got, err = wrapStore.IngestWithResult(wrap)
	if err != nil || !got.AcceptFlows || got.SequenceState != "uptime-wrap" {
		t.Fatal("natural wrap treated as certain restart", got, err)
	}
}
