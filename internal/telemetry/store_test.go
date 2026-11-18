package telemetry

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func sample(at time.Time, in, out string) Batch {
	return Batch{RouterID: "router-a", ObservedAt: at, Ports: []PortSample{{PortID: "port-a", MemberID: "member-a", InOctets: in, OutOctets: out, OperStatus: "up", CounterBits: 64, CounterEpoch: "boot:1"}}}
}
func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}
func TestCounterRatesAboveJSIntegerRangeAndRestart(t *testing.T) {
	s, dir := openTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-30 * time.Second)
	first := sample(start, "9007199254740993", "18446744073709551000")
	if err := s.Ingest(first, now); err != nil {
		t.Fatal(err)
	}
	second := sample(start.Add(10*time.Second), "9007199254742243", "18446744073709551100")
	if err := s.Ingest(second, now); err != nil {
		t.Fatal(err)
	}
	latest := s.Snapshot(now).Ports["port-a"]
	if latest.InBps == nil || *latest.InBps != 1000 || latest.OutBps == nil || *latest.OutBps != 80 {
		t.Fatalf("incorrect delta rates: %+v", latest)
	}
	if latest.InOctets != "9007199254742243" {
		t.Fatal("counter precision lost")
	}
	if err := s.Ingest(second, now); err != nil {
		t.Fatal("identical retry rejected")
	}
	if got := len(s.Series("port-a", start, now)); got != 2 {
		t.Fatal("retry duplicated sample")
	}
	s.Close()
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	after := again.Snapshot(now).Ports["port-a"]
	if after.InBps == nil || *after.InBps != 1000 {
		t.Fatal("replay lost rates")
	}
}
func TestResetWrapEpochAndAmbiguousCounter32(t *testing.T) {
	cases := []struct {
		name                     string
		old, next                string
		bits                     int
		speed                    uint64
		epoch, owner             string
		wantReset, wrapped, rate bool
	}{
		{"reset", "10000", "5", 64, 0, "boot:1", "member-a", true, false, false},
		{"32_wrap", "4294967200", "96", 32, 1, "boot:1", "member-a", false, true, true},
		{"ambiguous_multiwrap", "4294967200", "96", 32, 100000, "boot:1", "member-a", false, true, false},
		{"epoch_reset", "10000", "11000", 64, 0, "boot:2", "member-a", true, false, false},
		{"owner_changed", "10000", "11000", 64, 0, "boot:1", "member-b", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTest(t)
			now := time.Now().UTC()
			a := sample(now.Add(-2*time.Second), tc.old, tc.old)
			a.Ports[0].CounterBits = tc.bits
			a.Ports[0].LinkSpeedMbps = tc.speed
			if err := s.Ingest(a, now); err != nil {
				t.Fatal(err)
			}
			b := sample(now, tc.next, tc.next)
			b.Ports[0].CounterBits = tc.bits
			b.Ports[0].LinkSpeedMbps = tc.speed
			b.Ports[0].CounterEpoch = tc.epoch
			b.Ports[0].MemberID = tc.owner
			if err := s.Ingest(b, now); err != nil {
				t.Fatal(err)
			}
			p := s.Snapshot(now).Ports["port-a"]
			if p.CounterReset != tc.wantReset || p.CounterWrapped != tc.wrapped || (p.InBps != nil) != tc.rate {
				t.Fatalf("unexpected discontinuity: %+v", p)
			}
			if tc.name == "32_wrap" && *p.InBps != 768 {
				t.Fatalf("wrap rate=%v", *p.InBps)
			}
		})
	}
}
func TestTimestampAndCounterBounds(t *testing.T) {
	s, _ := openTest(t)
	now := time.Now()
	if err := s.Ingest(sample(now, "1", "1"), now); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(-time.Second), now.Add(500 * time.Millisecond), now.Add(31 * time.Second), now.Add(-11 * time.Minute)} {
		if err := s.Ingest(sample(at, "2", "2"), now); err == nil {
			t.Fatal("invalid observation time accepted")
		}
	}
	for _, v := range []string{"-1", "01", "1.5", "18446744073709551616"} {
		if err := s.Ingest(sample(now.Add(time.Second), v, "2"), now); err == nil {
			t.Fatalf("invalid counter %s accepted", v)
		}
	}
}
func TestJournalTruncatedTailRecoveryAndCorruption(t *testing.T) {
	s, dir := openTest(t)
	now := time.Now()
	if err := s.Ingest(sample(now, "1", "1"), now); err != nil {
		t.Fatal(err)
	}
	s.Close()
	path := filepath.Join(dir, "journal.bin")
	info, _ := os.Stat(path)
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	f.Write([]byte{0, 0, 0, 10, 1, 2, 3})
	f.Close()
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
	recovered, _ := os.Stat(path)
	if recovered.Size() != info.Size() {
		t.Fatal("partial tail not truncated")
	}
	raw, _ := os.ReadFile(path)
	raw[len(raw)-1] ^= 1
	os.WriteFile(path, raw, 0600)
	if broken, err := Open(dir); err == nil {
		broken.Close()
		t.Fatal("corrupt committed record accepted")
	}
}
func TestSnapshotPrefixReplayAfterCompactionCrash(t *testing.T) {
	s, dir := openTest(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		b := sample(now.Add(time.Duration(i-5)*time.Second), strconv.Itoa(i*100), "0")
		if err := s.Ingest(b, now); err != nil {
			t.Fatal(err)
		}
	}
	journal, _ := os.ReadFile(filepath.Join(dir, "journal.bin"))
	s.mu.Lock()
	err := s.compact()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err = os.WriteFile(filepath.Join(dir, "journal.bin"), journal, 0600); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if len(again.Series("port-a", now.Add(-time.Minute), now)) != 5 {
		t.Fatal("snapshot journal overlap duplicated history")
	}
}
func TestBoundedRetentionAndLongGap(t *testing.T) {
	s, _ := openTest(t)
	now := time.Now()
	if err := s.Ingest(sample(now.Add(-3*time.Minute), "100", "100"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Ingest(sample(now, "200", "200"), now); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot(now).Ports["port-a"].InBps != nil {
		t.Fatal("long gap presented as recent traffic")
	}
	if len(s.Snapshot(now.Add(25*time.Hour)).Ports) != 0 {
		t.Fatal("expired series remained visible")
	}
	s.mu.Lock()
	for i := 0; i < MaxPoints+2; i++ {
		s.state.Sessions["peer-a"] = append(s.state.Sessions["peer-a"], SessionPoint{ObservedAt: now.Add(time.Duration(i-MaxPoints) * time.Millisecond)})
	}
	s.prune(now)
	count := len(s.state.Sessions["peer-a"]) + len(s.state.Ports["port-a"])
	s.mu.Unlock()
	if count > MaxPoints {
		t.Fatal("global point retention not bounded")
	}
}

func TestPerSeriesHistoryBoundSurvivesAutomaticCompaction(t *testing.T) {
	s, dir := openTest(t)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < MaxPointsPerSeries+3; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		if err := s.Ingest(sample(at, strconv.Itoa(i*100), "0"), at); err != nil {
			t.Fatal(err)
		}
	}
	points := s.Series("port-a", base, base.Add(time.Hour))
	if len(points) != MaxPointsPerSeries || !points[0].ObservedAt.Equal(base.Add(3*time.Second)) {
		t.Fatal("per-series retention limit not enforced")
	}
	s.Close()
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if len(again.Series("port-a", base, base.Add(time.Hour))) != MaxPointsPerSeries {
		t.Fatal("compaction/replay violated retention bound")
	}
}

func TestNewKnownSessionRequiresObservedIdentity(t *testing.T) {
	s, _ := openTest(t)
	now := time.Now()
	batch := Batch{RouterID: "r1", ObservedAt: now, Sessions: []SessionSample{{PeerID: "p1", State: "Established"}}}
	if err := s.Ingest(batch, now); err == nil {
		t.Fatal("new known session persisted without identity")
	}
	batch.Sessions[0].NeighborAddress = "192.0.2.2"
	batch.Sessions[0].NeighborASN = 64513
	if err := s.Ingest(batch, now); err != nil {
		t.Fatal(err)
	}
	stored := s.Snapshot(now).Sessions["p1"]
	if stored.NeighborAddress != "192.0.2.2" || stored.NeighborASN != 64513 {
		t.Fatal("observed identity not persisted")
	}
	batch.Sessions[0].NeighborASN = 64514
	if err := s.Ingest(batch, now); err == nil {
		t.Fatal("conflicting same-time identity treated as idempotent retry")
	}
}
