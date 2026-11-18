package app

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
)

type flowAPIFixture struct {
	monitorFixture
	exporter                          Record
	writer, switchID, otherConnection string
}

func newFlowAPIFixture(t *testing.T) flowAPIFixture {
	f := monitoringFixture(t)
	sw := str(f.port, "switchId")
	raw := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "Flow fixture", "currentPassword": "very-long-test-password", "scopes": []string{"devices:write", "sflow:write", "sflow:read"}, "switchIds": []string{sw}}), "token")
	snapshot := devicesnmp.Snapshot{Version: 1, SwitchID: sw, ObservedAt: time.Now().UTC(), Security: "authPriv", EngineIdentity: strings.Repeat("a", 64), Identity: devicesnmp.Identity{Name: "flow switch"}, UptimeTicks: "10000", Interfaces: []devicesnmp.Interface{{IfIndex: 1, PortID: str(f.port, "id"), Name: "eth1", Type: 6, MTU: 9000, SpeedMbps: 1000, AdminStatus: "up", OperStatus: "up"}, {IfIndex: 2, PortID: str(f.otherPort, "id"), Name: "eth2", Type: 6, MTU: 9000, SpeedMbps: 1000, AdminStatus: "up", OperStatus: "up"}}, Entities: []devicesnmp.Entity{}, Sensors: []devicesnmp.Sensor{}, FDB: []devicesnmp.FDBEntry{}, Warnings: []string{}}
	meta := responseRecord(t, bearerRequest(f.c.a, raw, "POST", "/api/devices/discoveries", snapshot))
	path := "/api/devices/discoveries/" + str(meta, "id")
	preview := f.c.ok("GET", path+"/preview", nil)
	f.c.ok("POST", path+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": []Record{{"ifIndex": 1, "portId": f.port["id"]}, {"ifIndex": 2, "portId": f.otherPort["id"]}}})
	state := f.c.a.store.Snapshot()
	other := ""
	for _, c := range state.Resources["connections"] {
		if str(c, "memberId") == str(f.otherMember, "id") {
			other = str(c, "id")
		}
	}
	f.c.create("mac-addresses", Record{"name": "A MAC", "connectionId": f.connection["id"], "address": "02:00:00:00:00:01"})
	f.c.create("mac-addresses", Record{"name": "B MAC", "connectionId": other, "address": "02:00:00:00:00:02"})
	e := f.c.create(sflowExporterKind, Record{"name": "Exporter", "switchId": sw, "sourceAddress": "192.0.2.250", "agentAddress": "192.0.2.250", "subAgentId": 0, "vlanIds": []string{str(f.connection, "vlanId")}, "enabled": true})
	return flowAPIFixture{f, e, raw, sw, other}
}
func (f flowAPIFixture) batch(t *testing.T, sequence uint32) sflow.Batch {
	t.Helper()
	binding := responseRecord(t, bearerRequest(f.c.a, f.writer, "GET", "/api/sflow/bindings?exporterId="+str(f.exporter, "id"), nil))
	tag := uint32(100)
	return sflow.Batch{Version: 1, ExporterID: str(f.exporter, "id"), SwitchID: f.switchID, MappingRevision: str(binding, "mappingRevision"), ReceivedAt: time.Now().UTC(), TransportSource: "192.0.2.250", Datagram: sflow.Datagram{Version: 5, AgentAddress: "192.0.2.250", Sequence: sequence, UptimeMS: sequence * 1000, Flows: []sflow.Flow{{Sequence: sequence, SourceType: 0, SourceIndex: 1, SamplingRate: 100, SamplePool: 100 * sequence, Input: sflow.Interface{Value: 1}, Output: sflow.Interface{Value: 2}, PacketSize: 64, SourceMAC: "02:00:00:00:00:01", DestinationMAC: "02:00:00:00:00:02", Family: 4, HeaderVLANs: []uint16{100}, InputVLAN: &tag, OutputVLAN: &tag, EstimatedBytes: "6400", EstimatedPackets: "100", SampleCount: 1, SampledTCP179Activity: true}}, Counters: []sflow.CounterSample{}}, Quality: sflow.Quality{SequenceState: "first"}}
}
func flowQuery(c *client) Record {
	q := url.Values{"from": {time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)}, "to": {time.Now().UTC().Format(time.RFC3339)}, "resolution": {"5m"}}
	return c.ok("GET", "/api/sflow/query?"+q.Encode(), nil)
}
func TestSFlowHTTPCurrentAttributionPrivacyAndDurableDuplicate(t *testing.T) {
	f := newFlowAPIFixture(t)
	owner := memberDiagnosticClient(t, f.c, str(f.member, "id"), "flow-owner@example.net")
	edit := editableCredentialPeer(f.c.ok("GET", "/api/resources/members/"+str(f.otherMember, "id"), nil))
	edit["peeringMatrixEnabled"] = false
	f.c.ok("PUT", "/api/resources/members/"+str(f.otherMember, "id"), edit)
	first := f.batch(t, 1)
	reply := responseRecord(t, bearerRequest(f.c.a, f.writer, "POST", "/api/sflow/ingest", first))
	if reply["acceptedFlows"] != float64(1) || reply["unattributedFlows"] != float64(0) {
		t.Fatal(reply)
	}
	private := flowQuery(owner)
	rows := private["items"].([]any)
	if len(rows) != 1 {
		t.Fatal(private)
	}
	row := rows[0].(map[string]any)
	if row["estimatedBytes"] != "6400" || row["tcp179Samples"] != "1" || row["destinationMemberId"] == f.otherMember["id"] || row["exporterId"] != nil || row["destinationConnectionId"] != nil || row["destinationConnectionLabel"] != "接入信息受限" || private["quality"] != nil {
		t.Fatal("member projection leaked private scope", private)
	}
	if w := owner.request("GET", "/api/sflow/query?exporterId="+str(f.exporter, "id"), nil); w.Code != 403 {
		t.Fatal("member enumerated private exporter")
	}
	dir := filepath.Dir(f.c.a.store.path)
	f.c.a.Close()
	next, err := New(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	f.c.a = next
	replayed := responseRecord(t, bearerRequest(next, f.writer, "POST", "/api/sflow/ingest", first))
	if replayed["acceptedFlows"] != float64(0) {
		t.Fatal("restarted collector replay counted twice", replayed)
	}
	f.c = docLoginClient(t, next, "admin@example.net", "very-long-test-password")
	owner = docLoginClient(t, next, "flow-owner@example.net", "member-diagnostic-password")
	second := f.batch(t, 2)
	// Keep this pair in the same aggregation bucket even if the test happens
	// to cross a real UTC five-minute boundary during its restart/login work.
	second.ReceivedAt = first.ReceivedAt.Add(time.Millisecond)
	second.Datagram.Flows[0].SampledTCP179Activity = false
	responseRecord(t, bearerRequest(next, f.writer, "POST", "/api/sflow/ingest", second))
	row = flowQuery(owner)["items"].([]any)[0].(map[string]any)
	stamp, ok := row["lastTcp179At"].(string)
	if !ok {
		t.Fatal("missing activity evidence", row)
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || !at.Equal(first.ReceivedAt) || row["estimatedBytes"] != "12800" || row["tcp179Samples"] != "1" {
		t.Fatal("TCP activity time or estimate lost", row)
	}
	stale := f.batch(t, 3)
	f.c.create("mac-addresses", Record{"name": "new MAC", "connectionId": f.connection["id"], "address": "02:00:00:00:00:03"})
	if w := bearerRequest(next, f.writer, "POST", "/api/sflow/ingest", stale); w.Code != 409 {
		t.Fatal("old mapping accepted", w.Code, w.Body.String())
	}
	current := f.batch(t, 3)
	current.Datagram.Flows[0].SourceIndex = 2
	reply = responseRecord(t, bearerRequest(next, f.writer, "POST", "/api/sflow/ingest", current))
	if reply["unattributedFlows"] != float64(1) {
		t.Fatal("egress observation double-counted")
	}
	if rows := flowQuery(owner)["items"].([]any); len(rows) != 1 || rows[0].(map[string]any)["estimatedBytes"] != "12800" {
		t.Fatal("unattributed core/egress frame exposed to member")
	}
}

func TestSFlowScopedDirectoryAndCacheInvalidation(t *testing.T) {
	f := newFlowAPIFixture(t)
	member := f.c.create("members", Record{"name": "Foreign private member", "asn": 64510, "status": "active"})
	loc := f.c.create("locations", Record{"name": "Other DC"})
	sw := f.c.create("switches", Record{"name": "Other switch", "locationId": loc["id"]})
	p := f.c.create("ports", Record{"name": "other", "switchId": sw["id"], "speed": 1000})
	f.c.create("connections", Record{"name": "Foreign link", "memberId": member["id"], "portId": p["id"], "vlanId": f.connection["vlanId"], "status": "active"})
	read := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "Scoped flow reader", "currentPassword": "very-long-test-password", "scopes": []string{"sflow:read"}, "switchIds": []string{f.switchID}}), "token")
	w := bearerRequest(f.c.a, read, "GET", "/api/sflow", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), str(member, "id")) || strings.Contains(w.Body.String(), str(sw, "id")) {
		t.Fatal("scoped catalog leaked foreign inventory", w.Body.String())
	}
	for _, id := range []string{str(member, "id"), "missing-member"} {
		if r := bearerRequest(f.c.a, read, "GET", "/api/sflow/query?memberId="+id, nil); r.Code != 404 {
			t.Fatal("foreign member existence leaked", r.Code)
		}
	}
	batch := f.batch(t, 1)
	before := batch.MappingRevision
	oldPacket := batch
	oldPacket.ReceivedAt = time.Now().UTC().Add(-time.Minute)
	if w := bearerRequest(f.c.a, f.writer, "POST", "/api/sflow/ingest", oldPacket); w.Code != 409 {
		t.Fatal("old packet was relabeled with a newly obtained mapping", w.Code, w.Body.String())
	}
	state := f.c.a.store.Snapshot()
	e := editableCredentialPeer(find(&state, sflowExporterKind, str(f.exporter, "id")))
	e["enabled"] = false
	f.c.ok("PUT", "/api/resources/"+sflowExporterKind+"/"+str(f.exporter, "id"), e)
	if w := bearerRequest(f.c.a, f.writer, "POST", "/api/sflow/ingest", batch); w.Code != 409 {
		t.Fatal("cached enabled exporter survived inventory change", w.Code)
	}
	e["enabled"] = true
	f.c.ok("PUT", "/api/resources/"+sflowExporterKind+"/"+str(f.exporter, "id"), e)
	after := f.batch(t, 1)
	if after.MappingRevision == before {
		t.Fatal("exporter reconfiguration did not change mapping review")
	}
	bad := after
	bad.TransportSource = "192.0.2.251"
	if w := bearerRequest(f.c.a, f.writer, "POST", "/api/sflow/ingest", bad); w.Code != 422 {
		t.Fatal("transport identity not checked")
	}
	user, session, ok := f.c.a.authenticateToken(f.writer)
	if !ok {
		t.Fatal("token missing")
	}
	_ = user
	if !f.c.a.acquireSFlowUpload(session.TokenID) || !f.c.a.acquireSFlowUpload(session.TokenID) {
		t.Fatal("initial slots unavailable")
	}
	w = bearerRequest(f.c.a, f.writer, "POST", "/api/sflow/ingest", after)
	f.c.a.releaseSFlowUpload(session.TokenID)
	f.c.a.releaseSFlowUpload(session.TokenID)
	if w.Code != 429 {
		t.Fatal("unbounded per-token upload queue")
	}
	encoded, _ := json.Marshal(flowQuery(f.c))
	if strings.Contains(string(encoded), "rawHeader") || strings.Contains(string(encoded), "sourceAddress") {
		t.Fatal("packet payload or private transport entered report")
	}
}
