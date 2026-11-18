package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
)

func receiverFixtureSpec(f flowAPIFixture, enabled bool) sflow.Receiver {
	return sflow.Receiver{MemberID: str(f.member, "id"), ConnectionIDs: []string{str(f.connection, "id")}, VLANIDs: []string{str(f.connection, "vlanId")}, Address: "198.51.100.25", Port: 6343, Enabled: enabled}
}
func approveReceiver(t *testing.T, c *client, receiver Record) Record {
	t.Helper()
	path := "/api/sflow/receivers/" + str(receiver, "id")
	review := c.ok("POST", path+"/review", Record{"expectedRevision": receiver["revision"]})
	return c.ok("POST", path+"/approve", Record{"expectedRevision": receiver["revision"], "reviewChecksum": review["reviewChecksum"], "acknowledgeHeaderData": true, "currentPassword": "very-long-test-password"})
}
func requestDelivery(b sflow.Batch) sflow.Batch {
	raw, _ := json.Marshal(b.Datagram)
	b.DatagramDigest = digest(string(raw))
	b.RequestNonce = token(32)
	b.RequestDeliveries = true
	return b
}
func deliveryReply(t *testing.T, c *client, raw string, b sflow.Batch) (Record, *sflow.DeliveryEnvelope) {
	t.Helper()
	response := responseRecord(t, bearerRequest(c.a, raw, "POST", "/api/sflow/ingest", b))
	encoded, _ := json.Marshal(response["deliveryEnvelope"])
	var envelope *sflow.DeliveryEnvelope
	if json.Unmarshal(encoded, &envelope) != nil {
		t.Fatal("invalid delivery envelope")
	}
	return response, envelope
}
func TestReceiverApprovalPerPacketGrantAndSocketReceipt(t *testing.T) {
	f := newFlowAPIFixture(t)
	receiver := f.c.ok("POST", "/api/sflow/receivers", Record{"name": "Member telemetry", "spec": receiverFixtureSpec(f, true)})
	path := "/api/sflow/receivers/" + str(receiver, "id")
	if w := f.c.request("GET", path+"/plan", nil); w.Code != 409 {
		t.Fatal("draft plan downloadable as approved")
	}
	receiver = approveReceiver(t, f.c, receiver)
	forward := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "Approved forwarding", "currentPassword": "very-long-test-password", "scopes": []string{"sflow:write", "sflow:forward"}, "switchIds": []string{f.switchID}}), "token")
	binding := responseRecord(t, bearerRequest(f.c.a, forward, "GET", "/api/sflow/bindings?exporterId="+str(f.exporter, "id"), nil))
	if binding["forwardingAllowed"] != true {
		t.Fatal("forward scope unavailable")
	}
	b := requestDelivery(f.batch(t, 1))
	if w := bearerRequest(f.c.a, f.writer, "POST", "/api/sflow/ingest", b); w.Code != 403 {
		t.Fatal("ordinary collector gained member forwarding")
	}
	response, envelope := deliveryReply(t, f.c, forward, b)
	if response["memberForwarding"] != true || envelope == nil || len(envelope.Deliveries) != 1 || envelope.Deliveries[0].ReceiverID != str(receiver, "id") || len(envelope.Deliveries[0].FlowIndices) != 1 || envelope.Deliveries[0].FlowIndices[0] != 0 || envelope.ExpiresAt.Sub(envelope.IssuedAt) != 5*time.Second {
		t.Fatal("wrong per-packet grant", response)
	}
	if response, grant := deliveryReply(t, f.c, forward, b); response["memberForwarding"] != false || grant != nil {
		t.Fatal("duplicate packet received a new forwarding grant")
	}
	report := sflow.DeliveryReport{Version: 1, RequestNonce: b.RequestNonce, DatagramDigest: b.DatagramDigest, ExporterID: b.ExporterID, SwitchID: b.SwitchID, MappingRevision: b.MappingRevision, ObservedAt: time.Now().UTC(), Receipts: []sflow.DeliveryReceipt{{ReceiverID: str(receiver, "id"), ReceiverRevision: envelope.Deliveries[0].ReceiverRevision, Outcome: "sent-to-socket", Datagrams: 1, Bytes: 200}}}
	bad := report
	bad.RequestNonce = token(32)
	if w := bearerRequest(f.c.a, forward, "POST", "/api/sflow/delivery-receipts", bad); w.Code != 409 {
		t.Fatal("receipt without authorization accepted")
	}
	for _, outcome := range []string{"expired", "rate-limited", "send-error"} {
		bad = report
		bad.Receipts = append([]sflow.DeliveryReceipt(nil), report.Receipts...)
		bad.Receipts[0].Outcome = outcome
		if w := bearerRequest(f.c.a, forward, "POST", "/api/sflow/delivery-receipts", bad); w.Code != 422 {
			t.Fatal("no-send outcome accepted positive socket counts", outcome, w.Code)
		}
	}
	reply := responseRecord(t, bearerRequest(f.c.a, forward, "POST", "/api/sflow/delivery-receipts", report))
	if reply["remoteArrivalVerified"] != false {
		t.Fatal("socket receipt claimed remote arrival")
	}
	view := f.c.ok("GET", path, nil)
	if view["deliveryState"] != "sent-to-socket" || view["authorizationCount"] != float64(1) {
		t.Fatal("runtime evidence missing", view)
	}
	unknown := f.batch(t, 2)
	unknown.Datagram.Flows[0].SourceMAC = "02:00:00:00:00:99"
	if reply, grant := deliveryReply(t, f.c, forward, requestDelivery(unknown)); reply["acceptedFlows"] != float64(1) || (grant != nil && len(grant.Deliveries) != 0) {
		t.Fatal("physical port attribution disclosed an unapproved source MAC", reply)
	}
	receiver = approveReceiver(t, f.c, view)
	if _, grant := deliveryReply(t, f.c, forward, requestDelivery(f.batch(t, 3))); grant == nil || len(grant.Deliveries) != 1 {
		t.Fatal("reapproved receiver received no grant")
	}
	view = f.c.ok("GET", path, nil)
	if view["receiptForCurrentRevision"] != false || view["receiptRevision"] != report.Receipts[0].ReceiverRevision || view["authorizationCount"] != float64(2) {
		t.Fatal("new authorization relabeled a prior revision's receipt", view)
	}
	f.c.ok("POST", path+"/disable", Record{"expectedRevision": view["revision"]})
	_, grant := deliveryReply(t, f.c, forward, requestDelivery(f.batch(t, 4)))
	if grant != nil && len(grant.Deliveries) != 0 {
		t.Fatal("disabled receiver received new grant")
	}
}

