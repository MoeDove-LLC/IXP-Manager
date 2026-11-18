package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
)

func TestBindingClockSkewAndConservativeExpiry(t *testing.T) {
	local := time.Now() // Retain the local monotonic reading.
	server := local.UTC().Add(48 * time.Hour)
	binding := sflow.Binding{IssuedAt: server, ExpiresAt: server.Add(73 * time.Second)}
	response := local.Add(2 * time.Second)
	clock, err := anchorBinding(binding, local, response)
	if err != nil {
		t.Fatal(err)
	}
	estimate, ok := clock.receiptAt(response.Add(3 * time.Second))
	if !ok || !estimate.Equal(server.Add(4*time.Second)) {
		t.Fatal("local clock offset leaked into server-aligned receipt", estimate, ok)
	}
	if !clock.deadline.Equal(response.Add(71 * time.Second)) {
		t.Fatal("lease ignored RTT or assumed fixed60-second duration")
	}
	if _, ok = clock.receiptAt(clock.deadline); ok {
		t.Fatal("expired lease remained valid")
	}
	if _, ok = clock.receiptAt(response.Add(-time.Nanosecond)); ok {
		t.Fatal("pre-binding packet accepted")
	}
	if _, err = anchorBinding(binding, local, local.Add(sflow.ControlTimeout+time.Millisecond)); !sflow.RetryableControl(err) {
		t.Fatal("unbounded clock uncertainty accepted", err)
	}
	short := binding
	short.ExpiresAt = short.IssuedAt.Add(2 * time.Second)
	if _, err = anchorBinding(short, local, response); !sflow.RetryableControl(err) {
		t.Fatal("request already consumed lease but listener could open", err)
	}
	missing := binding
	missing.IssuedAt = time.Time{}
	if _, err = anchorBinding(missing, local, response); err == nil {
		t.Fatal("missing server clock silently guessed")
	}
}

func TestCollectorUploadsServerAlignedReceiptWithSlowLocalWallClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local := time.Now()
	server := local.UTC().Add(24 * time.Hour)
	socket := &queuedSocket{packets: [][]byte{emptyDatagram(1)}, cancel: cancel}
	var uploaded sflow.Batch
	ops := collectorIO{now: func() time.Time { return local }, fetch: func(_ context.Context, e sflow.Exporter) (sflow.Binding, error) {
		local = local.Add(2 * time.Second)
		return sflow.Binding{ExporterID: e.ID, SwitchID: e.SwitchID, MappingRevision: strings.Repeat("a", 22), IssuedAt: server, ExpiresAt: server.Add(time.Minute)}, nil
	}, open: func(string) (datagramSocket, error) { local = local.Add(3 * time.Second); return socket, nil }, wait: func(context.Context, time.Duration) error {
		t.Fatal("clock skew caused a false binding retry")
		return nil
	}, upload: func(_ context.Context, b sflow.Batch) error { uploaded = b; cancel(); return nil }}
	if err := collectLoop(ctx, loopConfig(), &bytes.Buffer{}, ops); err != nil {
		t.Fatal(err)
	}
	if !uploaded.ReceivedAt.Equal(server.Add(4*time.Second)) || uploaded.MappingRevision != strings.Repeat("a", 22) {
		t.Fatal("upload used local wall time instead of its exporter lease clock", uploaded)
	}
}
