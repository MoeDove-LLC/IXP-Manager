package sflow

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
)

type DatagramSender func(context.Context, netip.AddrPort, []byte) error

var errRelayBudget = errors.New("relay batch byte limit exceeded")

type relayStream struct {
	revision, address                string
	port                             uint16
	subAgent, datagramSeq, sampleSeq uint32
	started                          time.Time
}
type Forwarder struct {
	mu                           sync.Mutex
	policy                       ForwardingPolicy
	prefixes                     []netip.Prefix
	ports                        map[uint16]bool
	sender                       DatagramSender
	now                          func() time.Time
	streams                      map[string]relayStream
	subAgents                    map[uint32]string
	secret                       [32]byte
	window                       time.Time
	windowDatagrams, windowBytes int
	closed                       bool
}

func (f *Forwarder) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	clear(f.secret[:])
	f.streams = nil
	f.subAgents = nil
}

func ValidateForwardingPolicy(p ForwardingPolicy) error {
	if !validIP(p.RelayAgentAddress) || len(p.AllowedDestinationCIDRs) < 1 || len(p.AllowedDestinationCIDRs) > 64 || len(p.AllowedPorts) < 1 || len(p.AllowedPorts) > 64 || p.MaxReceivers < 1 || p.MaxReceivers > MaxDeliveries || p.MaxDatagramsPerSecond < 1 || p.MaxDatagramsPerSecond > 10000 || p.MaxBytesPerSecond < 1 || p.MaxBytesPerSecond > 16<<20 || p.MaxBytesPerBatch < 1 || p.MaxBytesPerBatch > 1<<20 {
		return errors.New("invalid explicit local forwarding limits")
	}
	seen := map[string]bool{}
	for _, raw := range p.AllowedDestinationCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() || prefix.String() != raw || prefix.Addr().Is4In6() || seen[raw] {
			return errors.New("forwarding destinations require distinct canonical CIDRs")
		}
		seen[raw] = true
	}
	ports := map[uint16]bool{}
	for _, port := range p.AllowedPorts {
		if port == 0 || ports[port] {
			return errors.New("forwarding requires distinct explicit destination ports")
		}
		ports[port] = true
	}
	return nil
}
func NewForwarder(p ForwardingPolicy, sender DatagramSender, now func() time.Time) (*Forwarder, error) {
	if err := ValidateForwardingPolicy(p); err != nil {
		return nil, err
	}
	if sender == nil || now == nil {
		return nil, errors.New("forwarder needs explicit sender and monotonic clock")
	}
	f := &Forwarder{policy: p, ports: map[uint16]bool{}, sender: sender, now: now, streams: map[string]relayStream{}, subAgents: map[uint32]string{}}
	if _, err := rand.Read(f.secret[:]); err != nil {
		return nil, errors.New("relay stream identity generation failed")
	}
	for _, raw := range p.AllowedDestinationCIDRs {
		prefix, _ := netip.ParsePrefix(raw)
		f.prefixes = append(f.prefixes, prefix)
	}
	for _, port := range p.AllowedPorts {
		f.ports[port] = true
	}
	return f, nil
}

