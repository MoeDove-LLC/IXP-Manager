package sflow

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strconv"
)

var errMalformed = errors.New("invalid or unsupported bounded sFlow v5 datagram")

type cursor struct {
	b   []byte
	off int
	err error
}

func (c *cursor) take(n int) []byte {
	if c.err != nil || n < 0 || n > len(c.b)-c.off {
		c.err = errMalformed
		return nil
	}
	v := c.b[c.off : c.off+n]
	c.off += n
	return v
}
func (c *cursor) u32() uint32 {
	v := c.take(4)
	if v == nil {
		return 0
	}
	return binary.BigEndian.Uint32(v)
}
func (c *cursor) u64() string {
	v := c.take(8)
	if v == nil {
		return ""
	}
	return strconv.FormatUint(binary.BigEndian.Uint64(v), 10)
}
func (c *cursor) opaque(limit int) []byte {
	n := c.u32()
	if uint64(n) > uint64(limit) {
		c.err = errMalformed
		return nil
	}
	v := c.take(int(n))
	c.take((4 - int(n)%4) % 4)
	return v
}

// Parse rejects the complete datagram on malformed framing; callers must not
// consume a partially parsed result. Unknown TLVs are skipped by bounded lengths.
func Parse(raw []byte) (Datagram, error) {
	empty := Datagram{}
	if len(raw) < 28 || len(raw) > MaxDatagramBytes {
		return empty, errMalformed
	}
	c := cursor{b: raw}
	d := Datagram{Version: c.u32(), Flows: []Flow{}, Counters: []CounterSample{}}
	if d.Version != 5 {
		return empty, errMalformed
	}
	switch c.u32() {
	case 1:
		if a, ok := netip.AddrFromSlice(c.take(4)); ok {
			d.AgentAddress = a.String()
		} else {
			c.err = errMalformed
		}
	case 2:
		if a, ok := netip.AddrFromSlice(c.take(16)); ok && !a.Is4In6() {
			d.AgentAddress = a.String()
		} else {
			c.err = errMalformed
		}
	default:
		return empty, errMalformed
	}
	d.SubAgentID = c.u32()
	d.Sequence = c.u32()
	d.UptimeMS = c.u32()
	count := c.u32()
	if count > MaxSamples {
		return empty, errMalformed
	}
	compact, expanded := false, false
	for i := uint32(0); i < count && c.err == nil; i++ {
		tag := c.u32()
		body := c.opaque(MaxDatagramBytes)
		if c.err != nil {
			break
		}
		if tag>>12 != 0 {
			d.UnknownSamples++
			continue
		}
		format := tag & 4095
		if format == 1 || format == 2 {
			compact = true
		}
		if format == 3 || format == 4 {
			expanded = true
		}
		if compact && expanded {
			return empty, errMalformed
		}
		switch format {
		case 1, 3:
			f, err := parseFlow(body, format == 3)
			if err != nil {
				return empty, err
			}
			d.Flows = append(d.Flows, f)
			d.UnknownRecords += f.UnknownRecords
		case 2, 4:
			s, err := parseCounters(body, format == 4)
			if err != nil {
				return empty, err
			}
			d.Counters = append(d.Counters, s)
			d.UnknownRecords += s.UnknownRecords
		default:
			d.UnknownSamples++
		}
	}
	if c.err != nil || c.off != len(c.b) {
		return empty, errMalformed
	}
	if err := ValidateDatagram(d); err != nil {
		return empty, err
	}
	return d, nil
}
func source(c *cursor, expanded bool) (uint32, uint32) {
	v := c.u32()
	if expanded {
		return v, c.u32()
	}
	return v >> 24, v & 0xffffff
}
func iface(c *cursor, expanded bool) Interface {
	v := c.u32()
	if expanded {
		return Interface{v, c.u32()}
	}
	return Interface{v >> 30, v & 0x3fffffff}
}

