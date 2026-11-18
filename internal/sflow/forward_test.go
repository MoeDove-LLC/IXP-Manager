package sflow

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func forwardingFixture() ForwardingPolicy {
	return ForwardingPolicy{RelayAgentAddress: "192.0.2.254", AllowedDestinationCIDRs: []string{"198.51.100.0/24"}, AllowedPorts: []uint16{6343}, MaxReceivers: 8, MaxDatagramsPerSecond: 100, MaxBytesPerSecond: 1 << 20, MaxBytesPerBatch: 65536}
}
func mixedRelayDatagram() []byte {
	a := append(packet4(), []byte("APPLICATION_A_SECRET")...)
	binary.BigEndian.PutUint16(a[16:18], uint16(len(a)-14))
	b := append(packet4(), []byte("APPLICATION_B_SECRET")...)
	b[6] = 2
	b[11] = 0xbb
	copy(b[26:30], []byte{203, 0, 113, 66})
	binary.BigEndian.PutUint16(b[16:18], uint16(len(b)-14))
	first := record(1, flowBytes(false, headerRecord(1, a), record(4096+7, []byte("VENDOR_A_SECRET"))))
	second := record(1, flowBytes(false, headerRecord(1, b), record(4096+9, []byte("VENDOR_B_SECRET"))))
	cntr := append(words(90, 8, 1), record(9999, []byte("COUNTER_VENDOR_SECRET"))...)
	out := words(5, 1, 0xc0000201, 123, 987, 100000, 3)
	out = append(out, first...)
	out = append(out, record(2, cntr)...)
	return append(out, second...)
}
func relayBatch(t *testing.T, raw []byte, at time.Time) (*CapturedPacket, Batch) {
	t.Helper()
	p, err := CaptureAt(raw, at)
	if err != nil {
		t.Fatal(err)
	}
	d := p.Metadata()
	b := Batch{Version: 1, ExporterID: "exporter", SwitchID: "switch", MappingRevision: strings.Repeat("a", 22), ReceivedAt: at.UTC(), TransportSource: "192.0.2.1", Datagram: d, Quality: Quality{SequenceState: "first", CounterSamples: uint32(len(d.Counters))}, RequestDeliveries: true, RequestNonce: p.Nonce(), DatagramDigest: p.Digest()}
	if err = ValidateBatch(b); err != nil {
		t.Fatal(err)
	}
	return p, b
}
func grantFor(b Batch, at time.Time, deliveries ...Delivery) *DeliveryEnvelope {
	return &DeliveryEnvelope{Version: 1, RequestNonce: b.RequestNonce, DatagramDigest: b.DatagramDigest, ExporterID: b.ExporterID, SwitchID: b.SwitchID, MappingRevision: b.MappingRevision, IssuedAt: at.UTC(), ExpiresAt: at.UTC().Add(5 * time.Second), Deliveries: deliveries}
}
func grantedReceiver(id, address string, indices ...int) Delivery {
	return Delivery{ReceiverID: id, ReceiverRevision: strings.Repeat("r", 22), Address: address, Port: 6343, FlowIndices: indices, HeaderDataDisclosure: true}
}

