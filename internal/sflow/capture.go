package sflow

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"slices"
	"sync"
	"time"
)

type relaySample struct {
	records [][]byte
	rate    uint32
}

// CapturedPacket is never JSON serializable: network addresses/ports exist only
// in its private, sanitized header records. No application payload is retained.
type CapturedPacket struct {
	mu         sync.Mutex
	metadata   Datagram
	nonce      string
	checksum   string
	flows      []relaySample
	consumed   bool
	capturedAt time.Time
}

func DatagramDigest(d Datagram) (string, error) {
	if err := ValidateDatagram(d); err != nil {
		return "", err
	}
	return digest(d), nil
}
func NewRequestNonce() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errors.New("forwarding nonce generation failed")
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
func validNonce(n string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(n)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == n
}

func Capture(raw []byte) (*CapturedPacket, error) {
	return CaptureAt(raw, time.Now())
}
func CaptureAt(raw []byte, receivedAt time.Time) (*CapturedPacket, error) {
	if receivedAt.IsZero() {
		return nil, errors.New("capture requires a local monotonic receipt time")
	}
	if len(raw) > MaxDatagramBytes {
		return nil, errMalformed
	}
	raw = append([]byte(nil), raw...)
	defer clear(raw)
	d, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	nonce, err := NewRequestNonce()
	if err != nil {
		return nil, err
	}
	p := &CapturedPacket{metadata: d, nonce: nonce, checksum: digest(d), flows: make([]relaySample, 0, len(d.Flows)), capturedAt: receivedAt}
	c := cursor{b: raw}
	c.u32()
	kind := c.u32()
	if kind == 1 {
		c.take(4)
	} else {
		c.take(16)
	}
	c.u32()
	c.u32()
	c.u32()
	count := c.u32()
	for i := uint32(0); i < count; i++ {
		tag := c.u32()
		payload := c.opaque(MaxDatagramBytes)
		if tag != 1 && tag != 3 {
			continue
		}
		sample, err := captureFlow(payload, tag == 3)
		if err != nil {
			p.Destroy()
			return nil, err
		}
		p.flows = append(p.flows, sample)
	}
	if c.err != nil || len(p.flows) != len(d.Flows) {
		p.Destroy()
		return nil, errMalformed
	}
	return p, nil
}
func (p *CapturedPacket) Nonce() string  { return p.nonce }
func (p *CapturedPacket) Digest() string { return p.checksum }
func (p *CapturedPacket) Metadata() Datagram {
	d := p.metadata
	d.Flows = slices.Clone(d.Flows)
	for i := range d.Flows {
		d.Flows[i].HeaderVLANs = slices.Clone(d.Flows[i].HeaderVLANs)
		if v := d.Flows[i].InputVLAN; v != nil {
			n := *v
			d.Flows[i].InputVLAN = &n
		}
		if v := d.Flows[i].OutputVLAN; v != nil {
			n := *v
			d.Flows[i].OutputVLAN = &n
		}
	}
	d.Counters = slices.Clone(d.Counters)
	for i := range d.Counters {
		d.Counters[i].Interfaces = slices.Clone(d.Counters[i].Interfaces)
	}
	return d
}
func wipeSamples(samples []relaySample) {
	for _, s := range samples {
		for _, r := range s.records {
			clear(r)
		}
	}
}
func (p *CapturedPacket) Destroy() {
	p.mu.Lock()
	defer p.mu.Unlock()
	wipeSamples(p.flows)
	p.flows = nil
	p.consumed = true
}
func appendWord(b []byte, n uint32) []byte { return binary.BigEndian.AppendUint32(b, n) }
func appendOpaque(b, payload []byte) []byte {
	b = appendWord(b, uint32(len(payload)))
	b = append(b, payload...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}
func relayRecord(tag uint32, body []byte) []byte { return appendOpaque(appendWord(nil, tag), body) }

func captureFlow(raw []byte, expanded bool) (relaySample, error) {
	c := cursor{b: raw}
	c.u32()
	source(&c, expanded)
	rate := c.u32()
	c.u32()
	c.u32()
	iface(&c, expanded)
	iface(&c, expanded)
	count := c.u32()
	out := relaySample{rate: rate, records: [][]byte{}}
	for i := uint32(0); i < count; i++ {
		tag := c.u32()
		payload := c.opaque(MaxDatagramBytes)
		if c.err != nil {
			return relaySample{}, errMalformed
		}
		r := cursor{b: payload}
		switch tag {
		case 1:
			protocol, size, stripped := r.u32(), r.u32(), r.u32()
			header := r.opaque(MaxHeaderBytes)
			safe, err := sanitizeHeader(protocol, header)
			if err != nil {
				continue
			}
			body := appendWord(nil, protocol)
			body = appendWord(body, size)
			body = appendWord(body, stripped)
			body = appendOpaque(body, safe)
			out.records = append(out.records, relayRecord(1, body))
			clear(safe)
			clear(body)
		case 2:
			if len(payload) < 24 {
				return relaySample{}, errMalformed
			}
			body := append([]byte(nil), payload[:24]...)
			clear(body[10:12])
			clear(body[18:20])
			out.records = append(out.records, relayRecord(2, body))
			clear(body)
		case 3:
			if len(payload) < 32 {
				return relaySample{}, errMalformed
			}
			out.records = append(out.records, relayRecord(3, payload[:32]))
		case 4:
			if len(payload) < 56 {
				return relaySample{}, errMalformed
			}
			out.records = append(out.records, relayRecord(4, payload[:56]))
		}
	}
	if c.err != nil {
		return relaySample{}, errMalformed
	}
	return out, nil
}

// sanitizeHeader copies only a proved L2/L3/L4 boundary. IP/TCP option bytes are
// zeroed; IPv6 extension/fragment payloads stop at the base IP header. Truncated
// or unsupported transport never causes bytes of application data to be copied.
func sanitizeHeader(protocol uint32, raw []byte) ([]byte, error) {
	b := raw
	offset := 0
	if protocol == 1 {
		if len(b) < 14 {
			return nil, errMalformed
		}
		offset = 14
		typ := binary.BigEndian.Uint16(b[12:14])
		for depth := 0; typ == 0x8100 || typ == 0x88a8 || typ == 0x9100; depth++ {
			if depth >= 4 || len(b) < offset+4 {
				return nil, errMalformed
			}
			typ = binary.BigEndian.Uint16(b[offset+2 : offset+4])
			offset += 4
		}
		switch typ {
		case 0x800:
			protocol = 11
		case 0x86dd:
			protocol = 12
		default:
			return append([]byte(nil), b[:offset]...), nil
		}
	}
	base := offset
	end := offset
	tcpOptionsStart, tcpOptionsEnd := 0, 0
	switch protocol {
	case 11:
		if len(b) < base+20 || b[base]>>4 != 4 {
			return nil, errMalformed
		}
		ihl := int(b[base]&15) * 4
		total := int(binary.BigEndian.Uint16(b[base+2 : base+4]))
		if ihl < 20 || ihl > 60 || total < ihl || len(b) < base+ihl {
			return nil, errMalformed
		}
		if base+total < len(b) {
			b = b[:base+total]
		}
		end = base + ihl
		if binary.BigEndian.Uint16(b[base+6:base+8])&0x3fff == 0 {
			end, tcpOptionsStart, tcpOptionsEnd = transportBoundary(b, end, b[base+9], false)
		}
		out := append([]byte(nil), b[:end]...)
		clear(out[base+20 : base+ihl])
		if tcpOptionsEnd > tcpOptionsStart {
			clear(out[tcpOptionsStart:tcpOptionsEnd])
		}
		return out, nil
	case 12:
		if len(b) < base+40 || b[base]>>4 != 6 {
			return nil, errMalformed
		}
		payload := int(binary.BigEndian.Uint16(b[base+4 : base+6]))
		if payload == 0 {
			return append([]byte(nil), b[:base+40]...), nil
		}
		if base+40+payload < len(b) {
			b = b[:base+40+payload]
		}
		end = base + 40
		end, tcpOptionsStart, tcpOptionsEnd = transportBoundary(b, end, b[base+6], true)
		out := append([]byte(nil), b[:end]...)
		if tcpOptionsEnd > tcpOptionsStart {
			clear(out[tcpOptionsStart:tcpOptionsEnd])
		}
		return out, nil
	default:
		return nil, errors.New("unsupported relay header protocol")
	}
}
func transportBoundary(b []byte, start int, protocol byte, ipv6 bool) (int, int, int) {
	remaining := len(b) - start
	switch protocol {
	case 6:
		if remaining < 20 {
			return start, 0, 0
		}
		length := int(b[start+12]>>4) * 4
		if length < 20 || length > 60 {
			return start, 0, 0
		}
		if length > remaining {
			return start + 20, 0, 0
		}
		return start + length, start + 20, start + length
	case 17:
		if remaining >= 8 && binary.BigEndian.Uint16(b[start+4:start+6]) >= 8 {
			return start + 8, 0, 0
		}
	case 1:
		if !ipv6 && remaining >= 4 {
			if (b[start] == 0 || b[start] == 8) && remaining >= 8 {
				return start + 8, 0, 0
			}
			return start + 4, 0, 0
		}
	case 58:
		if ipv6 && remaining >= 4 {
			if (b[start] == 128 || b[start] == 129) && remaining >= 8 {
				return start + 8, 0, 0
			}
			return start + 4, 0, 0
		}
	case 132:
		if remaining >= 12 {
			return start + 12, 0, 0
		}
	}
	return start, 0, 0
}
