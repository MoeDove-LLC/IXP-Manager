package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/traffic"
)

func trafficBatch(at time.Time, in string) Batch {
	return Batch{RouterID: "switch:s", ObservedAt: at, Ports: []PortSample{{PortID: "p", MemberID: "m", OwnershipRevision: "ownership", CounterEpoch: "epoch", LinkSpeedMbps: 1000, InOctets: in, OutOctets: "0", OperStatus: "up", Dimensions: traffic.Dimensions{SwitchID: "s", MemberRevision: "member-revision", ConnectionIDs: []string{"c"}}, Errors: &traffic.CounterPair{In: in, Out: "0", Bits: 32}}}}
}
func trafficQuery(at time.Time) traffic.Query {
	return traffic.Query{Scope: "port", ID: "p", Metric: traffic.Octets, Resolution: traffic.FiveMinutes, From: at.Truncate(5 * time.Minute), To: at.Truncate(5 * time.Minute).Add(5 * time.Minute)}
}
func assertTrafficTotal(t *testing.T, s *Store, q traffic.Query, want string) {
	t.Helper()
	got, e := s.QueryTraffic(q, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if got.Summary.In.Total == nil || *got.Summary.In.Total != want {
		t.Fatalf("want %s got %+v", want, got.Summary.In)
	}
}
func TestTrafficWALReplayCheckpointCrashAndOrphan(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Truncate(5 * time.Minute).Add(-5 * time.Minute)
	for i, v := range []string{"0", "123", "246"} {
		b := trafficBatch(at.Add(time.Duration(i)*time.Minute), v)
		if e = s.Ingest(b, b.ObservedAt); e != nil {
			t.Fatal(e)
		}
	}
	q := trafficQuery(at)
	assertTrafficTotal(t, s, q, "246")
	wal, e := os.ReadFile(filepath.Join(dir, "journal.bin"))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.compact(); e != nil {
		t.Fatal(e)
	}
	files, e := ListFiles(dir)
	if e != nil || len(files) != 6 {
		t.Fatalf("archive files %+v %v", files, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	// The new checkpoint is published but an old, fully committed WAL prefix
	// remains after a crash before truncation. Sequence replay must be idempotent.
	if e = os.WriteFile(filepath.Join(dir, "journal.bin"), wal, 0600); e != nil {
		t.Fatal(e)
	}
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	assertTrafficTotal(t, s, q, "246")
	if e = s.Ingest(trafficBatch(at.Add(3*time.Minute), "300"), at.Add(3*time.Minute)); e != nil {
		t.Fatal(e)
	}
	assertTrafficTotal(t, s, q, "300")
	orphan, e := s.writeArchive("catalog", map[string]archiveCohort{})
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	assertTrafficTotal(t, s, q, "300")
	if _, e = os.Stat(filepath.Join(dir, "archive", orphan.File)); !os.IsNotExist(e) {
		t.Fatal("unreferenced immutable file was not reclaimed")
	}
}
func TestTrafficFrozenDimensionsDeepCopyAndPrivateLegacy(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	at := time.Now().UTC().Truncate(5 * time.Minute).Add(-5 * time.Minute)
	b := trafficBatch(at, "0")
	if e = s.Ingest(b, at); e != nil {
		t.Fatal(e)
	}
	b.Ports[0].Dimensions.ConnectionIDs[0] = "mutated"
	b.Ports[0].Errors.In = "999"
	if e = s.Ingest(trafficBatch(at.Add(time.Minute), "10"), at.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	p := s.Series("p", at, at.Add(time.Minute))
	if p[0].Dimensions.ConnectionIDs[0] != "c" || p[0].Errors.In != "0" {
		t.Fatal("caller aliases persisted dimensions")
	}
	p[0].Dimensions.ConnectionIDs[0] = "also-mutated"
	catalog, e := s.TrafficTargets(traffic.TargetQuery{Scope: "connection", MemberID: "m", MemberRevision: "member-revision"}, at)
	if e != nil || len(catalog.Targets) != 1 || catalog.Targets[0].ID != "c" {
		t.Fatalf("catalog %+v %v", catalog, e)
	}
	q := trafficQuery(at)
	q.MemberID = "m"
	q.MemberRevision = "replacement-member"
	r, e := s.QueryTraffic(q, at)
	if e != nil || r.Summary.In.Total != nil {
		t.Fatal("reused member ID received old totals")
	}
	q.MemberRevision = "member-revision"
	q.OwnershipRevision = "replacement-binding"
	r, e = s.QueryTraffic(q, at)
	if e != nil || r.Summary.In.Total != nil {
		t.Fatal("reassigned physical port leaked previous totals")
	}
}
func TestTrafficArchivesSurviveRawPruningAndRejectCorruption(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(5 * time.Minute)
	for i, v := range []string{"10", "30"} {
		b := trafficBatch(at.Add(time.Duration(i)*time.Minute), v)
		if e = s.Ingest(b, b.ObservedAt); e != nil {
			t.Fatal(e)
		}
	}
	s.prune(time.Now())
	if len(s.state.Ports) != 0 {
		t.Fatal("raw did not expire")
	}
	assertTrafficTotal(t, s, trafficQuery(at), "20")
	if e = s.compact(); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	assertTrafficTotal(t, s, trafficQuery(at), "20")
	var ref archiveRef
	for _, r := range s.state.Archive.Refs {
		ref = r
		break
	}
	s.Close()
	if e = os.WriteFile(filepath.Join(dir, "archive", ref.File), []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = ValidateFiles(dir); e == nil {
		t.Fatal("corrupt archive accepted by read-only backup validation")
	}
	if st, e := Open(dir); e == nil {
		st.Close()
		t.Fatal("corrupt archive opened")
	}
}
func TestTrafficLegacySnapshotDoesNotInventMemberGeneration(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().UTC().Add(-2 * time.Minute)
	b := trafficBatch(at, "10")
	b.Ports[0].Dimensions = traffic.Dimensions{}
	state := diskState{Version: 1, Ports: map[string][]PortPoint{"p": {{PortSample: b.Ports[0], RouterID: b.RouterID, ObservedAt: at}}}, Sessions: map[string][]SessionPoint{}}
	raw, _ := json.Marshal(state)
	if e := os.WriteFile(filepath.Join(dir, "snapshot.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b.ObservedAt = at.Add(time.Minute)
	b.Ports[0].InOctets = "20"
	if e = s.Ingest(b, b.ObservedAt); e != nil {
		t.Fatal(e)
	}
	catalog, e := s.TrafficTargets(traffic.TargetQuery{Scope: "port"}, at)
	if e != nil || len(catalog.Targets) != 1 || !catalog.Targets[0].StaffOnly {
		t.Fatalf("legacy attribution %+v %v", catalog, e)
	}
	catalog, e = s.TrafficTargets(traffic.TargetQuery{Scope: "port", MemberID: "m", MemberRevision: "new"}, at)
	if e != nil || len(catalog.Targets) != 0 {
		t.Fatal("legacy ownership was upgraded from present inventory")
	}
}
func TestTrafficCloseGenerationRejectsReadsAndWrites(t *testing.T) {
	dir := t.TempDir()
	a, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	a.Close()
	b, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	at := time.Now().UTC().Truncate(5 * time.Minute)
	if e = a.Ingest(trafficBatch(at, "0"), at); e == nil {
		t.Fatal("closed writer accepted data")
	}
	if _, e = a.QueryTraffic(trafficQuery(at), at); e == nil {
		t.Fatal("closed reader accepted replaced generation")
	}
	if _, e = a.TrafficTargets(traffic.TargetQuery{}, at); e == nil {
		t.Fatal("closed catalog accepted replaced generation")
	}
}

func TestTrafficHardQuotaAndReadOnlyValidation(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	f, e := os.Create(filepath.Join(dir, "archive", ".pending-capacity"))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate(archiveQuota); e != nil {
		t.Fatal(e)
	}
	f.Close()
	if _, e = s.writeArchive("catalog", map[string]archiveCohort{}); e == nil {
		t.Fatal("hard disk quota did not reject write")
	}
	if e = os.Remove(filepath.Join(dir, "archive", ".pending-capacity")); e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Truncate(5 * time.Minute).Add(-5 * time.Minute)
	if e = s.Ingest(trafficBatch(at, "0"), at); e != nil {
		t.Fatal(e)
	}
	if e = s.Ingest(trafficBatch(at.Add(time.Minute), "10"), at.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e = s.compact(); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "journal.bin")
	if e = os.WriteFile(path, []byte{1, 2, 3}, 0600); e != nil {
		t.Fatal(e)
	}
	if e = ValidateFiles(dir); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(path)
	if len(raw) != 3 {
		t.Fatal("read-only validation repaired partial journal")
	}
}
func TestTrafficDimensionCounterValidationAndSemanticRetry(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	at := time.Now().UTC()
	b := trafficBatch(at, "0")
	b.Ports[0].Dimensions.ConnectionIDs = []string{"z", "c"}
	if e = s.Ingest(b, at); e != nil {
		t.Fatal(e)
	}
	b.Ports[0].Dimensions.ConnectionIDs = []string{"c", "z"}
	b.Ports[0].CounterBits = 64
	if e = s.Ingest(b, at); e != nil {
		t.Fatalf("semantically identical retry rejected: %v", e)
	}
	bad := trafficBatch(at.Add(time.Minute), "10")
	bad.Ports[0].Errors.In = "4294967296"
	if e = s.Ingest(bad, bad.ObservedAt); e == nil {
		t.Fatal("32-bit optional counter overflow accepted")
	}
	bad = trafficBatch(at.Add(time.Minute), "10")
	bad.Ports[0].Dimensions.ConnectionIDs = make([]string, 257)
	if e = s.Ingest(bad, bad.ObservedAt); e == nil {
		t.Fatal("incomplete connection attribution accepted")
	}
}
func TestTrafficRetentionTiersAndUTCDateBoundary(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	at := time.Now().UTC().Add(-100 * 24 * time.Hour).Truncate(time.Hour)
	for i, v := range []string{"0", "10"} {
		b := trafficBatch(at.Add(time.Duration(i)*time.Minute), v)
		if e = s.Ingest(b, b.ObservedAt); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.compact(); e != nil {
		t.Fatal(e)
	}
	q := trafficQuery(at)
	r, e := s.QueryTraffic(q, time.Now())
	if e != nil || r.Summary.In.Total != nil {
		t.Fatal("expired fine tier still visible")
	}
	q.Resolution = traffic.Hour
	q.From = at
	q.To = at.Add(time.Hour)
	assertTrafficTotal(t, s, q, "10")
	q.Resolution = traffic.Day
	q.From = at.Truncate(24 * time.Hour)
	q.To = q.From.Add(24 * time.Hour)
	assertTrafficTotal(t, s, q, "10")
}

func TestTrafficSequenceSkipsCompactedEventsAfterRawEviction(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(5 * time.Minute)
	for i, v := range []string{"0", "10", "30"} {
		b := trafficBatch(at.Add(time.Duration(i)*time.Minute), v)
		if e = s.Ingest(b, b.ObservedAt); e != nil {
			t.Fatal(e)
		}
	}
	wal, e := os.ReadFile(filepath.Join(dir, "journal.bin"))
	if e != nil {
		t.Fatal(e)
	}
	s.prune(time.Now())
	if len(s.state.Ports) != 0 {
		t.Fatal("raw points unexpectedly retained")
	}
	if e = s.compact(); e != nil {
		t.Fatal(e)
	}
	s.Close()
	if e = os.WriteFile(filepath.Join(dir, "journal.bin"), wal, 0600); e != nil {
		t.Fatal(e)
	}
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if len(s.state.Ports) != 0 {
		t.Fatal("checkpoint-covered raw events were resurrected")
	}
	assertTrafficTotal(t, s, trafficQuery(at), "30")
}
func TestTrafficDailyMonthlyPartitionAcrossDates(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	now := time.Now().UTC()
	base := time.Date(now.Year(), now.Month(), 2, 0, 0, 0, 0, time.UTC)
	if base.After(now) {
		base = base.AddDate(0, -1, 0)
	}
	for _, day := range []time.Time{base, base.Add(24 * time.Hour)} {
		for i, v := range []string{"0", "10"} {
			b := trafficBatch(day.Add(time.Duration(i)*time.Minute), v)
			if e = s.Ingest(b, b.ObservedAt); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = s.compact(); e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, ref := range s.state.Archive.Refs {
		if ref.Resolution == traffic.Day {
			count++
			if ref.Day != base.Format("2006-01-")+"01" {
				t.Fatalf("daily period %s", ref.Day)
			}
		}
	}
	if count != 1 {
		t.Fatalf("wanted one shared monthly shard, got %d", count)
	}
	q := traffic.Query{Scope: "port", ID: "p", Metric: traffic.Octets, Resolution: traffic.Day, From: base, To: base.Add(48 * time.Hour)}
	assertTrafficTotal(t, s, q, "20")
	if e = ValidateFiles(dir); e != nil {
		t.Fatal(e)
	}
}

func TestTrafficMixedWidthPersistenceAndValidation(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	at := time.Now().UTC().Truncate(5 * time.Minute).Add(-5 * time.Minute)
	b := trafficBatch(at, "0")
	b.Ports[0].Unicast = &traffic.CounterPair{In: "4294967300", Out: "4294967290", InBits: 64, OutBits: 32}
	b.Ports[0].LinkSpeedMbps = 1
	if e = s.Ingest(b, at); e != nil {
		t.Fatal(e)
	}
	b = trafficBatch(at.Add(time.Minute), "10")
	b.Ports[0].Unicast = &traffic.CounterPair{In: "4294967400", Out: "4", InBits: 64, OutBits: 32}
	b.Ports[0].LinkSpeedMbps = 1
	if e = s.Ingest(b, b.ObservedAt); e != nil {
		t.Fatal(e)
	}
	q := trafficQuery(at)
	q.Metric = traffic.Unicast
	r, e := s.QueryTraffic(q, time.Now())
	if e != nil || r.Summary.In.Total == nil || *r.Summary.In.Total != "100" || r.Summary.Out.Total != nil || r.Summary.Out.AmbiguousIntervals != 1 || r.Summary.Out.LowerBoundTotal == nil || *r.Summary.Out.LowerBoundTotal != "10" {
		t.Fatalf("mixed persistence %+v %v", r.Summary, e)
	}
	b.ObservedAt = b.ObservedAt.Add(time.Minute)
	b.Ports[0].Unicast.Out = "4294967296"
	if e = s.Ingest(b, b.ObservedAt); e == nil {
		t.Fatal("out32 overflow hidden by in64")
	}
}

func TestTrafficSubMillisecondBoundaryCompactsWithoutLostDelta(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	at := time.Now().UTC().Truncate(5 * time.Minute).Add(-time.Nanosecond)
	for i, v := range []string{"0", "1"} {
		b := trafficBatch(at.Add(time.Duration(i)*time.Minute), v)
		if e = s.Ingest(b, b.ObservedAt); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.compact(); e != nil {
		t.Fatal(e)
	}
	if e = ValidateFiles(s.dir); e != nil {
		t.Fatal(e)
	}
	q := trafficQuery(at)
	q.To = q.To.Add(5 * time.Minute)
	assertTrafficTotal(t, s, q, "1")
}

func TestTrafficCounterEpochBreaksIntervalsWithoutDuplicatingTargetDirectory(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	at := time.Now().UTC().Truncate(5 * time.Minute).Add(-5 * time.Minute)
	for i, epoch := range []string{"boot-a", "boot-b", "boot-c", "boot-c"} {
		b := trafficBatch(at.Add(time.Duration(i)*time.Minute), []string{"0", "10", "20", "30"}[i])
		b.Ports[0].CounterEpoch = epoch
		if e = s.Ingest(b, b.ObservedAt); e != nil {
			t.Fatal(e)
		}
	}
	if len(s.archive.cohorts) != 1 {
		t.Fatalf("counter restarts duplicated attribution catalog: %d", len(s.archive.cohorts))
	}
	assertTrafficTotal(t, s, trafficQuery(at), "10")
	if e = s.compact(); e != nil {
		t.Fatal(e)
	}
	if e = ValidateFiles(s.dir); e != nil {
		t.Fatal(e)
	}
}

func TestTrafficHistoricalWatermarkSurvivesRawExpiryAndClockRollback(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	at := time.Now().UTC().Add(-48 * time.Hour)
	b := trafficBatch(at, "100")
	if e = s.Ingest(b, at); e != nil {
		t.Fatal(e)
	}
	s.prune(time.Now())
	if len(s.state.Ports) != 0 {
		t.Fatal("expected raw expiry")
	}
	b.ObservedAt = at.Add(-time.Minute)
	b.Ports[0].CounterEpoch = "new-epoch"
	if e = s.Ingest(b, b.ObservedAt); e == nil {
		t.Fatal("expired raw baseline allowed historical time regression")
	}
}

func TestTrafficLowerBoundsPersistAndNeverBecomeExact(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Truncate(5 * time.Minute).Add(-5 * time.Minute)
	for i, v := range []string{"100", "123"} {
		b := trafficBatch(at.Add(time.Duration(i)*time.Minute), "0")
		b.Ports[0].Errors = &traffic.CounterPair{In: v, Out: "0", Bits: 32}
		if e = s.Ingest(b, b.ObservedAt); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.compact(); e != nil {
		t.Fatal(e)
	}
	s.Close()
	if e = ValidateFiles(dir); e != nil {
		t.Fatal(e)
	}
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	q := trafficQuery(at)
	q.Metric = traffic.Errors
	r, e := s.QueryTraffic(q, time.Now())
	if e != nil || r.Summary.In.Total != nil || r.Summary.In.LowerBoundTotal == nil || *r.Summary.In.LowerBoundTotal != "23" || r.Summary.In.AmbiguousIntervals != 1 || r.Summary.In.AverageRate != nil || r.Summary.In.MaxRate != nil {
		t.Fatalf("persisted bound became exact %+v %v", r.Summary, e)
	}
}

func TestTrafficCounter64AboveNominalSpeedMatchesRawAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Truncate(5 * time.Minute).Add(-5 * time.Minute)
	for i, value := range []string{"9007199254740992", "9007208254740992"} {
		b := trafficBatch(at.Add(time.Duration(i)*time.Minute), value)
		b.RouterID = "router:r"
		b.Ports[0].CounterBits = 64
		b.Ports[0].Errors = nil
		b.Ports[0].OutOctets = value
		if e = s.Ingest(b, b.ObservedAt); e != nil {
			t.Fatal(e)
		}
	}
	point := s.Snapshot(at.Add(time.Minute)).Ports["p"]
	if point.InBps == nil || *point.InBps != 1200000000 || point.OutBps == nil || *point.OutBps != 1200000000 {
		t.Fatalf("raw fixture rate %+v", point)
	}
	assertTrafficTotal(t, s, trafficQuery(at), "9000000000")
	if e = s.compact(); e != nil {
		t.Fatal(e)
	}
	s.Close()
	if e = ValidateFiles(dir); e != nil {
		t.Fatal(e)
	}
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	result, e := s.QueryTraffic(trafficQuery(at), time.Now())
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range []traffic.Direction{result.Summary.In, result.Summary.Out} {
		if d.Total == nil || *d.Total != "9000000000" || d.AverageRate == nil || *d.AverageRate != 1200000000 || d.GapIntervals != 1 {
			t.Fatalf("archived delta lost (only initial baseline gap expected): %+v", d)
		}
	}
}
