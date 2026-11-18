package traffic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"sort"
	"strconv"
	"time"
)

// Measure is persisted exact arithmetic. MaxRate describes a sampled interval,
// never an instantaneous line-rate peak. Totals stay decimal throughout storage.
type Measure struct {
	Total                   string `json:"total,omitempty"`
	LowerBound              string `json:"lowerBound,omitempty"`
	Ambiguous               uint64 `json:"ambiguous,omitempty"`
	CoveredMillis           uint64 `json:"coveredMillis"`
	Valid, Reset, Wrap, Gap uint64
	MaxRate                 *float64   `json:"maxRate,omitempty"`
	MaxAt                   *time.Time `json:"maxAt,omitempty"`
	MaxIntervalMillis       uint64     `json:"maxIntervalMillis,omitempty"`
}
type Row struct {
	Key        Key       `json:"key"`
	Resolution string    `json:"resolution"`
	Start      time.Time `json:"start"`
	In         Measure   `json:"in"`
	Out        Measure   `json:"out"`
	Uniform    bool      `json:"uniform"`
}

func Duration(res string) time.Duration {
	switch res {
	case FiveMinutes:
		return 5 * time.Minute
	case Hour:
		return time.Hour
	case Day:
		return 24 * time.Hour
	}
	return 0
}
func RowID(r Row) string {
	b, _ := json.Marshal(r.Key)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]) + "/" + strconv.FormatInt(r.Start.Unix(), 10)
}
func KeyID(k Key) string {
	b, _ := json.Marshal(k)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func AddDecimal(a, b string) string {
	x, _ := new(big.Int).SetString(a, 10)
	if x == nil {
		x = new(big.Int)
	}
	y, _ := new(big.Int).SetString(b, 10)
	if y == nil {
		y = new(big.Int)
	}
	return x.Add(x, y).String()
}
func mergeMeasure(a *Measure, b Measure) {
	if b.Total != "" {
		a.Total = AddDecimal(a.Total, b.Total)
	}
	if b.LowerBound != "" {
		a.LowerBound = AddDecimal(a.LowerBound, b.LowerBound)
	}
	a.Ambiguous += b.Ambiguous
	a.CoveredMillis += b.CoveredMillis
	a.Valid += b.Valid
	a.Reset += b.Reset
	a.Wrap += b.Wrap
	a.Gap += b.Gap
	if b.MaxRate != nil && (a.MaxRate == nil || *b.MaxRate > *a.MaxRate) {
		v := *b.MaxRate
		a.MaxRate = &v
		a.MaxIntervalMillis = b.MaxIntervalMillis
		if b.MaxAt != nil {
			t := *b.MaxAt
			a.MaxAt = &t
		}
	}
}
func Merge(a *Row, b Row) {
	mergeMeasure(&a.In, b.In)
	mergeMeasure(&a.Out, b.Out)
	a.Uniform = a.Uniform || b.Uniform
}
func CloneRow(r Row) Row {
	r.Key.Dimensions.ConnectionIDs = append([]string(nil), r.Key.Dimensions.ConnectionIDs...)
	for _, m := range []*Measure{&r.In, &r.Out} {
		if m.MaxRate != nil {
			v := *m.MaxRate
			m.MaxRate = &v
		}
		if m.MaxAt != nil {
			v := *m.MaxAt
			m.MaxAt = &v
		}
	}
	return r
}
func factor(metric string) float64 {
	if metric == Octets {
		return 8
	}
	return 1
}
func counterValue(v string, bits int) (uint64, bool) {
	if v == "" {
		return 0, false
	}
	n, e := strconv.ParseUint(v, 10, bits)
	return n, e == nil && strconv.FormatUint(n, 10) == v
}
func intervalMeasure(old, next string, bits int, dt time.Duration, speed uint64, metric string, same bool) Measure {
	m := Measure{}
	if dt == 0 {
		m.Gap = 1
		return m
	}
	if old == "" || next == "" {
		m.Gap = 1
		return m
	}
	if !same {
		m.Reset = 1
		return m
	}
	if dt < time.Second || dt > 120*time.Second {
		m.Gap = 1
		return m
	}
	a, ok := counterValue(old, bits)
	b, ok2 := counterValue(next, bits)
	if !ok || !ok2 {
		m.Gap = 1
		return m
	}
	// IF-MIB output packet counters count requests, including packets never
	// transmitted. Error/discard events likewise have no generic line-rate cap.
	// Without a separately verified event-rate bound, Counter32 cannot exclude
	// one or more hidden full wraps, even when its next value is larger.
	max := uint64(math.MaxUint64)
	if bits == 32 {
		max = math.MaxUint32
	}
	var delta uint64
	if b >= a {
		delta = b - a
	} else if a > max-max/16 && b < max/16 {
		delta = max - a + 1 + b
		m.Wrap = 1
	} else {
		m.Reset = 1
		return m
	}
	if bits == 32 && metric != Octets {
		m.LowerBound = strconv.FormatUint(delta, 10)
		m.Ambiguous = 1
		m.Wrap = 0
		return m
	}
	bound := dt.Seconds() * float64(speed) * 1e6 / 8
	// Inventory speed is not a physical cap for Counter64 interface counters:
	// Linux tx_bytes also counts data handed to the device, before wire success.
	// Counter32 octet wrap inference retains its explicit speed-bound premise.
	if metric == Octets && bits == 32 && (speed == 0 || bound >= 4294967296 || float64(delta) > bound) {
		m.Gap = 1
		m.Wrap = 0
		return m
	}
	m.Total = strconv.FormatUint(delta, 10)
	m.CoveredMillis = uint64(dt / time.Millisecond)
	m.Valid = 1
	v := float64(delta) * factor(metric) / dt.Seconds()
	m.MaxRate = &v
	m.MaxIntervalMillis = uint64(dt / time.Millisecond)
	return m
}

// Interval builds all retention tiers directly from the same accepted WAL event.
// Every tier conserves its exact integer delta when an interval crosses UTC bins.
func Interval(k Key, from, to time.Time, old, next CounterPair, speed uint64, same bool) []Row {
	observedAt := to.UTC()
	rawDuration := to.Sub(from)
	// Coverage is expressed in integer milliseconds. Normalize once before
	// splitting so fractional boundary fragments cannot acquire zero duration
	// while carrying an exact positive delta, and split durations still conserve.
	from = from.UTC().Truncate(time.Millisecond)
	to = to.UTC().Truncate(time.Millisecond)
	dt := to.Sub(from)
	if rawDuration < time.Second || rawDuration > 120*time.Second {
		dt = rawDuration
	}
	in := intervalMeasure(old.In, next.In, next.Width(true), dt, speed, k.Metric, same && old.Width(true) == next.Width(true))
	out := intervalMeasure(old.Out, next.Out, next.Width(false), dt, speed, k.Metric, same && old.Width(false) == next.Width(false))
	if in.MaxRate != nil {
		t := observedAt
		in.MaxAt = &t
	}
	if out.MaxRate != nil {
		t := observedAt
		out.MaxAt = &t
	}
	rows := []Row{}
	for _, res := range []string{FiveMinutes, Hour, Day} {
		step := Duration(res)
		start := from.UTC()
		end := to.UTC()
		if dt < time.Second || dt > 120*time.Second || !same {
			start = end
			in.Total = ""
			out.Total = ""
		}
		if start.Equal(end) {
			rows = append(rows, Row{Key: k, Resolution: res, Start: end.Truncate(step), In: in, Out: out})
			continue
		}
		remainingIn, remainingOut := in.Total, out.Total
		remainingLowerIn, remainingLowerOut := in.LowerBound, out.LowerBound
		for cursor := start; cursor.Before(end); {
			bin := cursor.Truncate(step)
			stop := bin.Add(step)
			if stop.After(end) {
				stop = end
			}
			piece := stop.Sub(cursor)
			r := Row{Key: k, Resolution: res, Start: bin, Uniform: !start.Truncate(step).Equal(end.Add(-time.Nanosecond).Truncate(step))}
			r.In = portion(in, piece, dt, stop.Equal(end), &remainingIn, &remainingLowerIn)
			r.Out = portion(out, piece, dt, stop.Equal(end), &remainingOut, &remainingLowerOut)
			rows = append(rows, r)
			cursor = stop
		}
	}
	return rows
}
func portion(m Measure, piece, total time.Duration, last bool, remaining, remainingLower *string) Measure {
	r := m
	if m.LowerBound != "" {
		r.LowerBound = splitDecimal(m.LowerBound, piece, total, last, remainingLower)
	}
	if m.Total == "" {
		return r
	}
	r.Total = splitDecimal(m.Total, piece, total, last, remaining)
	r.CoveredMillis = uint64(piece / time.Millisecond)
	return r
}
func splitDecimal(value string, piece, total time.Duration, last bool, remaining *string) string {
	n, _ := new(big.Int).SetString(value, 10)
	n.Mul(n, big.NewInt(int64(piece)))
	n.Div(n, big.NewInt(int64(total)))
	if last {
		n.SetString(*remaining, 10)
	}
	left, _ := new(big.Int).SetString(*remaining, 10)
	left.Sub(left, n)
	*remaining = left.String()
	return n.String()
}
func ValidateQuery(q Query) error {
	if q.ID == "" || len(q.ID) > 128 || q.From.IsZero() || !q.To.After(q.From) || Duration(q.Resolution) == 0 {
		return errors.New("invalid traffic scope, range or resolution")
	}
	switch q.Scope {
	case "port", "member", "switch", "rack", "location", "infrastructure", "connection":
	case "core-bundle":
		if q.CoreSide != "a" && q.CoreSide != "b" {
			return errors.New("core-bundle requires side a or b")
		}
	default:
		return errors.New("unsupported traffic scope")
	}
	switch q.Metric {
	case Octets, Unicast, Multicast, Broadcast, Errors, Discards:
	default:
		return errors.New("unsupported traffic metric")
	}
	d := Duration(q.Resolution)
	if q.From.UnixNano()%int64(d) != 0 || q.To.UnixNano()%int64(d) != 0 {
		return errors.New("traffic range must align to UTC resolution boundaries")
	}
	if q.To.Sub(q.From)/d > MaxQueryBuckets {
		return errors.New("traffic query exceeds 5000 buckets; choose a coarser resolution")
	}
	if q.MemberID != "" && q.MemberRevision == "" {
		return errors.New("member history requires its frozen member revision")
	}
	if len(q.ExpectedPortIDs) > 4096 {
		return errors.New("too many expected ports")
	}
	return nil
}
func Matches(k Key, q Query) bool {
	if k.Metric != q.Metric || q.MemberID != "" && (k.MemberID != q.MemberID || k.Dimensions.MemberRevision != q.MemberRevision) || q.MemberRevision != "" && k.Dimensions.MemberRevision != q.MemberRevision || q.OwnershipRevision != "" && k.OwnershipRevision != q.OwnershipRevision {
		return false
	}
	switch q.Scope {
	case "port":
		return k.PortID == q.ID
	case "member":
		return k.MemberID == q.ID
	case "switch":
		return k.Dimensions.SwitchID == q.ID
	case "rack":
		return k.Dimensions.RackID == q.ID
	case "location":
		return k.Dimensions.LocationID == q.ID
	case "infrastructure":
		return k.Dimensions.InfrastructureID == q.ID
	case "core-bundle":
		return k.Dimensions.CoreBundleID == q.ID && k.Dimensions.CoreSide == q.CoreSide
	case "connection":
		for _, id := range k.Dimensions.ConnectionIDs {
			if id == q.ID {
				return true
			}
		}
	}
	return false
}
func rate(total string, millis uint64, metric string) *float64 {
	if total == "" || millis == 0 {
		return nil
	}
	n, _ := new(big.Int).SetString(total, 10)
	v, _ := new(big.Float).SetInt(n).Float64()
	v *= factor(metric) * 1000 / float64(millis)
	return &v
}
func direction(m Measure, expected uint64, metric string) Direction {
	d := Direction{CoveredMillis: m.CoveredMillis, ExpectedMillis: expected, RateCoveredMillis: m.CoveredMillis, ValidIntervals: m.Valid, ResetIntervals: m.Reset, WrapIntervals: m.Wrap, GapIntervals: m.Gap, MaxRate: m.MaxRate, MaxAt: m.MaxAt}
	d.AmbiguousIntervals = m.Ambiguous
	if m.Total != "" {
		v := m.Total
		d.Total = &v
		d.AverageRate = rate(v, m.CoveredMillis, metric)
	}
	if m.LowerBound != "" {
		v := AddDecimal(m.Total, m.LowerBound)
		d.LowerBoundTotal = &v
	}
	if m.Ambiguous > 0 {
		d.AverageRate = nil
		d.MaxRate = nil
		d.MaxAt = nil
		d.RateCoveredMillis = 0
	}
	if expected > 0 {
		d.Coverage = math.Min(1, float64(d.CoveredMillis)/float64(expected))
	}
	return d
}

// Aggregate filters before summation. Multi-port rates are only reported for
// complete aligned bins across explicitly expected ports, never an observed subset.
func Aggregate(rows []Row, q Query) (Result, error) {
	if e := ValidateQuery(q); e != nil {
		return Result{}, e
	}
	step := Duration(q.Resolution)
	result := Result{SourceKind: "counter", Scope: q.Scope, ID: q.ID, CoreSide: q.CoreSide, Metric: q.Metric, Resolution: q.Resolution, From: q.From.UTC(), To: q.To.UTC(), Notices: []string{}, Buckets: []Bucket{}}
	result.TotalUnit = "events"
	result.RateUnit = "events/s"
	if q.Metric == Octets {
		result.TotalUnit = "bytes"
		result.RateUnit = "bits/s"
	} else if q.Metric == Unicast || q.Metric == Multicast || q.Metric == Broadcast {
		result.TotalUnit = "packets"
		result.RateUnit = "packets/s"
	}
	ports := map[string]bool{}
	for _, id := range q.ExpectedPortIDs {
		ports[id] = true
	}
	if q.Scope == "port" {
		ports[q.ID] = true
	}
	expectedKnown := len(ports) > 0
	by := map[int64]map[string]Row{}
	for _, r := range rows {
		if r.Resolution != q.Resolution || r.Start.Before(q.From) || !r.Start.Before(q.To) || !Matches(r.Key, q) {
			continue
		}
		if expectedKnown && !ports[r.Key.PortID] {
			continue
		}
		if !expectedKnown {
			ports[r.Key.PortID] = true
		}
		at := r.Start.Unix()
		if by[at] == nil {
			by[at] = map[string]Row{}
		}
		old := by[at][r.Key.PortID]
		if old.Key.PortID == "" {
			old = CloneRow(r)
		} else {
			Merge(&old, r)
		}
		by[at][r.Key.PortID] = old
	}
	group := q.Scope != "port"
	if group && !expectedKnown {
		result.Notices = append(result.Notices, "Aggregate rates require an explicit expected-port set; observed subsets do not establish whole-group coverage.")
	}
	if q.Scope == "connection" {
		result.Notices = append(result.Notices, "Physical port counters may include other VLANs and connections; they are not per-VLAN traffic.")
	}
	result.Notices = append(result.Notices, "Totals contain only observed valid counter intervals. Missing coverage is unknown, not zero. Peaks describe sampled intervals or aligned bucket averages, never instantaneous peaks.")
	if q.Metric != Octets {
		result.Notices = append(result.Notices, "Packet/event counters are device-defined counts, not byte throughput. IF-MIB output packet counters include transmission requests that were discarded or not sent. Counter32 packet/event intervals without a verified maximum event rate retain only a modulo-counter lower bound, conditional on no unobserved reset; they do not contribute exact totals or rates.")
	}
	totalIn, totalOut := Measure{}, Measure{}
	rateIn, rateOut := Measure{}, Measure{}
	var summaryMaxIn, summaryMaxOut *float64
	var summaryAtIn, summaryAtOut *time.Time
	for at := q.From.UTC(); at.Before(q.To); at = at.Add(step) {
		b := Bucket{Start: at, End: at.Add(step), ExpectedPorts: len(ports), PeakMethod: "observed-interval"}
		mi, mo := Measure{}, Measure{}
		completeIn, completeOut := expectedKnown, expectedKnown
		for id := range ports {
			r, ok := by[at.Unix()][id]
			if !ok {
				completeIn = false
				completeOut = false
				continue
			}
			if r.In.Total != "" || r.Out.Total != "" || r.In.LowerBound != "" || r.Out.LowerBound != "" {
				b.ContributingPorts++
			}
			mergeMeasure(&mi, r.In)
			mergeMeasure(&mo, r.Out)
			b.UniformAllocation = b.UniformAllocation || r.Uniform
			completeIn = completeIn && r.In.CoveredMillis == uint64(step/time.Millisecond)
			completeOut = completeOut && r.Out.CoveredMillis == uint64(step/time.Millisecond)
		}
		expected := uint64(step/time.Millisecond) * uint64(len(ports))
		b.In = direction(mi, expected, q.Metric)
		b.Out = direction(mo, expected, q.Metric)
		b.PeakResolutionSeconds = peakSeconds(mi, mo)
		if group {
			b.PeakMethod = "aligned-bucket-average"
			b.PeakResolutionSeconds = int(step / time.Second)
			setGroup := func(d *Direction, m Measure, complete bool, rm *Measure, max **float64, maxAt **time.Time) {
				d.AverageRate = nil
				d.MaxRate = nil
				d.MaxAt = nil
				d.RateCoveredMillis = 0
				if complete && len(ports) > 0 {
					d.AverageRate = rate(m.Total, uint64(step/time.Millisecond), q.Metric)
					d.MaxRate = d.AverageRate
					t := at
					d.MaxAt = &t
					d.RateCoveredMillis = uint64(step / time.Millisecond)
					rm.Total = AddDecimal(rm.Total, m.Total)
					rm.CoveredMillis += d.RateCoveredMillis
					if d.MaxRate != nil && (*max == nil || *d.MaxRate > **max) {
						v := *d.MaxRate
						*max = &v
						*maxAt = &t
					}
				}
			}
			setGroup(&b.In, mi, completeIn, &rateIn, &summaryMaxIn, &summaryAtIn)
			setGroup(&b.Out, mo, completeOut, &rateOut, &summaryMaxOut, &summaryAtOut)
		}
		b.Partial = b.In.Coverage < 1 || b.Out.Coverage < 1
		mergeMeasure(&totalIn, mi)
		mergeMeasure(&totalOut, mo)
		result.Buckets = append(result.Buckets, b)
	}
	expected := uint64(q.To.Sub(q.From)/time.Millisecond) * uint64(len(ports))
	result.Summary = Bucket{Start: q.From.UTC(), End: q.To.UTC(), In: direction(totalIn, expected, q.Metric), Out: direction(totalOut, expected, q.Metric), ExpectedPorts: len(ports), PeakMethod: "observed-interval", PeakResolutionSeconds: peakSeconds(totalIn, totalOut)}
	seen := map[string]bool{}
	for _, p := range by {
		for id, r := range p {
			if r.In.Total != "" || r.Out.Total != "" || r.In.LowerBound != "" || r.Out.LowerBound != "" {
				seen[id] = true
			}
		}
	}
	result.Summary.ContributingPorts = len(seen)
	result.Summary.Partial = result.Summary.In.Coverage < 1 || result.Summary.Out.Coverage < 1
	for _, b := range result.Buckets {
		result.Summary.UniformAllocation = result.Summary.UniformAllocation || b.UniformAllocation
	}
	if group {
		result.Summary.PeakMethod = "aligned-bucket-average"
		result.Summary.PeakResolutionSeconds = int(step / time.Second)
		result.Summary.In.AverageRate = rate(rateIn.Total, rateIn.CoveredMillis, q.Metric)
		result.Summary.Out.AverageRate = rate(rateOut.Total, rateOut.CoveredMillis, q.Metric)
		result.Summary.In.RateCoveredMillis = rateIn.CoveredMillis
		result.Summary.Out.RateCoveredMillis = rateOut.CoveredMillis
		result.Summary.In.MaxRate = summaryMaxIn
		result.Summary.Out.MaxRate = summaryMaxOut
		result.Summary.In.MaxAt = summaryAtIn
		result.Summary.Out.MaxAt = summaryAtOut
	}
	for _, d := range []*Direction{&result.Summary.In, &result.Summary.Out} {
		if d.AmbiguousIntervals > 0 {
			d.AverageRate = nil
			d.MaxRate = nil
			d.MaxAt = nil
			d.RateCoveredMillis = 0
		}
	}
	return result, nil
}
func SortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool { a, b := RowID(rows[i]), RowID(rows[j]); return a < b })
}
func peakSeconds(a, b Measure) int {
	n := a.MaxIntervalMillis
	if b.MaxIntervalMillis > n {
		n = b.MaxIntervalMillis
	}
	return int((n + 999) / 1000)
}

