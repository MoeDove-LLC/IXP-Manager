package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
)

func cliForwardPolicy() *sflow.ForwardingPolicy {
	return &sflow.ForwardingPolicy{RelayAgentAddress: "192.0.2.254", AllowedDestinationCIDRs: []string{"198.51.100.0/24"}, AllowedPorts: []uint16{6343}, MaxReceivers: 4, MaxDatagramsPerSecond: 100, MaxBytesPerSecond: 65536, MaxBytesPerBatch: 65536}
}
func cliFlowDatagram(sequence uint32) []byte {
	word := func(v ...uint32) []byte {
		b := make([]byte, len(v)*4)
		for i, n := range v {
			binary.BigEndian.PutUint32(b[i*4:], n)
		}
		return b
	}
	opaque := func(p []byte) []byte {
		out := append(word(uint32(len(p))), p...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		return out
	}
	packet := make([]byte, 54)
	copy(packet, []byte{0, 1, 2, 3, 4, 5, 0, 6, 7, 8, 9, 10, 8, 0})
	packet[14] = 0x45
	packet[23] = 6
	copy(packet[26:30], []byte{192, 0, 2, 99})
	copy(packet[30:34], []byte{198, 51, 100, 99})
	binary.BigEndian.PutUint16(packet[34:36], 179)
	packet[46] = 0x50
	packet[47] = 0x10
	packet = append(packet, []byte("CLI_APPLICATION_SECRET")...)
	binary.BigEndian.PutUint16(packet[16:18], uint16(len(packet)-14))
	header := append(word(1, uint32(len(packet))+4, 4), opaque(packet)...)
	flow := word(sequence, 7, 400, 999999, 3333, 7, 9, 1)
	flow = append(flow, word(1)...)
	flow = append(flow, opaque(header)...)
	datagram := word(5, 1, 0xc0000201, 0, sequence, 100000+sequence, 1, 1)
	return append(datagram, opaque(flow)...)
}

func TestCollectorDefaultIgnoresForwardingConfigurationAndDoesNotRequestGrants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()
	config := loopConfig()
	config.Forwarding = cliForwardPolicy()
	socket := &queuedSocket{packets: [][]byte{cliFlowDatagram(1)}, cancel: cancel}
	called := false
	ops := collectorIO{now: func() time.Time { return now }, fetch: func(_ context.Context, e sflow.Exporter) (sflow.Binding, error) {
		return sflow.Binding{ExporterID: e.ID, SwitchID: e.SwitchID, MappingRevision: strings.Repeat("a", 22), IssuedAt: now, ExpiresAt: now.Add(time.Minute)}, nil
	}, open: func(string) (datagramSocket, error) { return socket, nil }, wait: func(context.Context, time.Duration) error { return errors.New("unexpected retry") }, upload: func(_ context.Context, b sflow.Batch) error {
		called = true
		if b.RequestDeliveries || b.RequestNonce != "" || b.DatagramDigest != "" {
			t.Fatal("default collection requested forwarding")
		}
		cancel()
		return nil
	}, deliveryUpload: func(context.Context, sflow.Batch) (*sflow.DeliveryEnvelope, error) {
		t.Fatal("default collection used delivery adapter")
		return nil, nil
	}}
	if err := collectLoop(ctx, config, &bytes.Buffer{}, ops); err != nil || !called {
		t.Fatal(err)
	}
}