func ValidateDeliveryEnvelope(e DeliveryEnvelope, b Batch) error {
	if e.Version != 1 || !b.RequestDeliveries || !validNonce(b.RequestNonce) || e.RequestNonce != b.RequestNonce || !validDigest(b.DatagramDigest) || e.DatagramDigest != b.DatagramDigest || e.ExporterID != b.ExporterID || e.SwitchID != b.SwitchID || e.MappingRevision != b.MappingRevision || !identifier.MatchString(e.ExporterID) || !identifier.MatchString(e.SwitchID) || !revisionPattern.MatchString(e.MappingRevision) || e.IssuedAt.IsZero() || !e.ExpiresAt.After(e.IssuedAt) || e.ExpiresAt.Sub(e.IssuedAt) > MaxDeliveryTTL || len(e.Deliveries) > MaxDeliveries {
		return errors.New("delivery grant does not match this packet request")
	}
	seen := map[string]bool{}
	for _, d := range e.Deliveries {
		if !identifier.MatchString(d.ReceiverID) || !revisionPattern.MatchString(d.ReceiverRevision) || seen[d.ReceiverID] || !validIP(d.Address) || d.Port == 0 || !d.HeaderDataDisclosure || len(d.FlowIndices) == 0 || len(d.FlowIndices) > MaxSamples {
			return errors.New("invalid receiver delivery grant")
		}
		seen[d.ReceiverID] = true
		indices := map[int]bool{}
		for _, index := range d.FlowIndices {
			if index < 0 || index >= len(b.Datagram.Flows) || indices[index] {
				return errors.New("delivery grant has invalid or duplicate flow indices")
			}
			indices[index] = true
		}
	}
	return nil
}
func (f *Forwarder) destination(d Delivery) (netip.AddrPort, bool) {
	address, err := netip.ParseAddr(d.Address)
	if err != nil || !validIP(d.Address) || !f.ports[d.Port] {
		return netip.AddrPort{}, false
	}
	for _, prefix := range f.prefixes {
		if prefix.Contains(address) {
			return netip.AddrPortFrom(address, d.Port), true
		}
	}
	return netip.AddrPort{}, false
}
func (f *Forwarder) stream(d Delivery, now time.Time) (relayStream, error) {
	if old, ok := f.streams[d.ReceiverID]; ok && old.revision == d.ReceiverRevision && old.address == d.Address && old.port == d.Port {
		return old, nil
	}
	if _, ok := f.streams[d.ReceiverID]; !ok && len(f.streams) >= 4096 {
		return relayStream{}, errors.New("relay receiver-state limit reached")
	}
	mac := hmac.New(sha256.New, f.secret[:])
	mac.Write([]byte(d.ReceiverID + "\x00" + d.ReceiverRevision + "\x00" + d.Address))
	sub := binary.BigEndian.Uint32(mac.Sum(nil)[:4])
	if sub == 0 {
		sub = 1
	}
	for i := 0; i <= 4096; i++ {
		if owner := f.subAgents[sub]; owner == "" || owner == d.ReceiverID {
			return relayStream{revision: d.ReceiverRevision, address: d.Address, port: d.Port, subAgent: sub, started: now}, nil
		}
		sub++
		if sub == 0 {
			sub = 1
		}
	}
	return relayStream{}, errors.New("relay stream identity collision")
}

func relayDatagramHeader(agent netip.Addr, sub, seq, uptime, count uint32) []byte {
	b := appendWord(nil, 5)
	if agent.Is4() {
		b = appendWord(b, 1)
		v := agent.As4()
		b = append(b, v[:]...)
	} else {
		b = appendWord(b, 2)
		v := agent.As16()
		b = append(b, v[:]...)
	}
	b = appendWord(b, sub)
	b = appendWord(b, seq)
	b = appendWord(b, uptime)
	return appendWord(b, count)
}
func encodeRelay(samples []relaySample, indices []int, agent netip.Addr, stream relayStream, now time.Time, limit int) ([][]byte, relayStream, error) {
	parts := [][]byte{}
	group := []byte{}
	count := uint32(0)
	total := 0
	headerSize := len(relayDatagramHeader(agent, 0, 0, 0, 0))
	uptime := uint32(0)
	if now.After(stream.started) {
		uptime = uint32(now.Sub(stream.started) / time.Millisecond)
	}
	success := false
	defer func() {
		clear(group)
		if !success {
			for _, part := range parts {
				clear(part)
			}
		}
	}()
	flush := func() {
		if count == 0 {
			return
		}
		stream.datagramSeq++
		header := relayDatagramHeader(agent, stream.subAgent, stream.datagramSeq, uptime, count)
		parts = append(parts, append(header, group...))
		total += headerSize + len(group)
		clear(group)
		group = nil
		count = 0
	}
	for _, index := range indices {
		s := samples[index]
		if len(s.records) == 0 || s.rate == 0 {
			return nil, stream, errors.New("selected sample lacks relay-safe packet metadata")
		}
		stream.sampleSeq++
		body := appendWord(nil, stream.sampleSeq)
		body = appendWord(body, 0)
		body = appendWord(body, s.rate)
		for i := 0; i < 4; i++ {
			body = appendWord(body, 0)
		}
		body = appendWord(body, uint32(len(s.records)))
		for _, record := range s.records {
			body = append(body, record...)
		}
		encoded := relayRecord(1, body)
		clear(body)
		if headerSize+len(encoded) > MaxRelayDatagramBytes {
			clear(encoded)
			return nil, stream, errors.New("safe sample exceeds relay datagram bound")
		}
		if headerSize+len(group)+len(encoded) > MaxRelayDatagramBytes {
			flush()
		}
		if total+headerSize+len(group)+len(encoded) > limit {
			clear(encoded)
			return nil, stream, errRelayBudget
		}
		group = append(group, encoded...)
		clear(encoded)
		count++
	}
	flush()
	success = true
	return parts, stream, nil
}

