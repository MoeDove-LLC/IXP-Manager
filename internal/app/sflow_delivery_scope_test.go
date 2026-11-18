package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
)

func destinationReceiver(t *testing.T, f flowAPIFixture) Record {
	t.Helper()
	spec := receiverFixtureSpec(f, true)
	spec.MemberID = str(f.otherMember, "id")
	spec.ConnectionIDs = []string{f.otherConnection}
	return approveReceiver(t, f.c, f.c.ok("POST", "/api/sflow/receivers", Record{"name": "Reviewed destination receiver", "spec": spec}))
}

func TestSFlowDeliveryDestinationRequiresReviewedPhysicalOutput(t *testing.T) {
	f := newFlowAPIFixture(t)
	receiver := destinationReceiver(t, f)
	forward := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "Source-switch forwarding", "currentPassword": "very-long-test-password", "scopes": []string{"sflow:write", "sflow:forward"}, "switchIds": []string{f.switchID}}), "token")
	response, grant := deliveryReply(t, f.c, forward, requestDelivery(f.batch(t, 1)))
	if response["memberForwarding"] != true || grant == nil || len(grant.Deliveries) != 1 || grant.Deliveries[0].ReceiverID != str(receiver, "id") || len(grant.Deliveries[0].FlowIndices) != 1 || grant.Deliveries[0].FlowIndices[0] != 0 {
		t.Fatal("reviewed destination output=2 did not receive its exact flow", response)
	}
	for i, output := range []sflow.Interface{{Format: 0, Value: 0}, {Format: 1, Value: 2}, {Format: 0, Value: 99}} {
		packet := f.batch(t, uint32(i+2))
		packet.Datagram.Flows[0].Output = output
		response, grant = deliveryReply(t, f.c, forward, requestDelivery(packet))
		if response["acceptedFlows"] != float64(1) || response["unattributedFlows"] != float64(0) {
			t.Fatal("statistical MAC attribution must remain distinct from forwarding approval", output, response)
		}
		if response["memberForwarding"] != false || grant == nil || len(grant.Deliveries) != 0 {
			t.Fatal("destination MAC expanded approval beyond physical output=2", output, response)
		}
	}
}

func TestSFlowDeliveryDestinationCannotAddUnreviewedRemoteExporter(t *testing.T) {
	f := newFlowAPIFixture(t)
	originalSwitch := f.c.ok("GET", "/api/resources/switches/"+f.switchID, nil)
	remote := f.c.create("switches", Record{"name": "Destination switch", "locationId": originalSwitch["locationId"]})
	remotePort := f.c.create("ports", Record{"name": "remote-member", "switchId": remote["id"], "status": "active", "speed": 1000})
	remoteToken := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "Destination-switch collector", "currentPassword": "very-long-test-password", "scopes": []string{"devices:write", "sflow:write", "sflow:forward"}, "switchIds": []string{str(remote, "id")}}), "token")
	snapshot := devicesnmp.Snapshot{Version: 1, SwitchID: str(remote, "id"), ObservedAt: time.Now().UTC(), Security: "authPriv", EngineIdentity: strings.Repeat("b", 64), Identity: devicesnmp.Identity{Name: "Destination switch"}, UptimeTicks: "10000", Interfaces: []devicesnmp.Interface{{IfIndex: 77, Name: "remote-member", Type: 6, MTU: 9000, SpeedMbps: 1000, AdminStatus: "up", OperStatus: "up"}}, Entities: []devicesnmp.Entity{}, Sensors: []devicesnmp.Sensor{}, FDB: []devicesnmp.FDBEntry{}, Warnings: []string{}}
	discovery := responseRecord(t, bearerRequest(f.c.a, remoteToken, "POST", "/api/devices/discoveries", snapshot))
	path := "/api/devices/discoveries/" + str(discovery, "id")
	preview := f.c.ok("GET", path+"/preview", nil)
	f.c.ok("POST", path+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": []Record{{"ifIndex": 77, "portId": remotePort["id"]}}})
	f.c.ok("PUT", "/api/resources/connections/"+f.otherConnection, Record{"name": "Remote destination connection", "memberId": f.otherMember["id"], "portId": remotePort["id"], "vlanId": f.connection["vlanId"], "ipv4": "192.0.2.11", "status": "active"})
	remoteExporter := f.c.create(sflowExporterKind, Record{"name": "Destination exporter", "switchId": remote["id"], "sourceAddress": "192.0.2.251", "agentAddress": "192.0.2.251", "subAgentId": 0, "vlanIds": []string{str(f.connection, "vlanId")}, "enabled": true})
	receiver := destinationReceiver(t, f)
	planRecord := f.c.ok("GET", "/api/sflow/receivers/"+str(receiver, "id")+"/plan", nil)
	encoded, _ := json.Marshal(planRecord)
	var plan sflow.ReceiverPlan
	if err := json.Unmarshal(encoded, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Bindings) == 0 {
		t.Fatal("missing reviewed remote bindings")
	}
	for _, b := range plan.Bindings {
		if b.ExporterID != str(remoteExporter, "id") || b.SwitchID != str(remote, "id") || b.IfIndex != 77 || b.ConnectionID != f.otherConnection {
			t.Fatal("fixture review unexpectedly covered source switch", b)
		}
	}
	forward := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "Only source-switch forwarding", "currentPassword": "very-long-test-password", "scopes": []string{"sflow:write", "sflow:forward"}, "switchIds": []string{f.switchID}}), "token")
	packet := f.batch(t, 1)
	// Numeric output=77 matches the reviewed remote ifIndex but does not exist
	// on this packet's source switch/exporter. Statistics may use the known
	// destination MAC; a forwarding grant may not infer that remote tuple.
	packet.Datagram.Flows[0].Output = sflow.Interface{Format: 0, Value: 77}
	response, grant := deliveryReply(t, f.c, forward, requestDelivery(packet))
	if response["acceptedFlows"] != float64(1) || response["unattributedFlows"] != float64(0) {
		t.Fatal("fixture no longer exercises an attributed cross-switch destination", response)
	}
	if response["memberForwarding"] != false || grant == nil || len(grant.Deliveries) != 0 {
		t.Fatal("source-switch token obtained an unreviewed remote receiver grant", response)
	}
	// Positive control: that receiver is still eligible on its actually
	// reviewed exporter, as the source at physical input=77.
	binding := responseRecord(t, bearerRequest(f.c.a, remoteToken, "GET", "/api/sflow/bindings?exporterId="+str(remoteExporter, "id"), nil))
	packet.ExporterID, packet.SwitchID = str(remoteExporter, "id"), str(remote, "id")
	packet.MappingRevision, packet.TransportSource, packet.ReceivedAt = str(binding, "mappingRevision"), "192.0.2.251", time.Now().UTC()
	packet.Datagram.AgentAddress = "192.0.2.251"
	packet.Datagram.Flows[0].SourceIndex, packet.Datagram.Flows[0].Input = 77, sflow.Interface{Value: 77}
	packet.Datagram.Flows[0].Output = sflow.Interface{Value: 0}
	packet.Datagram.Flows[0].SourceMAC, packet.Datagram.Flows[0].DestinationMAC = "02:00:00:00:00:02", "02:00:00:00:00:01"
	response, grant = deliveryReply(t, f.c, remoteToken, requestDelivery(packet))
	if response["memberForwarding"] != true || grant == nil || len(grant.Deliveries) != 1 || grant.Deliveries[0].ReceiverID != str(receiver, "id") {
		t.Fatal("reviewed remote source tuple was incorrectly disabled", response)
	}
}
