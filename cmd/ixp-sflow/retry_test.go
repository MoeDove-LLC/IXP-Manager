package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
)

type queuedSocket struct {
	packets [][]byte
	closed  bool
	reads   int
	cancel  context.CancelFunc
}

func (s *queuedSocket) Close() error                  { s.closed = true; s.packets = nil; return nil }
func (*queuedSocket) SetReadBuffer(int) error         { return nil }
func (*queuedSocket) SetReadDeadline(time.Time) error { return nil }
func (s *queuedSocket) ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error) {
	if s.closed {
		return 0, netip.AddrPort{}, errors.New("closed socket read")
	}
	s.reads++
	if len(s.packets) == 0 {
		s.cancel()
		return 0, netip.AddrPort{}, context.Canceled
	}
	p := s.packets[0]
	s.packets = s.packets[1:]
	return copy(b, p), netip.MustParseAddrPort("192.0.2.1:6343"), nil
}
func emptyDatagram(seq uint32) []byte {
	b := make([]byte, 28)
	for i, v := range []uint32{5, 1, 0xc0000201, 0, seq, 100000 + seq, 0} {
		binary.BigEndian.PutUint32(b[i*4:], v)
	}
	return b
}
func loopConfig() sflow.Config {
	return sflow.Config{Endpoint: "https://manager.example/api/sflow", Bind: "127.0.0.1:6343", MaxDatagramsPerSecond: 100, Exporters: []sflow.Exporter{{ID: "exporter", SwitchID: "switch", SourceIP: "192.0.2.1", AgentAddress: "192.0.2.1"}}}
}

func TestCollectorRefreshesAfterConflictOrServerFailureWithoutRetagging(t *testing.T) {
	for _, status := range []int{409, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clock := time.Now()
			old := &queuedSocket{packets: [][]byte{emptyDatagram(1), emptyDatagram(2)}, cancel: cancel}
			next := &queuedSocket{packets: [][]byte{emptyDatagram(3)}, cancel: cancel}
			fetches, opens := 0, 0
			uploads := []sflow.Batch{}
			waits := []time.Duration{}
			ops := collectorIO{now: func() time.Time { return clock }, fetch: func(_ context.Context, e sflow.Exporter) (sflow.Binding, error) {
				fetches++
				if fetches > 1 && !old.closed {
					t.Fatal("new bindings fetched before old queue was closed")
				}
				revision := strings.Repeat("a", 22)
				if fetches > 1 {
					revision = strings.Repeat("b", 22)
				}
				return sflow.Binding{ExporterID: e.ID, SwitchID: e.SwitchID, MappingRevision: revision, IssuedAt: clock, ExpiresAt: clock.Add(time.Minute)}, nil
			}, open: func(string) (datagramSocket, error) {
				opens++
				if opens == 1 {
					return old, nil
				}
				return next, nil
			}, wait: func(_ context.Context, d time.Duration) error {
				if !old.closed {
					t.Fatal("backoff retained old UDP receive queue")
				}
				waits = append(waits, d)
				clock = clock.Add(d)
				return nil
			}, upload: func(_ context.Context, b sflow.Batch) error {
				uploads = append(uploads, b)
				if len(uploads) == 1 {
					return &sflow.ControlError{StatusCode: status, Temporary: true}
				}
				cancel()
				return nil
			}}
			var out bytes.Buffer
			if err := collectLoop(ctx, loopConfig(), &out, ops); err != nil {
				t.Fatal(err)
			}
			if fetches != 2 || opens != 2 || old.reads != 1 || len(uploads) != 2 || uploads[0].Datagram.Sequence != 1 || uploads[1].Datagram.Sequence != 3 || uploads[0].MappingRevision == uploads[1].MappingRevision || uploads[1].Quality.SequenceState != "gap" || !reflect.DeepEqual(waits, []time.Duration{time.Second}) {
				t.Fatalf("old packet replayed or tracker/reset recovery failed: fetch=%d opens=%d reads=%d uploads=%+v waits=%v", fetches, opens, old.reads, uploads, waits)
			}
			if !next.closed {
				t.Fatal("shutdown leaked new socket")
			}
		})
	}
}
func TestCollectorBindingBackoffIsBoundedAndAuthStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := time.Now()
	attempts, opens := 0, 0
	waits := []time.Duration{}
	socket := &queuedSocket{packets: [][]byte{emptyDatagram(1)}, cancel: cancel}
	ops := collectorIO{now: func() time.Time { return clock }, fetch: func(_ context.Context, e sflow.Exporter) (sflow.Binding, error) {
		attempts++
		if attempts <= 8 {
			return sflow.Binding{}, &sflow.ControlError{StatusCode: 503, Temporary: true}
		}
		return sflow.Binding{ExporterID: e.ID, SwitchID: e.SwitchID, MappingRevision: strings.Repeat("a", 22), IssuedAt: clock, ExpiresAt: clock.Add(time.Minute)}, nil
	}, open: func(string) (datagramSocket, error) { opens++; return socket, nil }, wait: func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		clock = clock.Add(d)
		return nil
	}, upload: func(context.Context, sflow.Batch) error { cancel(); return nil }}
	if err := collectLoop(ctx, loopConfig(), &bytes.Buffer{}, ops); err != nil {
		t.Fatal(err)
	}
	if opens != 1 || !reflect.DeepEqual(waits, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}) {
		t.Fatal("backoff unbounded or listener opened while lease failed", waits, opens)
	}
	for _, status := range []int{401, 403} {
		ctx, cancel := context.WithCancel(context.Background())
		socket := &queuedSocket{packets: [][]byte{emptyDatagram(1)}, cancel: cancel}
		waited := false
		ops.now = time.Now
		ops.fetch = func(_ context.Context, e sflow.Exporter) (sflow.Binding, error) {
			now := time.Now()
			return sflow.Binding{ExporterID: e.ID, SwitchID: e.SwitchID, MappingRevision: strings.Repeat("a", 22), IssuedAt: now, ExpiresAt: now.Add(time.Minute)}, nil
		}
		ops.open = func(string) (datagramSocket, error) { return socket, nil }
		ops.wait = func(context.Context, time.Duration) error { waited = true; return nil }
		ops.upload = func(context.Context, sflow.Batch) error { return &sflow.ControlError{StatusCode: status} }
		if err := collectLoop(ctx, loopConfig(), &bytes.Buffer{}, ops); err == nil || waited || !socket.closed {
			t.Fatal("revoked authority retried or socket remained open", status, err)
		}
		cancel()
	}
}