func parseFlow(raw []byte, expanded bool) (Flow, error) {
	c := cursor{b: raw}
	f := Flow{Sequence: c.u32(), SampleCount: 1}
	f.SourceType, f.SourceIndex = source(&c, expanded)
	f.SamplingRate = c.u32()
	f.SamplePool = c.u32()
	f.Drops = c.u32()
	f.Input = iface(&c, expanded)
	f.Output = iface(&c, expanded)
	count := c.u32()
	if count > MaxRecords {
		return Flow{}, errMalformed
	}
	headerSeen, ethernetSeen, ipSeen, switchSeen := false, false, false, false
	for i := uint32(0); i < count && c.err == nil; i++ {
		tag := c.u32()
		payload := c.opaque(MaxDatagramBytes)
		if c.err != nil {
			break
		}
		r := cursor{b: payload}
		if tag>>12 != 0 {
			f.UnknownRecords++
			continue
		}
		switch tag & 4095 {
		case 1:
			if headerSeen || ethernetSeen || ipSeen {
				return Flow{}, errMalformed
			}
			headerSeen = true
			protocol := r.u32()
			f.PacketSize = r.u32()
			stripped := r.u32()
			header := r.opaque(MaxHeaderBytes)
			if r.err != nil || stripped > f.PacketSize || uint64(len(header))+uint64(stripped) > uint64(f.PacketSize) {
				return Flow{}, errMalformed
			}
			f.UnattributedReason = decodeHeader(&f, protocol, header)
		case 2:
			if headerSeen || ethernetSeen {
				return Flow{}, errMalformed
			}
			ethernetSeen = true
			f.PacketSize = r.u32()
			src := r.take(8)
			dst := r.take(8)
			typ := r.u32()
			if r.err == nil {
				f.SourceMAC = net.HardwareAddr(src[:6]).String()
				f.DestinationMAC = net.HardwareAddr(dst[:6]).String()
				family := 0
				if typ == 0x800 {
					family = 4
				}
				if typ == 0x86dd {
					family = 6
				}
				if f.Family != 0 && f.Family != family {
					return Flow{}, errMalformed
				}
				f.Family = family
			}
		case 3, 4:
			if headerSeen || ipSeen {
				return Flow{}, errMalformed
			}
			ipSeen = true
			size := r.u32()
			proto := r.u32()
			family := 4
			addressBytes := 8
			if tag&4095 == 4 {
				family = 6
				addressBytes = 32
			}
			r.take(addressBytes)
			sp, dp, flags := r.u32(), r.u32(), r.u32()
			r.u32()
			if sp > 65535 || dp > 65535 || flags > 255 || proto > 255 {
				return Flow{}, errMalformed
			}
			if f.Family != 0 && f.Family != family {
				return Flow{}, errMalformed
			}
			f.Family = family
			if !ethernetSeen {
				f.PacketSize = size
			}
			f.SampledTCP179Activity = proto == 6 && tcp179(uint16(sp), uint16(dp), byte(flags))
		case 1001:
			if switchSeen {
				return Flow{}, errMalformed
			}
			switchSeen = true
			in, priorityIn, out, priorityOut := r.u32(), r.u32(), r.u32(), r.u32()
			if in > 4095 || out > 4095 || priorityIn > 7 || priorityOut > 7 {
				return Flow{}, errMalformed
			}
			f.InputVLAN = &in
			f.OutputVLAN = &out
		default:
			f.UnknownRecords++
			continue
		}
		if r.err != nil {
			return Flow{}, errMalformed
		}
		if r.off < len(r.b) {
			f.UnknownRecords++
		}
	}
	if c.err != nil {
		return Flow{}, errMalformed
	}
	if c.off < len(c.b) {
		f.UnknownRecords++
	}
	if !headerSeen && !ethernetSeen && !ipSeen {
		f.UnattributedReason = "missing-packet-record"
	}
	if f.SamplingRate == 0 {
		f.UnattributedReason = "unknown-sampling-rate"
	} else if f.PacketSize == 0 {
		f.UnattributedReason = "unknown-packet-size"
	} else {
		f.EstimatedBytes = strconv.FormatUint(uint64(f.PacketSize)*uint64(f.SamplingRate), 10)
		f.EstimatedPackets = strconv.FormatUint(uint64(f.SamplingRate), 10)
	}
	if f.UnattributedReason == "" && (f.SourceMAC == "" || f.DestinationMAC == "") {
		f.UnattributedReason = "missing-mac-metadata"
	}
	if f.UnattributedReason == "" && f.Family == 0 {
		f.UnattributedReason = "unsupported-network-protocol"
	}
	if f.SourceType > 2 && f.UnattributedReason == "" {
		f.UnattributedReason = "unsupported-source-type"
	}
	return f, nil
}

