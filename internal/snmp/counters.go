package snmp

import (
	"errors"
	"github.com/gosnmp/gosnmp"
	"github.com/moedove/ixp-manager/internal/traffic"
	"slices"
)

// A value is a counter only when the fixed MIB's wire type agrees. A Gauge32,
// unavailable OID, malformed integer or out-of-range value never becomes zero.
func typedCounter(p gosnmp.SnmpPDU, bits int) string {
	if bits == 64 && p.Type != gosnmp.Counter64 || bits == 32 && p.Type != gosnmp.Counter32 {
		return ""
	}
	return uintText(p, bits)
}
func chooseCounter(high, low gosnmp.SnmpPDU) (string, int) {
	if value := typedCounter(high, 64); value != "" {
		return value, 64
	}
	if value := typedCounter(low, 32); value != "" {
		return value, 32
	}
	return "", 0
}
func metricPair(highIn, highOut, lowIn, lowOut gosnmp.SnmpPDU) *traffic.CounterPair {
	in, inBits := chooseCounter(highIn, lowIn)
	out, outBits := chooseCounter(highOut, lowOut)
	if in == "" && out == "" {
		return nil
	}
	p := &traffic.CounterPair{In: in, Out: out}
	if inBits == 0 {
		p.Bits = outBits
	} else if outBits == 0 || inBits == outBits {
		p.Bits = inBits
	} else {
		p.InBits = inBits
		p.OutBits = outBits
	}
	return p
}
func validateMetricPair(p *traffic.CounterPair, fixed32 bool) error {
	if p == nil {
		return nil
	}
	if p.In == "" && p.Out == "" {
		return errors.New("unavailable counter metric must be omitted")
	}
	for _, bits := range []int{p.Bits, p.InBits, p.OutBits} {
		if bits != 0 && bits != 32 && bits != 64 {
			return errors.New("metric counter width must be32/64")
		}
	}
	for _, d := range []struct {
		value    string
		in       bool
		override int
	}{{p.In, true, p.InBits}, {p.Out, false, p.OutBits}} {
		if d.value == "" {
			if d.override != 0 {
				return errors.New("missing counter direction cannot declare an observed width")
			}
			continue
		}
		bits := p.Width(d.in)
		if fixed32 && bits != 32 {
			return errors.New("IF-MIB errors/discards are32-bit counters")
		}
		if _, err := parseCounter(d.value, bits); err != nil {
			return err
		}
	}
	return nil
}
func validateMetricAliases(pair *traffic.CounterPair, in, out string) error {
	if pair != nil && ((in != "" && in != pair.In) || (out != "" && out != pair.Out)) {
		return errors.New("legacy error/discard fields disagree with typed counters")
	}
	return nil
}

func clonePair(p *traffic.CounterPair) *traffic.CounterPair {
	if p == nil {
		return nil
	}
	out := *p
	return &out
}

// Clone detaches the optional counter pairs and other reference-bearing fields.
// Strings hold decimal values without precision conversion or secret material.
func (s Snapshot) Clone() Snapshot {
	out := s
	out.Interfaces = slices.Clone(s.Interfaces)
	for i := range out.Interfaces {
		p := &out.Interfaces[i]
		p.Unicast = clonePair(p.Unicast)
		p.Multicast = clonePair(p.Multicast)
		p.Broadcast = clonePair(p.Broadcast)
		p.Errors = clonePair(p.Errors)
		p.Discards = clonePair(p.Discards)
	}
	out.Entities = slices.Clone(s.Entities)
	out.Sensors = slices.Clone(s.Sensors)
	for i := range out.Sensors {
		if p := s.Sensors[i].Value; p != nil {
			v := *p
			out.Sensors[i].Value = &v
		}
	}
	out.FDB = slices.Clone(s.FDB)
	for i := range out.FDB {
		out.FDB[i].VLANIDs = slices.Clone(s.FDB[i].VLANIDs)
	}
	out.Warnings = slices.Clone(s.Warnings)
	return out
}
