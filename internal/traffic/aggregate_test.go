package traffic

import (
	"math"
	"testing"
	"time"
)

func testKey(port string) Key {
	return Key{PortID: port, SourceID: "switch:s", MemberID: "m", OwnershipRevision: "own", CounterEpoch: "epoch", Dimensions: Dimensions{MemberRevision: "member-generation", ConnectionIDs: []string{"c"}}, Metric: Octets}
}
func TestExactCrossBucketConservation(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 4, 50, 0, time.UTC)
	rows := Interval(testKey("p"), base, base.Add(30*time.Second), CounterPair{In: "0", Out: "0"}, CounterPair{In: "18446744073709551615", Out: "7"}, 0, true)
	for _, res := range []string{FiveMinutes, Hour, Day} {
		in, out := "0", "0"
		for _, r := range rows {
			if r.Resolution == res {
				in = AddDecimal(in, r.In.Total)
				out = AddDecimal(out, r.Out.Total)
			}
		}
		if in != "18446744073709551615" || out != "7" {
			t.Fatalf("%s conservation: %s/%s", res, in, out)
		}
	}
	q := Query{Scope: "port", ID: "p", Metric: Octets, Resolution: FiveMinutes, From: base.Truncate(5 * time.Minute), To: base.Truncate(5 * time.Minute).Add(10 * time.Minute)}
	r, e := Aggregate(rows, q)
	if e != nil || r.Summary.In.Total == nil || *r.Summary.In.Total != "18446744073709551615" || !r.Summary.UniformAllocation {
		t.Fatalf("exact aggregation: %+v %v", r, e)
	}
}
func TestUnknownZeroResetWrapAndBound(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name             string
		old, next        CounterPair
		speed            uint64
		same             bool
		want             string
		reset, wrap, gap bool
	}{{"zero", CounterPair{In: "1"}, CounterPair{In: "1"}, 1000, true, "0", false, false, false}, {"missing", CounterPair{}, CounterPair{In: "1"}, 1000, true, "", false, false, true}, {"reset", CounterPair{In: "100"}, CounterPair{In: "1"}, 1000, true, "", true, false, false}, {"wrap", CounterPair{In: "4294967290", Bits: 32}, CounterPair{In: "4", Bits: 32}, 1, true, "10", false, true, false}, {"ambiguous", CounterPair{In: "4294967290", Bits: 32}, CounterPair{In: "4", Bits: 32}, 1000, true, "", false, false, true}, {"32-bit-speed-bound", CounterPair{In: "0", Bits: 32}, CounterPair{In: "8000000", Bits: 32}, 1, true, "", false, false, true}, {"lineage", CounterPair{In: "1"}, CounterPair{In: "2"}, 1000, false, "", true, false, false}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := Interval(testKey("p"), at, at.Add(time.Minute), c.old, c.next, c.speed, c.same)
			m := rows[0].In
			if m.Total != c.want || (m.Reset > 0) != c.reset || (m.Wrap > 0) != c.wrap || (m.Gap > 0) != c.gap {
				t.Fatalf("%+v", m)
			}
		})
	}
}
func TestGroupPeaksAlignAndMissingPortIsNotZero(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []Row{}
	for minute := 0; minute < 10; minute++ {
		for _, port := range []string{"p1", "p2"} {
			delta := uint64(10)
			if port == "p1" && minute < 5 || port == "p2" && minute >= 5 {
				delta = 100
			}
			old := CounterPair{In: "0", Out: "0"}
			next := CounterPair{In: AddDecimal("0", uintString(delta)), Out: "0"}
			rows = append(rows, Interval(testKey(port), at.Add(time.Duration(minute)*time.Minute), at.Add(time.Duration(minute+1)*time.Minute), old, next, 1000, true)...)
		}
	}
	q := Query{Scope: "member", ID: "m", Metric: Octets, Resolution: FiveMinutes, From: at, To: at.Add(10 * time.Minute), ExpectedPortIDs: []string{"p1", "p2"}}
	r, e := Aggregate(rows, q)
	if e != nil {
		t.Fatal(e)
	}
	want := float64(550) * 8 / 300
	if r.Summary.In.MaxRate == nil || math.Abs(*r.Summary.In.MaxRate-want) > 0.000001 || r.Summary.In.RateCoveredMillis != 600000 {
		t.Fatalf("aligned peak: %+v", r.Summary)
	}
	q.ExpectedPortIDs = append(q.ExpectedPortIDs, "missing")
	r, e = Aggregate(rows, q)
	if e != nil || r.Summary.In.MaxRate != nil || r.Summary.In.AverageRate != nil || r.Summary.In.Total == nil || !r.Summary.Partial {
		t.Fatalf("missing falsely zero: %+v %v", r, e)
	}
}
func uintString(v uint64) string {
	if v == 100 {
		return "100"
	}
	return "10"
}
func TestACLFiltersBeforeAggregateAndCoreSideRequired(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	key := testKey("p")
	rows := Interval(key, at, at.Add(time.Minute), CounterPair{In: "1"}, CounterPair{In: "10"}, 1000, true)
	q := Query{Scope: "member", ID: "m", MemberID: "m", MemberRevision: "new-generation", Metric: Octets, Resolution: FiveMinutes, From: at, To: at.Add(5 * time.Minute)}
	r, e := Aggregate(rows, q)
	if e != nil || r.Summary.In.Total != nil {
		t.Fatalf("old member leaked: %+v %v", r, e)
	}
	q.MemberRevision = "member-generation"
	q.OwnershipRevision = "different-binding"
	r, e = Aggregate(rows, q)
	if e != nil || r.Summary.In.Total != nil {
		t.Fatal("old ownership leaked")
	}
	q.Scope = "core-bundle"
	q.CoreSide = ""
	if _, e = Aggregate(rows, q); e == nil {
		t.Fatal("core sides may not be counted together")
	}
}

