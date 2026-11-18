package provision

import (
	"errors"
	"testing"
)

func TestRFC1982SerialComparisonsAndWrap(t *testing.T) {
	for _, tc := range []struct {
		a, b      uint32
		want      int
		undefined bool
	}{
		{0, 0, 0, false}, {1, 0, 1, false}, {0, 1, -1, false},
		{0, ^uint32(0), 1, false}, {1, ^uint32(0), 1, false}, {^uint32(0), 1, -1, false},
		{2147483647, 0, 1, false}, {2147483648, 0, 0, true}, {0, 2147483648, 0, true},
		{2147483649, 0, -1, false}, {0, 2147483649, 1, false},
		{4294967295, 2147483647, 0, true}, {2147483647, 4294967295, 0, true},
	} {
		got, err := CompareSerial(tc.a, tc.b)
		if got != tc.want || errors.Is(err, ErrSerialUndefined) != tc.undefined {
			t.Fatalf("compare(%d,%d): %d %v", tc.a, tc.b, got, err)
		}
		reverse, reverseErr := CompareSerial(tc.b, tc.a)
		if reverse != -got || errors.Is(reverseErr, ErrSerialUndefined) != tc.undefined {
			t.Fatal("defined comparison is not antisymmetric")
		}
	}
	for _, tc := range []struct{ before, after uint32 }{{0, 1}, {1, 2}, {4294967294, 4294967295}, {4294967295, 1}} {
		if got := NextSOASerial(tc.before); got != tc.after {
			t.Fatal("wrong nonzero successor")
		}
		if order, err := CompareSerial(tc.after, tc.before); err != nil || order != 1 {
			t.Fatal("successor did not advance")
		}
	}
	// RFC 1982 ordering is intentionally non-transitive across a large span.
	for _, pair := range [][2]uint32{{1500000000, 0}, {3000000000, 1500000000}, {0, 3000000000}} {
		if order, err := CompareSerial(pair[0], pair[1]); err != nil || order != 1 {
			t.Fatal("serials were treated as ordinary integers")
		}
	}
}
