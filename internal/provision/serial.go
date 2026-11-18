package provision

import "errors"

// ErrSerialUndefined marks the exactly half-space pair for which RFC 1982
// defines neither serial as newer. It must never be resolved by integer order.
var ErrSerialUndefined = errors.New("DNS serial comparison is undefined at half the 32-bit sequence space")

// CompareSerial returns -1, 0 or 1 when a is older, equal or newer than b under
// RFC 1982 sections 3 and 7. The ordering is circular and is not a sorting order.
func CompareSerial(a, b uint32) (int, error) {
	if a == b {
		return 0, nil
	}
	delta := a - b
	if delta == uint32(1)<<31 {
		return 0, ErrSerialUndefined
	}
	if delta < uint32(1)<<31 {
		return 1, nil
	}
	return -1, nil
}

// NextSOASerial advances by one, or by two across 4294967295 -> 1. Skipping
// zero preserves this renderer's explicit nonzero serial contract; both steps
// are defined positive RFC 1982 increments. An existing baseline of zero is
// valid and advances to one.
func NextSOASerial(previous uint32) uint32 {
	next := previous + 1
	if next == 0 {
		return 1
	}
	return next
}
