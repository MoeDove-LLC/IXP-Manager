package app

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
	"github.com/moedove/ixp-manager/internal/telemetry"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type monitorFixture struct {
	c                                                                           *client
	router, otherRouter, member, otherMember, port, otherPort, connection, peer Record
	token                                                                       string
}

func monitoringFixture(t *testing.T) monitorFixture {
	c, _ := setupClient(t)
	loc := c.create("locations", Record{"name": "Telemetry DC"})
	sw := c.create("switches", Record{"name": "sw", "locationId": loc["id"]})
	port := c.create("ports", Record{"name": "eth1", "switchId": sw["id"], "status": "active", "speed": 1000})
	otherPort := c.create("ports", Record{"name": "eth2", "switchId": sw["id"], "status": "active", "speed": 1000})
	member := c.create("members", Record{"name": "One", "asn": 64501, "status": "active"})
	otherMember := c.create("members", Record{"name": "Two", "asn": 64502, "status": "active"})
	vlan := c.create("vlans", Record{"name": "Peering", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	connection := c.create("connections", Record{"name": "Uplink1", "memberId": member["id"], "portId": port["id"], "vlanId": vlan["id"], "ipv4": "192.0.2.10", "status": "active"})
	c.create("connections", Record{"name": "Uplink2", "memberId": otherMember["id"], "portId": otherPort["id"], "vlanId": vlan["id"], "ipv4": "192.0.2.11", "status": "active"})
	router := c.create("routers", Record{"name": "rs1", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": vlan["id"]})
	otherRouter := c.create("routers", Record{"name": "rs2", "asn": 64500, "routerId": "192.0.2.2", "localIpv4": "192.0.2.2", "vlanId": vlan["id"]})
	// This fixture deliberately has two router candidates. Select its known
	// counter producer explicitly; production auto mode must stay ambiguous.
	port = c.ok("PUT", "/api/resources/ports/"+str(port, "id"), Record{"name": "eth1", "switchId": sw["id"], "status": "active", "speed": 1000, "counterSource": "router:" + str(router, "id")})
	otherPort = c.ok("PUT", "/api/resources/ports/"+str(otherPort, "id"), Record{"name": "eth2", "switchId": sw["id"], "status": "active", "speed": 1000, "counterSource": "router:" + str(router, "id")})
	peer := c.create("peers", Record{"name": "Peer one", "routerId": router["id"], "memberId": member["id"], "address": "192.0.2.10", "asn": 64501, "enabled": true, "maxPrefixes": 100, "allowedPrefixes": []string{"198.51.100.0/24"}})
	raw := str(c.ok("POST", "/api/account/tokens", Record{"name": "collector", "currentPassword": "very-long-test-password", "scopes": []string{"monitoring:write"}, "routerIds": []string{str(router, "id")}}), "token")
	if err := c.a.store.Update(func(s *State) error {
		for _, kind := range []string{"ports", "connections", "peers"} {
			for _, r := range s.Resources[kind] {
				r["ownershipSince"] = time.Now().Add(-15 * time.Minute).UTC().Format(time.RFC3339Nano)
				if kind == "connections" {
					r["bindingSince"] = r["ownershipSince"]
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return monitorFixture{c: c, router: router, otherRouter: otherRouter, member: member, otherMember: otherMember, port: port, otherPort: otherPort, connection: connection, peer: peer, token: raw}
}
func (f monitorFixture) batch(at time.Time, in string) Record {
	s := f.c.a.store.Snapshot()
	portRevision := monitoringBindingRevision(&s, "port", str(f.port, "id"), str(f.router, "id"))
	peerRevision := monitoringBindingRevision(&s, "peer", str(f.peer, "id"), str(f.router, "id"))
	return Record{"routerId": f.router["id"], "observedAt": at.Format(time.RFC3339Nano), "ports": []Record{{"portId": f.port["id"], "ownershipRevision": portRevision, "inOctets": in, "outOctets": "10000", "operStatus": "up", "counterBits": 64, "counterEpoch": "boot:2", "memberId": f.otherMember["id"]}}, "sessions": []Record{{"peerId": f.peer["id"], "ownershipRevision": peerRevision, "state": "Established", "neighborAddress": f.peer["address"], "neighborAsn": f.peer["asn"], "receivedPrefixes": 10, "memberId": f.otherMember["id"]}}}
}
func TestMonitoringScopedIngestionRatesAndUnknown(t *testing.T) {
	f := monitoringFixture(t)
	summary := f.c.ok("GET", "/api/monitoring/summary", nil)
	for _, raw := range summary["ports"].([]any) {
		if raw.(map[string]any)["status"] != "unknown" {
			t.Fatal("unsampled port did not remain unknown")
		}
	}
	now := time.Now().UTC().Add(-20 * time.Second)
	batch := f.batch(now, "9007199254740993")
	if w := f.c.request("POST", "/api/monitoring/ingest", batch); w.Code != 403 {
		t.Fatal("browser cookie ingested collector observations")
	}
	for _, b := range []Record{batch, f.batch(now.Add(10*time.Second), "9007199254742243")} {
		w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", b)
		if w.Code != 200 {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	summary = f.c.ok("GET", "/api/monitoring/summary", nil)
	found := false
	for _, raw := range summary["ports"].([]any) {
		p := raw.(map[string]any)
		if p["portId"] == f.port["id"] {
			found = true
			if p["inBps"] != float64(1000) || p["inOctets"] != "9007199254742243" || p["status"] != "fresh" {
				t.Fatal(p)
			}
		}
	}
	if !found {
		t.Fatal("missing observed port")
	}
	batch["routerId"] = f.otherRouter["id"]
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", batch); w.Code != 403 {
		t.Fatal("collector token crossed router binding")
	}
	if w := bearerRequest(f.c.a, f.token, "GET", "/api/resources/members", nil); w.Code != 403 {
		t.Fatal("collector token read control-plane resources")
	}
}
func TestMonitoringMemberIsolationAndPortReassignment(t *testing.T) {
	f := monitoringFixture(t)
	f.c.create("users", Record{"name": "Member two", "email": "two@example.net", "role": "member", "memberId": f.otherMember["id"], "password": "member-two-password"})
	member := loginClient(t, f.c.a, Record{"email": "two@example.net", "password": "member-two-password"})
	now := time.Now().UTC().Add(-30 * time.Second)
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", f.batch(now, "9007199254740993")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	before := member.request("GET", "/api/monitoring/summary", nil)
	if strings.Contains(before.Body.String(), str(f.port, "id")) || strings.Contains(before.Body.String(), str(f.peer, "id")) {
		t.Fatal("member saw another tenant port/session")
	}
	if w := member.request("GET", "/api/monitoring/series?portId="+str(f.port, "id"), nil); w.Code != 404 {
		t.Fatal("member read another tenant series")
	}
	f.c.ok("DELETE", "/api/resources/connections/"+str(f.connection, "id"), nil)
	f.c.create("connections", Record{"name": "Reassigned", "memberId": f.otherMember["id"], "portId": f.port["id"], "vlanId": f.connection["vlanId"], "ipv4": "192.0.2.10", "status": "active"})
	// The injected client memberId must never have reassigned old samples.
	history := member.ok("GET", "/api/monitoring/series?portId="+str(f.port, "id"), nil)
	if len(history["points"].([]any)) != 0 {
		t.Fatal("new tenant read prior owner's samples")
	}
	for _, at := range []time.Time{now.Add(10 * time.Second), now.Add(20 * time.Second)} {
		w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", f.batch(at, "9007199254742243"))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	delayed := member.ok("GET", "/api/monitoring/series?portId="+str(f.port, "id"), nil)
	if len(delayed["points"].([]any)) != 0 {
		t.Fatal("delayed pre-assignment counters leaked")
	}
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", f.batch(time.Now().UTC(), "9007199254749999")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	history = member.ok("GET", "/api/monitoring/series?portId="+str(f.port, "id"), nil)
	points := history["points"].([]any)
	if len(points) != 1 {
		t.Fatal(history)
	}
	point := points[0].(map[string]any)
	if point["inBps"] != nil || point["inOctets"] != nil || point["counterReset"] != true {
		t.Fatal("ownership transfer exposed prior traffic totals or rate")
	}
}
func TestMonitoringStaleSummaryAnd24HourWindow(t *testing.T) {
	f := monitoringFixture(t)
	old := time.Now().UTC().Add(-3 * time.Minute)
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", f.batch(old, "100")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	summary := f.c.ok("GET", "/api/monitoring/summary", nil)
	raw, _ := json.Marshal(summary)
	if !strings.Contains(string(raw), `"status":"stale"`) {
		t.Fatal("old data reported as live")
	}
	from := url.QueryEscape(time.Now().Add(-24*time.Hour - time.Second).Format(time.RFC3339Nano))
	if w := f.c.request("GET", "/api/monitoring/series?portId="+str(f.port, "id")+"&from="+from, nil); w.Code != 200 {
		t.Fatal("24h chart rejected normal client/request clock delay")
	}
}

func TestCollectorBindingRejectsFutureDatedPreTransferBatch(t *testing.T) {
	f := monitoringFixture(t)
	captured := f.batch(time.Now().UTC().Add(10*time.Second), "9007199254749999")
	f.c.ok("DELETE", "/api/resources/connections/"+str(f.connection, "id"), nil)
	f.c.create("connections", Record{"name": "New owner", "memberId": f.otherMember["id"], "portId": f.port["id"], "vlanId": f.connection["vlanId"], "ipv4": "192.0.2.10", "status": "active"})
	w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", captured)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var ack Record
	json.Unmarshal(w.Body.Bytes(), &ack)
	if ack["acceptedPorts"] != float64(0) {
		t.Fatal("future skew bypassed ownership revision")
	}
	state := f.c.a.telemetry.Snapshot(time.Now())
	if _, ok := state.Ports[str(f.port, "id")]; ok {
		t.Fatal("pre-transfer counters stored for newowner")
	}
}
func TestSameMemberReallocationResetsCounterEpoch(t *testing.T) {
	f := monitoringFixture(t)
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", f.batch(time.Now().Add(-10*time.Second), "1000")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f.c.ok("DELETE", "/api/resources/connections/"+str(f.connection, "id"), nil)
	temp := f.c.create("connections", Record{"name": "TemporaryB", "memberId": f.otherMember["id"], "portId": f.port["id"], "vlanId": f.connection["vlanId"], "ipv4": "192.0.2.10"})
	f.c.ok("DELETE", "/api/resources/connections/"+str(temp, "id"), nil)
	f.c.create("connections", Record{"name": "New A tenancy", "memberId": f.member["id"], "portId": f.port["id"], "vlanId": f.connection["vlanId"], "ipv4": "192.0.2.10"})
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", f.batch(time.Now(), "900000")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	point := f.c.a.telemetry.Snapshot(time.Now()).Ports[str(f.port, "id")]
	if point.InBps != nil || !point.CounterReset {
		t.Fatal("B's traffic included in A's newperiod rate")
	}
	before := f.c.a.store.Snapshot()
	revision := monitoringBindingRevision(&before, "port", str(f.port, "id"), str(f.router, "id"))
	port := clean(find(&before, "ports", str(f.port, "id")))
	delete(port, "id")
	delete(port, "createdAt")
	delete(port, "updatedAt")
	port["name"] = "cosmetic rename"
	f.c.ok("PUT", "/api/resources/ports/"+str(f.port, "id"), port)
	after := f.c.a.store.Snapshot()
	if monitoringBindingRevision(&after, "port", str(f.port, "id"), str(f.router, "id")) != revision {
		t.Fatal("cosmetic edit reset ownership")
	}
}

func monitorSession(t *testing.T, c *client, id string) Record {
	t.Helper()
	summary := c.ok("GET", "/api/monitoring/summary", nil)
	for _, value := range summary["sessions"].([]any) {
		row := value.(map[string]any)
		if row["peerId"] == id {
			return Record(row)
		}
	}
	t.Fatal("session missing from summary")
	return nil
}
func TestObservedBGPMismatchClearsPriorEstablishedAndProtectsMemberPrivacy(t *testing.T) {
	f := monitoringFixture(t)
	f.c.create("users", Record{"name": "Observer member", "email": "identity-member@example.net", "role": "member", "memberId": f.member["id"], "password": "identity-member-password"})
	member := loginClient(t, f.c.a, Record{"email": "identity-member@example.net", "password": "identity-member-password"})
	now := time.Now().UTC()
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", f.batch(now.Add(-10*time.Second), "100")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if monitorSession(t, member, str(f.peer, "id"))["state"] != "Established" {
		t.Fatal("verified baseline not observed")
	}
	wrong := f.batch(now, "200")
	sessions := wrong["sessions"].([]Record)
	sessions[0]["neighborAddress"] = "192.0.2.11"
	sessions[0]["neighborAsn"] = 64502
	w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", wrong)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var ack Record
	json.Unmarshal(w.Body.Bytes(), &ack)
	if ack["identityMismatches"] != float64(1) || ack["acceptedPorts"] != float64(1) {
		t.Fatal("mismatch did not retain independent valid port samples")
	}
	staffRow := monitorSession(t, f.c, str(f.peer, "id"))
	if staffRow["state"] != "unknown" || staffRow["identityVerified"] != false || staffRow["observedNeighborAddress"] != "192.0.2.11" {
		t.Fatal("mismatch was attached to current configured identity")
	}
	memberRow := monitorSession(t, member, str(f.peer, "id"))
	if memberRow["state"] != "unknown" || memberRow["observedNeighborAddress"] != nil || memberRow["observedNeighborAsn"] != nil || memberRow["receivedPrefixes"] != nil {
		t.Fatal("mismatched other-neighbor identity or prefixes leaked to member")
	}
	if memberRow["address"] != "192.0.2.10" {
		t.Fatal("desired identity was overwritten with reported mismatch")
	}
}
func TestPeerReconfigurationDoesNotRelabelOldNativeSession(t *testing.T) {
	f := monitoringFixture(t)
	now := time.Now().UTC()
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", f.batch(now.Add(-10*time.Second), "100")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	resourceUpdate(t, f.c, "connections", str(f.connection, "id"), Record{"ipv4": "192.0.2.50"})
	resourceUpdate(t, f.c, "peers", str(f.peer, "id"), Record{"address": "192.0.2.50"})
	if row := monitorSession(t, f.c, str(f.peer, "id")); row["state"] != "unknown" {
		t.Fatal("old stored neighbor relabeled as newly configured address")
	}
	oldDaemon := f.batch(time.Now().UTC(), "200")
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", oldDaemon); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if row := monitorSession(t, f.c, str(f.peer, "id")); row["state"] != "unknown" {
		t.Fatal("fresh ownership token falsely verified old deployed identity")
	}
	updated := f.batch(time.Now().UTC().Add(2*time.Second), "300")
	updated["sessions"].([]Record)[0]["neighborAddress"] = "192.0.2.50"
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", updated); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if row := monitorSession(t, f.c, str(f.peer, "id")); row["state"] != "Established" || row["observedNeighborAddress"] != "192.0.2.50" {
		t.Fatal("new matching native identity not recognized")
	}
}
func TestLegacySessionJournalLoadsButSummaryRemainsUnverified(t *testing.T) {
	f := monitoringFixture(t)
	f.c.a.telemetry.Close()
	raw, _ := json.Marshal(Record{"routerId": f.router["id"], "observedAt": time.Now().UTC(), "sessions": []Record{{"peerId": f.peer["id"], "memberId": f.member["id"], "state": "Established", "receivedPrefixes": 100}}})
	frame := make([]byte, 36+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	sum := sha256.Sum256(raw)
	copy(frame[4:36], sum[:])
	copy(frame[36:], raw)
	dir := filepath.Join(filepath.Dir(f.c.a.store.path), "telemetry")
	if err := os.WriteFile(filepath.Join(dir, "journal.bin"), frame, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := telemetry.Open(dir)
	if err != nil {
		t.Fatal("legacy journal failed to load", err)
	}
	f.c.a.telemetry = store
	row := monitorSession(t, f.c, str(f.peer, "id"))
	if row["state"] != "unknown" || row["identityVerified"] != false || row["receivedPrefixes"] != nil {
		t.Fatal("legacy unverified session was shown as established")
	}
}

func TestStoredFuturePointCannotReappearAfterReturningTenancy(t *testing.T) {
	f := monitoringFixture(t)
	f.c.create("users", Record{"name": "Tenant A", "email": "future-lineage@example.net", "role": "member", "memberId": f.member["id"], "password": "future-lineage-password"})
	member := loginClient(t, f.c.a, Record{"email": "future-lineage@example.net", "password": "future-lineage-password"})
	at := time.Now().UTC().Add(15 * time.Second)
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", f.batch(at, "1000")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f.c.ok("DELETE", "/api/resources/connections/"+str(f.connection, "id"), nil)
	other := f.c.create("connections", Record{"name": "Temporary other owner", "memberId": f.otherMember["id"], "portId": f.port["id"], "vlanId": f.connection["vlanId"], "ipv4": "192.0.2.10"})
	f.c.ok("DELETE", "/api/resources/connections/"+str(other, "id"), nil)
	f.c.create("connections", Record{"name": "Returned tenant", "memberId": f.member["id"], "portId": f.port["id"], "vlanId": f.connection["vlanId"], "ipv4": "192.0.2.10"})
	for _, c := range []*client{member, f.c} {
		summary := c.ok("GET", "/api/monitoring/summary", nil)
		for _, raw := range summary["ports"].([]any) {
			p := raw.(map[string]any)
			if p["portId"] == f.port["id"] && p["status"] != "unknown" {
				t.Fatal("future old-lineage port point appeared current")
			}
		}
		if row := monitorSession(t, c, str(f.peer, "id")); row["state"] != "unknown" {
			t.Fatal("future old-lineage BGP point appeared current")
		}
	}
	to := url.QueryEscape(at.Add(time.Second).Format(time.RFC3339Nano))
	series := member.ok("GET", "/api/monitoring/series?portId="+str(f.port, "id")+"&to="+to, nil)
	if len(series["points"].([]any)) != 0 {
		t.Fatal("current member received prior-tenancy future sample")
	}
	staff := f.c.ok("GET", "/api/monitoring/series?portId="+str(f.port, "id")+"&to="+to, nil)
	if len(staff["points"].([]any)) != 1 {
		t.Fatal("staff historical evidence was incorrectly erased")
	}
}

func TestDirectOwnedSNMPPortAppearsInMemberMonitoring(t *testing.T) {
	f := deviceFixture(t)
	proposal := f.propose(t, f.snapshot(time.Now().UTC(), "9007199254740993"))
	f.approve(t, proposal)
	f.c.create("users", Record{"name": "Direct port owner", "email": "direct-port-owner@example.net", "role": "member", "memberId": f.member, "password": "direct-port-owner-password"})
	member := loginClient(t, f.c.a, Record{"email": "direct-port-owner@example.net", "password": "direct-port-owner-password"})
	start := time.Now().UTC().Add(time.Second)
	first := f.stamp(t, f.snapshot(start, "9007199254740993"))
	second := f.stamp(t, f.snapshot(start.Add(10*time.Second), "9007199254742243"))
	second.UptimeTicks = "101000"
	for _, sample := range []devicesnmp.Snapshot{first, second} {
		if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", sample); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	summary := member.ok("GET", "/api/monitoring/summary", nil)
	ports := summary["ports"].([]any)
	if len(ports) != 1 {
		t.Fatal("direct owner missing, or unassigned core port exposed")
	}
	port := ports[0].(map[string]any)
	if port["portId"] != f.port || port["inBps"] != float64(1000) || port["inOctets"] != nil {
		t.Fatal("SNMP lineage did not reach the member monitoring view safely")
	}
	to := url.QueryEscape(start.Add(11 * time.Second).Format(time.RFC3339Nano))
	history := member.ok("GET", "/api/monitoring/series?portId="+f.port+"&to="+to, nil)
	if len(history["points"].([]any)) != 2 {
		t.Fatal("direct-owned SNMP history missing")
	}
	if w := member.request("GET", "/api/monitoring/series?portId="+f.core, nil); w.Code != 404 {
		t.Fatal("unassigned core history exposed")
	}
}
