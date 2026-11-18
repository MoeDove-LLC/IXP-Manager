package traffic

import (
	"errors"
	"time"
)

// A calendar day need not start at local00:00 (some DST transitions skip it).
// Find the first/last instant of the declared local date rather than guessing a
// 24-hour duration or moving a report label to a different date.
func legacyCalendarBounds(label string, loc *time.Location) (time.Time, time.Time, error) {
	date, e := time.Parse("2006-01-02", label)
	if e != nil {
		return time.Time{}, time.Time{}, e
	}
	inside := time.Date(date.Year(), date.Month(), date.Day(), 12, 0, 0, 0, loc)
	same := func(at time.Time) bool { return at.In(loc).Format("2006-01-02") == label }
	if !same(inside) {
		return time.Time{}, time.Time{}, errors.New("declared local calendar date has no resolvable interval")
	}
	left, right := inside, inside
	for i := 0; i < 73 && same(left); i++ {
		left = left.Add(-time.Hour)
	}
	for i := 0; i < 73 && same(right); i++ {
		right = right.Add(time.Hour)
	}
	if same(left) || same(right) {
		return time.Time{}, time.Time{}, errors.New("legacy calendar day exceeds supported timezone bound")
	}
	lo, hi := left.Unix(), inside.Unix()
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if same(time.Unix(mid, 0)) {
			hi = mid
		} else {
			lo = mid
		}
	}
	start := time.Unix(hi, 0).UTC()
	lo, hi = inside.Unix(), right.Unix()
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if same(time.Unix(mid, 0)) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return start, time.Unix(hi, 0).UTC(), nil
}