func TestCollectorForwardingRequiresBindingScopeBeforeSocket(t *testing.T) {
	now := time.Now()
	f, err := sflow.NewForwarder(*cliForwardPolicy(), func(context.Context, netip.AddrPort, []byte) error {
		t.Fatal("no authority yet sender called")
		return nil
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	opened := false
	ops := collectorIO{now: func() time.Time { return now }, forwarder: f, fetch: func(_ context.Context, e sflow.Exporter) (sflow.Binding, error) {
		return sflow.Binding{ExporterID: e.ID, SwitchID: e.SwitchID, MappingRevision: strings.Repeat("a", 22), IssuedAt: now, ExpiresAt: now.Add(time.Minute)}, nil
	}, open: func(string) (datagramSocket, error) { opened = true; return nil, errors.New("unexpected") }, wait: func(context.Context, time.Duration) error { t.Fatal("missing scope must stop"); return nil }}
	if err = collectLoop(context.Background(), loopConfig(), &bytes.Buffer{}, ops); err == nil || opened {
		t.Fatal("scope denial opened UDP receiver", err)
	}
}

func TestCollectorForwardRebindDiscardsOldCaptureAndReceiptFailureNeverResends(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := time.Now()
	old := &queuedSocket{packets: [][]byte{cliFlowDatagram(1), cliFlowDatagram(2)}, cancel: cancel}
	fresh := &queuedSocket{packets: [][]byte{cliFlowDatagram(3)}, cancel: cancel}
	opens, fetches, sent, reports := 0, 0, 0, 0
	uploads := []sflow.Batch{}
	f, err := sflow.NewForwarder(*cliForwardPolicy(), func(_ context.Context, dest netip.AddrPort, payload []byte) error {
		sent++
		if dest.String() != "198.51.100.10:6343" || bytes.Contains(payload, []byte("CLI_APPLICATION_SECRET")) {
			t.Fatal("wrong relay destination or payload escaped")
		}
		return nil
	}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ops := collectorIO{now: func() time.Time { return clock }, forwarder: f, fetch: func(_ context.Context, e sflow.Exporter) (sflow.Binding, error) {
		fetches++
		revision := strings.Repeat("a", 22)
		if fetches > 1 {
			if !old.closed {
				t.Fatal("old socket remained open during remap")
			}
			revision = strings.Repeat("b", 22)
		}
		return sflow.Binding{ExporterID: e.ID, SwitchID: e.SwitchID, MappingRevision: revision, IssuedAt: clock, ExpiresAt: clock.Add(time.Minute), ForwardingAllowed: true}, nil
	}, open: func(string) (datagramSocket, error) {
		opens++
		if opens == 1 {
			return old, nil
		}
		return fresh, nil
	}, wait: func(_ context.Context, d time.Duration) error { clock = clock.Add(d); return nil }, deliveryUpload: func(_ context.Context, b sflow.Batch) (*sflow.DeliveryEnvelope, error) {
		if !b.RequestDeliveries || len(b.RequestNonce) != 43 {
			t.Fatal("forward upload missing packet binding")
		}
		uploads = append(uploads, b)
		if len(uploads) == 1 {
			return nil, &sflow.ControlError{StatusCode: 409, Temporary: true}
		}
		return &sflow.DeliveryEnvelope{Version: 1, RequestNonce: b.RequestNonce, DatagramDigest: b.DatagramDigest, ExporterID: b.ExporterID, SwitchID: b.SwitchID, MappingRevision: b.MappingRevision, IssuedAt: clock, ExpiresAt: clock.Add(5 * time.Second), Deliveries: []sflow.Delivery{{ReceiverID: "receiver", ReceiverRevision: strings.Repeat("r", 22), Address: "198.51.100.10", Port: 6343, FlowIndices: []int{0}, HeaderDataDisclosure: true}}}, nil
	}, report: func(_ context.Context, r sflow.DeliveryReport) error {
		reports++
		if len(r.Receipts) != 1 || r.Receipts[0].Outcome != "sent-to-socket" {
			t.Fatal("incorrect receipt", r)
		}
		cancel()
		return errors.New("private receipt transport error")
	}}
	var out bytes.Buffer
	if err = collectLoop(ctx, loopConfig(), &out, ops); err != nil {
		t.Fatal(err)
	}
	if sent != 1 || reports != 1 || len(uploads) != 2 || uploads[0].Datagram.Sequence != 1 || uploads[1].Datagram.Sequence != 3 || uploads[0].RequestNonce == uploads[1].RequestNonce || old.reads != 1 {
		t.Fatal("old capture or output was retried", sent, reports, uploads)
	}
	if strings.Contains(out.String(), "private receipt") {
		t.Fatal("receipt transport details leaked")
	}
}
