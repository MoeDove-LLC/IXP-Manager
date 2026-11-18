package sflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testSequence atomic.Uint32

func testAttributed(id string) AttributedBatch {
	sequence := testSequence.Add(1)
	return AttributedBatch{ID: id, Sequence: sequence, UptimeMS: 100000 + sequence*100, DatagramDigest: digest([]string{"datagram", id}), ReceivedAt: time.Now().UTC(), ExporterID: "exporter", SwitchID: "switch", InfrastructureID: "fabric", MappingRevision: strings.Repeat("a", 22), Quality: Quality{SequenceState: "first"}, Flows: []AttributedFlow{{VLANID: "vlan", Family: 4, SourceMemberID: "member-a", DestinationMemberID: "member-b", SourceMemberRevision: strings.Repeat("a", 64), DestinationMemberRevision: strings.Repeat("b", 64), SourceConnectionID: "connection-a", DestinationConnectionID: "connection-b", EstimatedBytes: "18446744073709551615", EstimatedPackets: "400", SampleCount: 1, SampledTCP179Activity: true}}}
}
func queryNow(member string) Query {
	revision := ""
	if member != "" {
		revision = strings.Repeat("f", 64)
		if member == "member-a" {
			revision = strings.Repeat("a", 64)
		}
		if member == "member-b" {
			revision = strings.Repeat("b", 64)
		}
	}
	return Query{From: time.Now().UTC().Add(-10 * time.Minute), To: time.Now().UTC().Add(time.Second), Resolution: "5m", MemberID: member, MemberRevision: revision, Limit: 100}
}
func crashStore(s *Store) {
	s.mu.Lock()
	s.closed = true
	close(s.stop)
	s.wal.Close()
	s.lock.Close()
	s.mu.Unlock()
	<-s.done
}
func TestStoreExactTotalsIdempotencyAndTenantScope(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sflow")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := testAttributed("batch-one")
	if err = s.Ingest(first); err != nil {
		t.Fatal(err)
	}
	if err = s.Ingest(first); err != nil {
		t.Fatal(err)
	}
	second := testAttributed("batch-two")
	if err = s.Ingest(second); err != nil {
		t.Fatal(err)
	}
	r, err := s.Query(queryNow("member-a"))
	if err != nil || len(r.Items) != 1 || r.Items[0].EstimatedBytes != "36893488147419103230" || r.Items[0].SampleCount != "2" || len(r.Quality) != 0 {
		t.Fatal("precision, idempotency or member quality filtering failed", r, err)
	}
	r, _ = s.Query(queryNow("unrelated"))
	if len(r.Items) != 0 {
		t.Fatal("unrelated member saw flow")
	}
	r, _ = s.Query(queryNow(""))
	if len(r.Quality) != 1 || r.Quality[0].Datagrams != "2" {
		t.Fatal("quality aggregates absent")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Query(queryNow("")); err == nil {
		t.Fatal("closed store read")
	}
	if err = s.Ingest(testAttributed("closed")); err == nil {
		t.Fatal("closed store write")
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, _ = s.Query(queryNow("member-b"))
	if len(r.Items) != 1 || r.Items[0].SampleCount != "2" {
		t.Fatal("restore lost totals")
	}
	second.Flows[0].EstimatedBytes = "1"
	if err = s.Ingest(second); err == nil {
		t.Fatal("batch ID content conflict accepted")
	}
}
func TestStoreWALReplayTornTailAndCompactionCrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sflow")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Ingest(testAttributed("durable")); err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(filepath.Join(dir, "flows.wal"))
	if err != nil {
		t.Fatal(err)
	}
	crashStore(s)
	f, err := os.OpenFile(filepath.Join(dir, "flows.wal"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"sequence":`)
	f.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.Query(queryNow("member-a"))
	if len(r.Items) != 1 || r.Items[0].SampleCount != "1" {
		t.Fatal("WAL replay/torn tail lost durable record")
	}
	s.Close()
	if err = os.WriteFile(filepath.Join(dir, "flows.wal"), wal, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, _ = s.Query(queryNow("member-a"))
	if len(r.Items) != 1 || r.Items[0].SampleCount != "1" {
		t.Fatal("snapshot-before-truncate crash counted twice")
	}
	s.Close()
	var frame flowFrame
	if err = json.Unmarshal(wal, &frame); err != nil {
		t.Fatal(err)
	}
	frame.Checksum = strings.Repeat("0", 64)
	bad, _ := json.Marshal(frame)
	os.WriteFile(filepath.Join(dir, "flows.wal"), append(bad, '\n'), 0600)
	if opened, err := Open(dir); err == nil {
		opened.Close()
		t.Fatal("corrupt complete WAL accepted")
	}
}
func TestStoreUnknownEpochAndDirectionRemainSeparate(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sflow"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := testAttributed("a")
	s.Ingest(b)
	b = testAttributed("b")
	b.MappingRevision = strings.Repeat("b", 22)
	s.Ingest(b)
	b = testAttributed("c")
	b.Flows[0].SourceMemberID, b.Flows[0].DestinationMemberID = "member-b", "member-a"
	b.Flows[0].SourceMemberRevision, b.Flows[0].DestinationMemberRevision = b.Flows[0].DestinationMemberRevision, b.Flows[0].SourceMemberRevision
	b.Flows[0].SourceConnectionID, b.Flows[0].DestinationConnectionID = "connection-b", "connection-a"
	s.Ingest(b)
	b = testAttributed("unknown")
	b.Flows[0] = AttributedFlow{Family: 4, SampleCount: 1, UnattributedReason: "unknown-sampling-rate"}
	if err = s.Ingest(b); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Query(queryNow("member-a"))
	if len(r.Items) != 3 {
		t.Fatal("epoch or direction merged", len(r.Items))
	}
	r, _ = s.Query(queryNow(""))
	if len(r.Items) != 4 {
		t.Fatal("unattributed sample lost")
	}
	for _, row := range r.Items {
		if row.UnattributedReason != "" && row.EstimatedBytes != "" {
			t.Fatal("unknown estimate fabricated zero")
		}
	}
	b = testAttributed("duplicate")
	head := s.state.Cursors[b.ExporterID].Head
	b.Sequence, b.UptimeMS, b.DatagramDigest = head.Sequence, head.UptimeMS, head.Digest
	classified, err := s.IngestWithResult(b)
	if err != nil || classified.AcceptFlows || classified.SequenceState != "duplicate" {
		t.Fatal("replayed datagram estimates accepted or trusted client quality", classified, err)
	}
}
func TestStoreNoSamplesNoSyntheticMatrixAndRetention(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sflow"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.Query(queryNow("member-a"))
	if err != nil || len(r.Items) != 0 || r.OldestRetainedAt != nil {
		t.Fatal("empty store invented zero pairs", r, err)
	}
	b := testAttributed("expired")
	b.ReceivedAt = time.Now().Add(-6 * time.Minute)
	if s.Ingest(b) == nil {
		t.Fatal("late packet accepted under current mapping")
	}
	b = testAttributed("future")
	b.ReceivedAt = time.Now().Add(time.Minute)
	if s.Ingest(b) == nil {
		t.Fatal("future packet accepted")
	}
	q := queryNow("")
	q.Limit = 10001
	if _, err = s.Query(q); err == nil {
		t.Fatal("unbounded query accepted")
	}
	if second, err := Open(s.dir); err == nil {
		second.Close()
		t.Fatal("second writer acquired store")
	}
}

func TestUTCDayBucketsAndResolutionRetention(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 30, 0, time.UTC)
	s := flowSnapshot{Version: 1, Rows: map[string]Bucket{}, Seen: map[string]seenBatch{}, Quality: map[string]QualityDay{}}
	b := testAttributed("before-midnight")
	b.ReceivedAt = now.Add(-time.Minute)
	var err error
	s, err = apply(s, b, now)
	if err != nil {
		t.Fatal(err)
	}
	b = testAttributed("after-midnight")
	b.ReceivedAt = now
	s, err = apply(s, b, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Rows) != 4 || len(s.Quality) != 2 {
		t.Fatal("UTC midnight windows merged", len(s.Rows), len(s.Quality))
	}
	prune(&s, now.Add(3*24*time.Hour))
	if len(s.Rows) != 2 {
		t.Fatal("5m retention did not preserve only daily rows")
	}
	for _, row := range s.Rows {
		if row.Resolution != "1d" || row.SampleCount != "1" {
			t.Fatal("daily estimate changed during retention")
		}
	}
	prune(&s, now.Add(31*24*time.Hour))
	if len(s.Rows) != 0 || len(s.Quality) != 0 {
		t.Fatal("expired day records survived")
	}
}

func TestStoreQueryIncludesWholeOverlappingBucket(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sflow"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := testAttributed("boundary")
	if err = s.Ingest(b); err != nil {
		t.Fatal(err)
	}
	q := queryNow("member-a")
	q.From = b.ReceivedAt.Add(-time.Second)
	q.To = b.ReceivedAt.Add(time.Second)
	r, err := s.Query(q)
	if err != nil || len(r.Items) != 1 || !r.From.Equal(q.From.UTC().Truncate(5*time.Minute)) || r.To.Before(q.To) {
		t.Fatal("overlapping first bucket lost or implicit boundaries", r, err)
	}
}
