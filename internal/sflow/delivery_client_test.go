package sflow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDeliveryHTTPRequiresMatchingEnvelopeAndDoesNotChangeOldUpload(t *testing.T) {
	at := time.Now()
	p, b := relayBatch(t, mixedRelayDatagram(), at)
	defer p.Destroy()
	grant := grantFor(b, at, grantedReceiver("receiver", "198.51.100.10", 0))
	client := &http.Client{Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/sflow/ingest" {
			t.Fatal("unexpected control path")
		}
		var posted Batch
		json.NewDecoder(r.Body).Decode(&posted)
		if !posted.RequestDeliveries || posted.RequestNonce != p.Nonce() {
			t.Fatal("delivery upload not bound to capture")
		}
		raw, _ := json.Marshal(map[string]any{"acceptedFlows": 2, "deliveryEnvelope": grant})
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: r}, nil
	})}
	got, err := UploadWithDeliveries(context.Background(), client, "https://manager.example/api/sflow", "fixture-private-token", b)
	if err != nil || got.RequestNonce != b.RequestNonce {
		t.Fatal(got, err)
	}
	grant.RequestNonce = strings.Repeat("A", 43)
	if _, err = UploadWithDeliveries(context.Background(), client, "https://manager.example/api/sflow", "fixture-private-token", b); err == nil {
		t.Fatal("wrong packet envelope accepted")
	}
	if err = Upload(context.Background(), client, "https://manager.example/api/sflow", "fixture-private-token", b); err != nil {
		t.Fatal("legacy metadata upload unexpectedly depends on grants", err)
	}
}

func TestDeliveryReportUsesOnlyFixedMetadataEndpoint(t *testing.T) {
	_, b := relayBatch(t, mixedRelayDatagram(), time.Now())
	r := DeliveryReport{Version: 1, RequestNonce: b.RequestNonce, DatagramDigest: b.DatagramDigest, ExporterID: b.ExporterID, SwitchID: b.SwitchID, MappingRevision: b.MappingRevision, ObservedAt: time.Now().UTC(), Receipts: []DeliveryReceipt{{ReceiverID: "receiver", ReceiverRevision: strings.Repeat("r", 22), Outcome: "sent-to-socket", Datagrams: 1, Bytes: 100}}}
	calls := 0
	client := &http.Client{Transport: fakeTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Path != "/api/sflow/delivery-receipts" {
			t.Fatal("arbitrary receipt path")
		}
		raw, _ := io.ReadAll(request.Body)
		if strings.Contains(string(raw), "APPLICATION") || strings.Contains(string(raw), "sourceMac") || strings.Contains(string(raw), "fixture-private-token") {
			t.Fatal("receipt leaked packet data or credentials")
		}
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("private backend diagnostics")), Request: request}, nil
	})}
	if err := UploadDeliveryReport(context.Background(), client, "https://manager.example/api/sflow", "fixture-private-token", r); err == nil || calls != 1 || strings.Contains(err.Error(), "private") {
		t.Fatal("receipt failure retried or exposed body", err, calls)
	}
}
