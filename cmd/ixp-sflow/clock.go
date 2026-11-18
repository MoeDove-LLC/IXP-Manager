package main

import (
	"errors"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
)

// bindingClock retains local monotonic instants. Only its server-aligned receipt
// estimate crosses the API; it is not a packet capture timestamp.
type bindingClock struct {
	binding          sflow.Binding
	responseAt       time.Time
	serverAtResponse time.Time
	deadline         time.Time
	uncertainty      time.Duration
}

func anchorBinding(b sflow.Binding, requestAt, responseAt time.Time) (bindingClock, error) {
	rtt := responseAt.Sub(requestAt)
	ttl := b.ExpiresAt.Sub(b.IssuedAt)
	if b.IssuedAt.IsZero() || ttl <= 0 || ttl > 5*time.Minute {
		return bindingClock{}, errors.New("invalid server-issued sFlow lease interval")
	}
	// Deduct the entire request interval for authorization. Taking an RTT/2
	// midpoint estimates wall time without extending the earliest safe expiry.
	if rtt < 0 || rtt > sflow.ControlTimeout || ttl-rtt <= time.Second {
		return bindingClock{}, &sflow.ControlError{StatusCode: 409, Temporary: true}
	}
	return bindingClock{binding: b, responseAt: responseAt, serverAtResponse: b.IssuedAt.Add(rtt / 2), deadline: responseAt.Add(ttl - rtt), uncertainty: rtt/2 + 100*time.Millisecond}, nil
}

func (clock bindingClock) receiptAt(local time.Time) (time.Time, bool) {
	elapsed := local.Sub(clock.responseAt)
	if elapsed < 0 || !local.Before(clock.deadline) {
		return time.Time{}, false
	}
	return clock.serverAtResponse.Add(elapsed).UTC(), true
}