func TestMixedDirectionalWidthsDoNotDiscardHCOrResetOtherDirection(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	k := testKey("p")
	k.Metric = Octets
	old := CounterPair{In: "4294967300", Out: "4294967290", InBits: 64, OutBits: 32}
	next := CounterPair{In: "4294967400", Out: "4", InBits: 64, OutBits: 32}
	rows := Interval(k, at, at.Add(time.Minute), old, next, 1, true)
	if rows[0].In.Total != "100" || rows[0].Out.Total != "10" || rows[0].Out.Wrap != 1 {
		t.Fatalf("mixed widths %+v", rows[0])
	}
	next.OutBits = 64
	rows = Interval(k, at, at.Add(time.Minute), old, next, 1, true)
	if rows[0].In.Total != "100" || rows[0].In.Reset != 0 || rows[0].Out.Total != "" || rows[0].Out.Reset != 1 {
		t.Fatalf("directional width change %+v", rows[0])
	}
	q := Query{Scope: "port", ID: "p", Metric: Octets, Resolution: FiveMinutes, From: at, To: at.Add(5 * time.Minute)}
	result, e := Aggregate(rows, q)
	if e != nil || result.Summary.PeakResolutionSeconds != 60 {
		t.Fatalf("actual sampled resolution %+v %v", result.Summary, e)
	}
}

func TestRequestedPacketCountsAreNotCappedByWireSpeed(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	k := testKey("p")
	k.Metric = Unicast
	rows := Interval(k, at, at.Add(time.Minute), CounterPair{In: "0", Out: "0"}, CounterPair{In: "0", Out: "9999999999"}, 1, true)
	if rows[0].Out.Total != "9999999999" {
		t.Fatalf("requested transmissions incorrectly capped: %+v", rows[0].Out)
	}
	rows = Interval(k, at, at.Add(time.Minute), CounterPair{In: "0", Out: "0", Bits: 32}, CounterPair{In: "1", Out: "1", Bits: 32}, 1, true)
	if rows[0].In.Total != "" || rows[0].Out.Total != "" || rows[0].Out.Ambiguous != 1 || rows[0].Out.LowerBound != "1" {
		t.Fatal("unknown event-rate bound falsely proved a precise Counter32 delta")
	}
}

func TestCounter32LowerBoundsConserveWithoutPretendingExactRates(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 4, 50, 0, time.UTC)
	k := testKey("p")
	k.Metric = Errors
	rows := Interval(k, at, at.Add(30*time.Second), CounterPair{In: "4294967290", Out: "100", Bits: 32}, CounterPair{In: "4", Out: "1", Bits: 32}, 1000, true)
	q := Query{Scope: "port", ID: "p", Metric: Errors, Resolution: FiveMinutes, From: at.Truncate(5 * time.Minute), To: at.Truncate(5 * time.Minute).Add(10 * time.Minute)}
	r, e := Aggregate(rows, q)
	if e != nil {
		t.Fatal(e)
	}
	if r.Summary.In.Total != nil || r.Summary.In.LowerBoundTotal == nil || *r.Summary.In.LowerBoundTotal != "10" || r.Summary.In.AverageRate != nil || r.Summary.In.MaxRate != nil || r.Summary.In.CoveredMillis != 0 || r.Summary.In.AmbiguousIntervals != 2 || !r.Summary.UniformAllocation {
		t.Fatalf("ambiguous lower bound %+v", r.Summary)
	}
	if r.Summary.Out.LowerBoundTotal != nil || r.Summary.Out.ResetIntervals == 0 {
		t.Fatal("identified reset became a false large modulo lower bound")
	}
	sum := "0"
	for _, b := range r.Buckets {
		if b.In.LowerBoundTotal != nil {
			sum = AddDecimal(sum, *b.In.LowerBoundTotal)
		}
	}
	if sum != "10" {
		t.Fatalf("split lower bound duplicated: %s", sum)
	}
	rows = Interval(k, at, at.Add(30*time.Second), CounterPair{In: "1", Bits: 32}, CounterPair{In: "4", Bits: 32}, 1000, false)
	r, e = Aggregate(rows, q)
	if e != nil || r.Summary.In.LowerBoundTotal != nil {
		t.Fatal("changed epoch received a counter lower bound")
	}
}

func TestCounter64OctetsMayExceedNominalInventorySpeed(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	k := testKey("p")
	k.SourceID = "router:r"
	rows := Interval(k, at, at.Add(time.Minute), CounterPair{In: "9007199254740992", Out: "9007199254740992", Bits: 64}, CounterPair{In: "9007208254740992", Out: "9007208254740992", Bits: 64}, 1000, true)
	q := Query{Scope: "port", ID: "p", Metric: Octets, Resolution: FiveMinutes, From: at, To: at.Add(5 * time.Minute)}
	result, e := Aggregate(rows, q)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range []Direction{result.Summary.In, result.Summary.Out} {
		if d.Total == nil || *d.Total != "9000000000" || d.AverageRate == nil || *d.AverageRate != 1200000000 || d.CoveredMillis != 60000 || d.GapIntervals != 0 {
			t.Fatalf("valid Counter64 delta discarded by nominal 1G speed: %+v", d)
		}
	}
}