func TestForwardMixedPacketIsolatesMembersAndRemovesPayloadAndSourceStats(t *testing.T) {
	now := time.Now()
	raw := mixedRelayDatagram()
	captured, b := relayBatch(t, raw, now)
	clear(raw) // caller buffer cannot alter the captured authorization/headers.
	metadata, _ := json.Marshal(b)
	for _, secret := range []string{"APPLICATION_A_SECRET", "APPLICATION_B_SECRET", "VENDOR_A_SECRET", "COUNTER_VENDOR_SECRET", "203.0.113.66"} {
		if bytes.Contains(metadata, []byte(secret)) {
			t.Fatal("manager metadata included packet contents", secret)
		}
	}
	if encoded, _ := json.Marshal(captured); string(encoded) != "{}" {
		t.Fatal("captured headers can be accidentally JSON uploaded")
	}
	sent := map[string][][]byte{}
	f, err := NewForwarder(forwardingFixture(), func(_ context.Context, destination netip.AddrPort, p []byte) error {
		sent[destination.String()] = append(sent[destination.String()], bytes.Clone(p))
		return nil
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	envelope := grantFor(b, now, grantedReceiver("receiver-a", "198.51.100.10", 0), grantedReceiver("receiver-b", "198.51.100.11", 1))
	report, err := f.Forward(context.Background(), captured, b, envelope, now, now, 0)
	if err != nil || len(report.Receipts) != 2 {
		t.Fatal(report, err)
	}
	var subagents []uint32
	for i, address := range []string{"198.51.100.10:6343", "198.51.100.11:6343"} {
		if len(sent[address]) != 1 {
			t.Fatal("wrong number of receiver datagrams", sent)
		}
		payload := sent[address][0]
		for _, secret := range []string{"APPLICATION_A_SECRET", "APPLICATION_B_SECRET", "VENDOR_A_SECRET", "VENDOR_B_SECRET", "COUNTER_VENDOR_SECRET"} {
			if bytes.Contains(payload, []byte(secret)) {
				t.Fatal("secret reached relay", secret)
			}
		}
		decoded, err := Parse(payload)
		if err != nil || len(decoded.Flows) != 1 || len(decoded.Counters) != 0 || decoded.UnknownRecords != 0 || decoded.UnknownSamples != 0 {
			t.Fatal("relay copied whole datagram or opaque metadata", decoded, err)
		}
		flow := decoded.Flows[0]
		if flow.SourceMAC != b.Datagram.Flows[i].SourceMAC || flow.PacketSize != b.Datagram.Flows[i].PacketSize || flow.SamplingRate != 400 {
			t.Fatal("wrong member sample or sampling metadata")
		}
		if flow.Sequence != 1 || flow.SourceIndex != 0 || flow.SamplePool != 0 || flow.Drops != 0 || flow.Input.Value != 0 || flow.Output.Value != 0 || decoded.Sequence != 1 || decoded.UptimeMS != 0 || decoded.AgentAddress != "192.0.2.254" {
			t.Fatal("source-wide statistics leaked", decoded)
		}
		subagents = append(subagents, decoded.SubAgentID)
		if len(payload) > MaxRelayDatagramBytes || report.Receipts[i].Outcome != "sent-to-socket" || report.Receipts[i].Datagrams != 1 || report.Receipts[i].Bytes != len(payload) {
			t.Fatal("receipt did not describe actual local write")
		}
	}
	if subagents[0] == subagents[1] {
		t.Fatal("receivers share relay-wide sequence stream")
	}
	if _, err = f.Forward(context.Background(), captured, b, envelope, now, now, 0); err == nil {
		t.Fatal("same immutable packet forwarded twice")
	}
	if len(captured.flows) != 0 {
		t.Fatal("private header buffers retained after grant consumption")
	}
}

func TestForwardRejectsMissingStaleOrMismatchedAuthority(t *testing.T) {
	now := time.Now()
	for name, change := range map[string]func(*DeliveryEnvelope){"nonce": func(e *DeliveryEnvelope) { e.RequestNonce = strings.Repeat("A", 43) }, "digest": func(e *DeliveryEnvelope) { e.DatagramDigest = strings.Repeat("f", 64) }, "mapping": func(e *DeliveryEnvelope) { e.MappingRevision = strings.Repeat("b", 22) }, "exporter": func(e *DeliveryEnvelope) { e.ExporterID = "other" }, "switch": func(e *DeliveryEnvelope) { e.SwitchID = "other" }, "receiver-revision": func(e *DeliveryEnvelope) { e.Deliveries[0].ReceiverRevision = "invalid" }, "no-consent": func(e *DeliveryEnvelope) { e.Deliveries[0].HeaderDataDisclosure = false }, "bad-index": func(e *DeliveryEnvelope) { e.Deliveries[0].FlowIndices = []int{2} }, "duplicate-index": func(e *DeliveryEnvelope) { e.Deliveries[0].FlowIndices = []int{0, 0} }, "expired": func(e *DeliveryEnvelope) { e.IssuedAt = now.Add(-4 * time.Second); e.ExpiresAt = now.Add(-time.Second) }} {
		t.Run(name, func(t *testing.T) {
			p, b := relayBatch(t, mixedRelayDatagram(), now)
			sent := 0
			f, err := NewForwarder(forwardingFixture(), func(context.Context, netip.AddrPort, []byte) error { sent++; return nil }, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			e := grantFor(b, now, grantedReceiver("receiver", "198.51.100.10", 0))
			change(e)
			_, _ = f.Forward(context.Background(), p, b, e, now, now, 0)
			if sent != 0 {
				t.Fatal("invalid grant emitted a datagram")
			}
		})
	}
	p, b := relayBatch(t, mixedRelayDatagram(), now)
	sent := 0
	f, _ := NewForwarder(forwardingFixture(), func(context.Context, netip.AddrPort, []byte) error { sent++; return nil }, func() time.Time { return now })
	defer f.Close()
	if _, err := f.Forward(context.Background(), p, b, nil, now, now, 0); err != nil || sent != 0 {
		t.Fatal("missing grant sent data", err)
	}
}

func TestForwardLocalPolicyRateExpiryAndSendFailureBoundaries(t *testing.T) {
	now := time.Now()
	for _, kind := range []string{"cidr", "port", "bytes", "datagrams", "batch", "time", "failure"} {
		t.Run(kind, func(t *testing.T) {
			policy := forwardingFixture()
			if kind == "bytes" {
				policy.MaxBytesPerSecond = 1
			}
			if kind == "batch" {
				policy.MaxBytesPerBatch = 1
			}
			if kind == "datagrams" {
				policy.MaxDatagramsPerSecond = 1
			}
			p, b := relayBatch(t, mixedRelayDatagram(), now)
			current := now
			sent := 0
			f, _ := NewForwarder(policy, func(context.Context, netip.AddrPort, []byte) error {
				sent++
				if kind == "failure" {
					return errors.New("private receiver failure detail")
				}
				return nil
			}, func() time.Time { return current })
			defer f.Close()
			d := grantedReceiver("receiver", "198.51.100.10", 0)
			if kind == "cidr" {
				d.Address = "203.0.113.10"
			}
			if kind == "port" {
				d.Port = 6344
			}
			e := grantFor(b, now, d)
			if kind == "datagrams" {
				e.Deliveries = append(e.Deliveries, grantedReceiver("receiver-two", "198.51.100.11", 1))
			}
			if kind == "time" {
				current = now.Add(6 * time.Second)
			}
			report, err := f.Forward(context.Background(), p, b, e, now, now, 0)
			if err != nil {
				t.Fatal(err)
			}
			expected := 0
			if kind == "datagrams" || kind == "failure" {
				expected = 1
			}
			if sent != expected {
				t.Fatal("local restriction failed", kind, sent)
			}
			if kind == "failure" && (report.Receipts[0].Outcome != "send-error" || report.Receipts[0].Datagrams != 0) {
				t.Fatal("UDP failure claimed delivery")
			}
			for _, r := range report.Receipts {
				if r.Outcome != "sent-to-socket" && r.Bytes != 0 {
					t.Fatal("failed action claimed sent bytes")
				}
			}
		})
	}
}

func TestHeaderSanitizerStripsOptionsFragmentsAndUnsupportedPayload(t *testing.T) {
	base := packet4()
	tcp := append(bytes.Clone(base), []byte("TCP_OPTIONS_SECRET!!APPLICATION_PAYLOAD")...)
	tcp[46] = 0xa0
	binary.BigEndian.PutUint16(tcp[16:18], uint16(len(tcp)-14))
	safe, err := sanitizeHeader(1, tcp)
	if err != nil || len(safe) != 74 || bytes.Contains(safe, []byte("SECRET")) || bytes.Contains(safe, []byte("PAYLOAD")) {
		t.Fatal("TCP options/application content leaked", len(safe), err)
	}
	fragment := append(bytes.Clone(base), []byte("FRAGMENT_PAYLOAD")...)
	binary.BigEndian.PutUint16(fragment[20:22], 0x2000)
	safe, err = sanitizeHeader(1, fragment)
	if err != nil || len(safe) != 34 {
		t.Fatal("fragment extended past proved IP header", len(safe), err)
	}
	unknown := append(bytes.Clone(base[:34]), []byte("UNSUPPORTED_PAYLOAD")...)
	unknown[23] = 253
	binary.BigEndian.PutUint16(unknown[16:18], uint16(len(unknown)-14))
	safe, err = sanitizeHeader(1, unknown)
	if err != nil || len(safe) != 34 {
		t.Fatal("unsupported transport copied payload")
	}
	v6 := make([]byte, 40)
	v6[0] = 0x60
	v6[6] = 44
	binary.BigEndian.PutUint16(v6[4:6], 20)
	v6 = append(v6, []byte("EXTENSION_OR_FRAGMENT_SECRET")...)
	safe, err = sanitizeHeader(12, v6)
	if err != nil || len(safe) != 40 {
		t.Fatal("IPv6 extension payload was copied")
	}
}

func TestRelayDecodedAndExpandedSamplesAreReencoded(t *testing.T) {
	ethernet := append(words(90), []byte{0, 6, 7, 8, 9, 10, 'X', 'Y', 0, 1, 2, 3, 4, 5, 'Z', 'Q'}...)
	ethernet = append(ethernet, words(0x800)...)
	ip := append(words(76, 6, 0xc0000263, 0xc6336463, 179, 12345, 0x10, 0), []byte("DECODED_VENDOR_SECRET")...)
	for name, raw := range map[string][]byte{"decoded": datagramBytes(1, flowBytes(false, record(2, ethernet), record(3, ip))), "expanded": datagramBytes(3, flowBytes(true, headerRecord(1, append(packet4(), []byte("EXPANDED_PAYLOAD")...))))} {
		t.Run(name, func(t *testing.T) {
			at := time.Now()
			p, b := relayBatch(t, raw, at)
			var sent []byte
			f, err := NewForwarder(forwardingFixture(), func(_ context.Context, _ netip.AddrPort, data []byte) error { sent = bytes.Clone(data); return nil }, func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			_, err = f.Forward(context.Background(), p, b, grantFor(b, at, grantedReceiver("receiver", "198.51.100.10", 0)), at, at, 0)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Parse(sent)
			if err != nil || len(decoded.Flows) != 1 || decoded.Flows[0].SourceMAC != b.Datagram.Flows[0].SourceMAC || decoded.Flows[0].SourceIndex != 0 || bytes.Contains(sent, []byte("SECRET")) || bytes.Contains(sent, []byte("PAYLOAD")) {
				t.Fatal("decoded/expanded relay unsafe", decoded, err)
			}
		})
	}
}

func TestRelaySplitsAtBoundAndReportsPartialSocketFailureWithoutRetry(t *testing.T) {
	at := time.Now()
	raw := words(5, 1, 0xc0000201, 0, 99, 100000, 50)
	indices := []int{}
	for i := 0; i < 50; i++ {
		raw = append(raw, record(1, flowBytes(false, headerRecord(1, packet4())))...)
		indices = append(indices, i)
	}
	p, b := relayBatch(t, raw, at)
	calls := 0
	totalFlows := 0
	f, err := NewForwarder(forwardingFixture(), func(_ context.Context, _ netip.AddrPort, packet []byte) error {
		calls++
		if len(packet) > MaxRelayDatagramBytes {
			t.Fatal("oversized relay UDP datagram")
		}
		decoded, err := Parse(packet)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Sequence != uint32(calls) {
			t.Fatal("relay datagram sequence is not local")
		}
		if calls == 2 {
			return errors.New("simulated socket failure")
		}
		totalFlows += len(decoded.Flows)
		return nil
	}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	report, err := f.Forward(context.Background(), p, b, grantFor(b, at, grantedReceiver("receiver", "198.51.100.10", indices...)), at, at, 0)
	if err != nil || calls != 2 || len(report.Receipts) != 1 || report.Receipts[0].Outcome != "partial-send-error" || report.Receipts[0].Datagrams != 1 || totalFlows == 0 || totalFlows >= 50 {
		t.Fatal("partial write misreported or retried", report, err, calls, totalFlows)
	}
}