// ValidateRow enforces persisted arithmetic bounds before any aggregation.
func ValidateRow(r Row) error {
	d := Duration(r.Resolution)
	if d == 0 || r.Start.IsZero() || r.Key.PortID == "" || len(r.Key.PortID) > 128 || r.Key.SourceID == "" || len(r.Key.Dimensions.ConnectionIDs) > 256 {
		return errors.New("invalid traffic archive row")
	}
	switch r.Key.Metric {
	case Octets, Unicast, Multicast, Broadcast, Errors, Discards:
	default:
		return errors.New("invalid archived metric")
	}
	for _, m := range []Measure{r.In, r.Out} {
		if m.CoveredMillis > uint64(d/time.Millisecond) || m.Valid > 100000 || m.Gap > 100000 || m.Reset > 100000 || m.Wrap > 100000 || m.Ambiguous > 100000 {
			return errors.New("invalid archived duration or interval count")
		}
		if m.Total != "" {
			n, ok := new(big.Int).SetString(m.Total, 10)
			if !ok || n.Sign() < 0 || n.String() != m.Total || len(m.Total) > 40 || m.CoveredMillis == 0 {
				return errors.New("invalid archived exact total")
			}
		} else if m.CoveredMillis != 0 {
			return errors.New("archive coverage without total")
		}
		if m.LowerBound != "" {
			n, ok := new(big.Int).SetString(m.LowerBound, 10)
			if !ok || n.Sign() < 0 || n.String() != m.LowerBound || len(m.LowerBound) > 40 || m.Ambiguous == 0 {
				return errors.New("invalid archived counter lower bound")
			}
		} else if m.Ambiguous != 0 {
			return errors.New("ambiguous interval missing lower bound")
		}
		if m.MaxRate != nil && (math.IsNaN(*m.MaxRate) || math.IsInf(*m.MaxRate, 0) || *m.MaxRate < 0) {
			return errors.New("invalid archived sampled rate")
		}
	}
	return nil
}
