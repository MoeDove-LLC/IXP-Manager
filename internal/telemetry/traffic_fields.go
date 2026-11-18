package telemetry

import (
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/moedove/ixp-manager/internal/traffic"
)

func samePortSample(a, b PortSample) bool {
	a = copyPortSample(a)
	b = copyPortSample(b)
	normalizePortSample(&a)
	normalizePortSample(&b)
	if a.CounterBits == 0 {
		a.CounterBits = 64
	}
	if b.CounterBits == 0 {
		b.CounterBits = 64
	}
	for _, p := range []*traffic.CounterPair{a.Unicast, a.Multicast, a.Broadcast, a.Errors, a.Discards, b.Unicast, b.Multicast, b.Broadcast, b.Errors, b.Discards} {
		if p != nil {
			p.InBits = p.Width(true)
			p.OutBits = p.Width(false)
			p.Bits = 64
		}
	}
	return reflect.DeepEqual(a, b)
}
func normalizePortSample(p *PortSample) {
	sort.Strings(p.Dimensions.ConnectionIDs)
	p.Dimensions.OwnershipSince = p.Dimensions.OwnershipSince.UTC()
}
func validateTrafficFields(p PortSample) error {
	d := p.Dimensions
	for _, v := range append([]string{p.MemberID, p.OwnershipRevision, d.MemberRevision, d.SwitchID, d.RackID, d.LocationID, d.InfrastructureID, d.CoreBundleID, d.CoreLinkID}, d.ConnectionIDs...) {
		if len(v) > 128 || strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("invalid frozen traffic dimension")
		}
	}
	if len(d.ConnectionIDs) > 256 {
		return errors.New("physical port has more than 256 connections; cannot freeze complete attribution")
	}
	seen := map[string]bool{}
	for _, v := range d.ConnectionIDs {
		if v == "" || seen[v] {
			return errors.New("connection IDs must be nonempty and distinct")
		}
		seen[v] = true
	}
	if d.CoreSide != "" && d.CoreSide != "a" && d.CoreSide != "b" {
		return errors.New("invalid core bundle side")
	}
	if d.CoreBundleID != "" && (d.CoreSide == "" || d.CoreLinkID == "") {
		return errors.New("core traffic requires bundle, link and side attribution")
	}
	if p.LinkSpeedMbps > 10000000 {
		return errors.New("link speed exceeds supported limit")
	}
	for _, pair := range []*traffic.CounterPair{p.Unicast, p.Multicast, p.Broadcast, p.Errors, p.Discards} {
		if pair == nil {
			continue
		}
		for _, bits := range []int{pair.Bits, pair.InBits, pair.OutBits} {
			if bits != 0 && bits != 32 && bits != 64 {
				return errors.New("optional counter bits must be 32 or 64")
			}
		}
		if pair.In == "" && pair.Out == "" {
			return errors.New("optional counter pair must contain an observed direction")
		}
		for i, v := range []string{pair.In, pair.Out} {
			if v != "" {
				if _, e := counter(v, pair.Width(i == 0)); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func copyPortSample(p PortSample) PortSample {
	p.Dimensions.ConnectionIDs = append([]string(nil), p.Dimensions.ConnectionIDs...)
	copyPair := func(pair *traffic.CounterPair) *traffic.CounterPair {
		if pair == nil {
			return nil
		}
		v := *pair
		return &v
	}
	p.Unicast = copyPair(p.Unicast)
	p.Multicast = copyPair(p.Multicast)
	p.Broadcast = copyPair(p.Broadcast)
	p.Errors = copyPair(p.Errors)
	p.Discards = copyPair(p.Discards)
	return p
}