// Forward consumes this captured packet once, including on rejection. It never
// caches authority or retransmits. send success means local UDP socket success.
func (f *Forwarder) Forward(ctx context.Context, p *CapturedPacket, b Batch, e *DeliveryEnvelope, requestAt, responseAt time.Time, clockUncertainty time.Duration) (DeliveryReport, error) {
	report := DeliveryReport{Version: 1, RequestNonce: b.RequestNonce, DatagramDigest: b.DatagramDigest, ExporterID: b.ExporterID, SwitchID: b.SwitchID, MappingRevision: b.MappingRevision, Receipts: []DeliveryReceipt{}}
	if p == nil {
		return report, errors.New("missing immutable captured packet")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.consumed {
		return report, errors.New("captured packet already consumed")
	}
	p.consumed = true
	defer func() { wipeSamples(p.flows); p.flows = nil }()
	if e == nil {
		return report, nil
	}
	if err := ValidateDeliveryEnvelope(*e, b); err != nil {
		return report, err
	}
	if p.nonce != b.RequestNonce || p.checksum != b.DatagramDigest || digest(b.Datagram) != p.checksum || len(p.flows) != len(b.Datagram.Flows) {
		return report, errors.New("captured packet and manager request disagree")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return report, errors.New("relay forwarder closed")
	}
	now := f.now()
	rtt := responseAt.Sub(requestAt)
	ttl := e.ExpiresAt.Sub(e.IssuedAt)
	deadline := responseAt.Add(ttl - rtt)
	serverUpperAtResponse := b.ReceivedAt.Add(responseAt.Sub(p.capturedAt) + clockUncertainty)
	clockDeadline := responseAt.Add(e.ExpiresAt.Sub(serverUpperAtResponse))
	if clockDeadline.Before(deadline) {
		deadline = clockDeadline
	}
	report.ObservedAt = e.IssuedAt.Add(rtt/2 + now.Sub(responseAt)).UTC()
	if len(e.Deliveries) > f.policy.MaxReceivers || f.policy.RelayAgentAddress == p.metadata.AgentAddress {
		for _, d := range e.Deliveries {
			report.Receipts = append(report.Receipts, DeliveryReceipt{ReceiverID: d.ReceiverID, ReceiverRevision: d.ReceiverRevision, Outcome: "blocked-policy"})
		}
		return report, nil
	}
	if clockUncertainty < 0 || clockUncertainty > ControlTimeout || responseAt.Before(p.capturedAt) || e.IssuedAt.After(serverUpperAtResponse) || rtt < 0 || rtt > ControlTimeout || now.Before(responseAt) || !now.Before(deadline) {
		for _, d := range e.Deliveries {
			report.Receipts = append(report.Receipts, DeliveryReceipt{ReceiverID: d.ReceiverID, ReceiverRevision: d.ReceiverRevision, Outcome: "expired"})
		}
		return report, nil
	}
	if f.window.IsZero() || now.Sub(f.window) >= time.Second {
		f.window = now
		f.windowDatagrams = 0
		f.windowBytes = 0
	}
	batchBytes := 0
	agent, _ := netip.ParseAddr(f.policy.RelayAgentAddress)
	for _, d := range e.Deliveries {
		receipt := DeliveryReceipt{ReceiverID: d.ReceiverID, ReceiverRevision: d.ReceiverRevision}
		destination, allowed := f.destination(d)
		if !allowed {
			receipt.Outcome = "blocked-policy"
			report.Receipts = append(report.Receipts, receipt)
			continue
		}
		stream, err := f.stream(d, now)
		if err != nil {
			receipt.Outcome = "rate-limited"
			report.Receipts = append(report.Receipts, receipt)
			continue
		}
		packets, next, err := encodeRelay(p.flows, d.FlowIndices, agent, stream, now, f.policy.MaxBytesPerBatch-batchBytes)
		if err != nil {
			receipt.Outcome = "encode-rejected"
			if errors.Is(err, errRelayBudget) {
				receipt.Outcome = "rate-limited"
			}
			report.Receipts = append(report.Receipts, receipt)
			continue
		}
		bytes := 0
		for _, packet := range packets {
			bytes += len(packet)
		}
		if f.windowDatagrams+len(packets) > f.policy.MaxDatagramsPerSecond || f.windowBytes+bytes > f.policy.MaxBytesPerSecond || batchBytes+bytes > f.policy.MaxBytesPerBatch {
			for _, packet := range packets {
				clear(packet)
			}
			receipt.Outcome = "rate-limited"
			report.Receipts = append(report.Receipts, receipt)
			continue
		}
		f.windowDatagrams += len(packets)
		f.windowBytes += bytes
		batchBytes += bytes
		if old, ok := f.streams[d.ReceiverID]; ok && old.subAgent != next.subAgent {
			delete(f.subAgents, old.subAgent)
		}
		f.streams[d.ReceiverID] = next
		f.subAgents[next.subAgent] = d.ReceiverID
		receipt.Outcome = "sent-to-socket"
		for _, packet := range packets {
			if ctx.Err() != nil || !f.now().Before(deadline) {
				receipt.Outcome = "expired"
				if receipt.Datagrams > 0 {
					receipt.Outcome = "partial-send-error"
				}
				break
			}
			sendCtx, cancel := context.WithDeadline(ctx, deadline)
			err = f.sender(sendCtx, destination, packet)
			cancel()
			if err != nil {
				receipt.Outcome = "send-error"
				if receipt.Datagrams > 0 {
					receipt.Outcome = "partial-send-error"
				}
				break
			}
			receipt.Datagrams++
			receipt.Bytes += len(packet)
		}
		for _, packet := range packets {
			clear(packet)
		}
		report.Receipts = append(report.Receipts, receipt)
	}
	report.ObservedAt = e.IssuedAt.Add(rtt/2 + f.now().Sub(responseAt)).UTC()
	return report, nil
}

// SendUDP uses an already validated literal address; it performs no DNS lookup.
func SendUDP(ctx context.Context, address netip.AddrPort, payload []byte) error {
	if ctx.Err() != nil || !address.IsValid() || len(payload) == 0 || len(payload) > MaxRelayDatagramBytes {
		return errors.New("invalid or expired relay datagram")
	}
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IP(address.Addr().AsSlice()), Port: int(address.Port())})
	if err != nil {
		return errors.New("relay UDP socket unavailable")
	}
	defer conn.Close()
	deadline := time.Now().Add(time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err = conn.SetWriteDeadline(deadline); err != nil {
		return errors.New("relay UDP deadline failed")
	}
	if ctx.Err() != nil {
		return errors.New("relay grant expired before send")
	}
	n, err := conn.Write(payload)
	if err != nil || n != len(payload) {
		return errors.New("relay UDP write failed")
	}
	return nil
}