func parseCounters(raw []byte, expanded bool) (CounterSample, error) {
	c := cursor{b: raw}
	s := CounterSample{Sequence: c.u32(), Interfaces: []InterfaceCounters{}}
	s.SourceType, s.SourceIndex = source(&c, expanded)
	count := c.u32()
	if count > MaxRecords {
		return CounterSample{}, errMalformed
	}
	for i := uint32(0); i < count && c.err == nil; i++ {
		tag := c.u32()
		payload := c.opaque(MaxDatagramBytes)
		if c.err != nil {
			break
		}
		if tag != 1 {
			s.UnknownRecords++
			continue
		}
		r := cursor{b: payload}
		v := InterfaceCounters{IfIndex: r.u32(), IfType: r.u32(), IfSpeed: r.u64(), IfDirection: r.u32(), IfStatus: r.u32(), InOctets: r.u64(), InUnicast: r.u32(), InMulticast: r.u32(), InBroadcast: r.u32(), InDiscards: r.u32(), InErrors: r.u32(), InUnknownProtocols: r.u32(), OutOctets: r.u64(), OutUnicast: r.u32(), OutMulticast: r.u32(), OutBroadcast: r.u32(), OutDiscards: r.u32(), OutErrors: r.u32(), Promiscuous: r.u32()}
		if r.err != nil {
			return CounterSample{}, errMalformed
		}
		if r.off < len(r.b) {
			s.UnknownRecords++
		}
		s.Interfaces = append(s.Interfaces, v)
	}
	if c.err != nil {
		return CounterSample{}, errMalformed
	}
	if c.off < len(c.b) {
		s.UnknownRecords++
	}
	return s, nil
}

func tcp179(src, dst uint16, flags byte) bool {
	return (src == 179 || dst == 179) && flags&0x10 != 0 && flags&0x07 == 0
}
func decodeHeader(f *Flow, protocol uint32, b []byte) string {
	if protocol == 1 {
		if len(b) < 14 {
			return "truncated-ethernet-header"
		}
		f.DestinationMAC = net.HardwareAddr(b[:6]).String()
		f.SourceMAC = net.HardwareAddr(b[6:12]).String()
		typ := binary.BigEndian.Uint16(b[12:14])
		b = b[14:]
		for typ == 0x8100 || typ == 0x88a8 || typ == 0x9100 {
			if len(f.HeaderVLANs) >= 4 {
				return "unsupported-vlan-depth"
			}
			if len(b) < 4 {
				return "truncated-vlan-header"
			}
			f.HeaderVLANs = append(f.HeaderVLANs, binary.BigEndian.Uint16(b[:2])&4095)
			typ = binary.BigEndian.Uint16(b[2:4])
			b = b[4:]
		}
		switch typ {
		case 0x800:
			protocol = 11
		case 0x86dd:
			protocol = 12
		default:
			return "unsupported-network-protocol"
		}
	}
	switch protocol {
	case 11:
		if len(b) < 20 || b[0]>>4 != 4 {
			return "truncated-ipv4-header"
		}
		n := int(b[0]&15) * 4
		length := int(binary.BigEndian.Uint16(b[2:4]))
		if n < 20 || n > len(b) || length < n {
			return "invalid-ipv4-header"
		}
		f.Family = 4
		if binary.BigEndian.Uint16(b[6:8])&0x1fff != 0 {
			return ""
		}
		if length < len(b) {
			b = b[:length]
		}
		if b[9] == 6 {
			decodeTCP(f, b[n:])
		}
		return ""
	case 12:
		if len(b) < 40 || b[0]>>4 != 6 {
			return "truncated-ipv6-header"
		}
		f.Family = 6
		payload := int(binary.BigEndian.Uint16(b[4:6]))
		if payload == 0 {
			return "unsupported-ipv6-jumbogram"
		}
		if payload+40 < len(b) {
			b = b[:payload+40]
		}
		next := b[6]
		b = b[40:]
		for depth := 0; depth < 8; depth++ {
			switch next {
			case 0, 43, 60:
				if len(b) < 2 {
					return ""
				}
				n := (int(b[1]) + 1) * 8
				if n > len(b) {
					return ""
				}
				next = b[0]
				b = b[n:]
			case 44:
				if len(b) < 8 {
					return ""
				}
				if binary.BigEndian.Uint16(b[2:4])&0xfff8 != 0 {
					return ""
				}
				next = b[0]
				b = b[8:]
			case 51:
				if len(b) < 2 {
					return ""
				}
				n := (int(b[1]) + 2) * 4
				if n > len(b) {
					return ""
				}
				next = b[0]
				b = b[n:]
			case 6:
				decodeTCP(f, b)
				return ""
			default:
				return ""
			}
		}
		return ""
	default:
		return "unsupported-header-protocol"
	}
}
func decodeTCP(f *Flow, b []byte) {
	if len(b) < 20 {
		return
	}
	n := int(b[12]>>4) * 4
	if n < 20 || n > len(b) {
		return
	}
	f.SampledTCP179Activity = tcp179(binary.BigEndian.Uint16(b[:2]), binary.BigEndian.Uint16(b[2:4]), b[13])
}
