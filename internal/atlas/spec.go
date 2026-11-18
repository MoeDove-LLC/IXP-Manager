package atlas

import (
	"errors"
	"net/netip"
	"regexp"
	"sort"
	"time"
)

var markerPattern = regexp.MustCompile(`^MoeDove Atlas [A-Za-z0-9_-]{22}/[A-Za-z0-9_-]{22}/[A-Za-z0-9_-]{22}$`)

func ValidTarget(raw string, af int) bool {
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip.String() != raw || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.Zone() != "" || ip.Is4() != (af == 4) || (af != 4 && af != 6) {
		return false
	}
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "198.18.0.0/15", "240.0.0.0/4", "2001::/32", "2001:10::/28", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return true
}
func NormalizeSpec(s Spec, now time.Time) (Spec, Estimate, error) {
	var estimate Estimate
	if s.Type != "ping" && s.Type != "traceroute" {
		return s, estimate, errors.New("Atlas type must be ping or traceroute")
	}
	if !ValidTarget(s.Target, s.AF) {
		return s, estimate, errors.New("Atlas target must be a canonical public unicast IP in the selected family")
	}
	if len(s.ProbeIDs) < 1 || len(s.ProbeIDs) > MaxProbes {
		return s, estimate, errors.New("select 1..20 explicit Atlas probes")
	}
	s.ProbeIDs = append([]int64(nil), s.ProbeIDs...)
	sort.Slice(s.ProbeIDs, func(i, j int) bool { return s.ProbeIDs[i] < s.ProbeIDs[j] })
	for i, id := range s.ProbeIDs {
		if id < 1 || id > 2147483647 || i > 0 && id == s.ProbeIDs[i-1] {
			return s, estimate, errors.New("probe IDs must be positive, bounded and distinct")
		}
	}
	unit := 3
	if s.Type == "traceroute" {
		unit = 30
	}
	samples := len(s.ProbeIDs)
	if s.IsOneoff {
		if s.IntervalSeconds != 0 || s.DurationSeconds != 0 {
			return s, estimate, errors.New("one-off measurements cannot set interval or duration")
		}
		unit *= 2
	} else {
		if s.IntervalSeconds < 300 || s.IntervalSeconds > 3600 || s.DurationSeconds < s.IntervalSeconds || s.DurationSeconds > 86400 {
			return s, estimate, errors.New("recurring interval must be300..3600 seconds and duration between one interval and86400 seconds")
		}
		samples *= ((s.DurationSeconds+s.IntervalSeconds-1)/s.IntervalSeconds + 1)
	}
	if samples > MaxSamples || samples*unit > MaxEstimatedCredits {
		return s, estimate, errors.New("measurement exceeds1000 estimated samples or20000 estimated credits")
	}
	if !s.StartAt.IsZero() && (s.StartAt.Before(now.Add(-time.Minute)) || s.StartAt.After(now.Add(7*24*time.Hour))) {
		return s, estimate, errors.New("startAt must be near now or within7 days")
	}
	if s.Description != "" && !markerPattern.MatchString(s.Description) {
		return s, estimate, errors.New("measurement description must be a managed ownership marker")
	}
	estimate = Estimate{Samples: samples, Credits: samples * unit, UnitCost: unit, Basis: "Conservative result-count estimate using fixed3 packets/64 bytes; one-off costs double. This is not measured expenditure."}
	return s, estimate, nil
}
func Owns(m Measurement, s Spec) bool {
	return MatchesIntent(m, s) && m.IsPublic == s.IsPublic && m.IsOneoff == s.IsOneoff && (s.IsOneoff || m.IntervalSeconds == s.IntervalSeconds)
}

// MatchesIntent is ownership evidence, distinct from full policy conformity.
// A provider-created measurement with wrong privacy/timing must still be safely
// stoppable once its unguessable local intent and target identity are verified.
func MatchesIntent(m Measurement, s Spec) bool {
	return m.ID > 0 && markerPattern.MatchString(s.Description) && m.Description == s.Description && m.Type == s.Type && m.AF == s.AF && m.Target == s.Target
}