func TestReceiverStaleBindingCannotForwardAndCanBeRetired(t *testing.T) {
	f := newFlowAPIFixture(t)
	receiver := approveReceiver(t, f.c, f.c.ok("POST", "/api/sflow/receivers", Record{"name": "Retire old scope", "spec": receiverFixtureSpec(f, true)}))
	path := "/api/sflow/receivers/" + str(receiver, "id")
	state := f.c.a.store.Snapshot()
	for _, mac := range state.Resources["mac-addresses"] {
		if str(mac, "connectionId") == str(f.connection, "id") {
			f.c.ok("DELETE", "/api/resources/mac-addresses/"+str(mac, "id"), nil)
		}
	}
	f.c.ok("DELETE", "/api/resources/connections/"+str(f.connection, "id"), nil)
	stale := f.c.ok("GET", path, nil)
	if stale["status"] != "stale" {
		t.Fatal("retired scope remained approved")
	}
	stopped := f.c.ok("POST", path+"/disable", Record{"expectedRevision": stale["revision"]})
	planResponse := f.c.request("GET", path+"/plan", nil)
	if planResponse.Code != 200 || planResponse.Header().Get("X-SFlow-Receiver-ID") != str(receiver, "id") {
		t.Fatal("retired disabled scope cannot export stop plan", planResponse.Body.String())
	}
	var plan sflow.ReceiverPlan
	if json.Unmarshal(planResponse.Body.Bytes(), &plan) != nil || plan.Receiver.Enabled || plan.ForwardingEnabled || len(plan.Bindings) != 0 {
		t.Fatal("stop plan grants delivery")
	}
	f.c.ok("DELETE", path, Record{"expectedRevision": stopped["revision"], "reason": "retired connection"})
}

func TestReceiverResourceImportRequiresFreshApproval(t *testing.T) {
	f := newFlowAPIFixture(t)
	r := approveReceiver(t, f.c, f.c.ok("POST", "/api/sflow/receivers", Record{"name": "Original approval", "spec": receiverFixtureSpec(f, true)}))
	exported := f.c.ok("GET", "/api/export", nil)
	raw, err := json.Marshal(exported["sflowReceivers"])
	if err != nil {
		t.Fatal(err)
	}
	var imports []SFlowReceiverImport
	if err = json.Unmarshal(raw, &imports); err != nil || len(imports) != 1 || imports[0].Spec.Revision != "" {
		t.Fatal("receiver export retained approval revision", string(raw), err)
	}
	imports[0].Spec.ID = "imported-receiver"
	// Even a caller-supplied old revision cannot transfer approval authority.
	imports[0].Spec.Revision = str(r, "revision")
	f.c.ok("POST", "/api/import", Record{"version": 1, "resources": Record{}, "sflowReceivers": imports})
	imported := f.c.ok("GET", "/api/sflow/receivers/imported-receiver", nil)
	if imported["status"] != "draft" || imported["revision"] == r["revision"] || str(imported, "approvedBy") != "" || imported["approvedAt"] != nil {
		t.Fatal("resource import inherited an approval", imported)
	}
	if w := f.c.request("GET", "/api/sflow/receivers/imported-receiver/plan", nil); w.Code != 409 {
		t.Fatal("imported receiver can disclose before review", w.Code)
	}
}
