package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
)

type deviceTestFixture struct {
	c                                                             *client
	switchID, otherSwitch, member, otherMember, port, core, token string
}

func deviceFixture(t *testing.T) deviceTestFixture {
	c, _ := setupClient(t)
	loc := c.create("locations", Record{"name": "Device DC"})
	sw := c.create("switches", Record{"name": "switch-one", "locationId": loc["id"], "managementIp": "192.0.2.250"})
	other := c.create("switches", Record{"name": "switch-two", "locationId": loc["id"]})
	member := c.create("members", Record{"name": "Tenant A", "asn": 64501, "status": "active"})
	otherMember := c.create("members", Record{"name": "Tenant B", "asn": 64502, "status": "active"})
	port := c.create("ports", Record{"name": "reviewed-port-name", "switchId": sw["id"], "memberId": member["id"], "speed": 1000, "status": "active"})
	core := c.create("ports", Record{"name": "core-port", "switchId": sw["id"], "speed": 10000, "status": "active"})
	raw := str(c.ok("POST", "/api/account/tokens", Record{"name": "SNMP collector", "currentPassword": "very-long-test-password", "scopes": []string{"devices:read", "devices:write"}, "switchIds": []string{str(sw, "id")}}), "token")
	return deviceTestFixture{c: c, switchID: str(sw, "id"), otherSwitch: str(other, "id"), member: str(member, "id"), otherMember: str(otherMember, "id"), port: str(port, "id"), core: str(core, "id"), token: raw}
}
func (f deviceTestFixture) snapshot(at time.Time, in string) devicesnmp.Snapshot {
	return devicesnmp.Snapshot{Version: 1, SwitchID: f.switchID, ObservedAt: at, Security: "authPriv", EngineIdentity: strings.Repeat("a", 64), Identity: devicesnmp.Identity{Name: "device-private-hostname", Description: "private OS identity", Contact: "private-noc@example.net"}, UptimeTicks: "100000", EngineEpoch: "boot-one", Interfaces: []devicesnmp.Interface{{IfIndex: 1, PortID: f.port, Name: "observed-ethernet1", Alias: "previous-tenant-alias", Description: "internal interface detail", Type: 6, MTU: 9000, SpeedMbps: 10000, AdminStatus: "up", OperStatus: "up", InOctets: in, OutOctets: "9007199254745000", CounterBits: 64, DiscontinuityTicks: "0", InErrors: "9"}, {IfIndex: 2, PortID: f.core, Name: "core-port", Type: 6, MTU: 9000, SpeedMbps: 10000, AdminStatus: "up", OperStatus: "up", InOctets: "5000", OutOctets: "6000", CounterBits: 64, DiscontinuityTicks: "0"}, {IfIndex: 3, Name: "new-port", Type: 6, MTU: 9000, SpeedMbps: 25000, AdminStatus: "down", OperStatus: "down"}}, Entities: []devicesnmp.Entity{}, Sensors: []devicesnmp.Sensor{}, FDB: []devicesnmp.FDBEntry{}, Warnings: []string{}}
}
func (f deviceTestFixture) propose(t *testing.T, snapshot devicesnmp.Snapshot) Record {
	t.Helper()
	return responseRecord(t, bearerRequest(f.c.a, f.token, "POST", "/api/devices/discoveries", snapshot))
}
func (f deviceTestFixture) approve(t *testing.T, proposal Record) Record {
	t.Helper()
	path := "/api/devices/discoveries/" + str(proposal, "id")
	preview := f.c.ok("GET", path+"/preview", nil)
	return f.c.ok("POST", path+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": []Record{{"ifIndex": 1, "portId": f.port}, {"ifIndex": 2, "portId": f.core}, {"ifIndex": 3, "name": "new-port"}}})
}
func (f deviceTestFixture) stamp(t *testing.T, snapshot devicesnmp.Snapshot) devicesnmp.Snapshot {
	t.Helper()
	bindings := responseRecord(t, bearerRequest(f.c.a, f.token, "GET", "/api/devices/bindings?switchId="+f.switchID, nil))
	revisions := map[string]string{}
	for _, raw := range bindings["mappings"].([]any) {
		m := Record(raw.(map[string]any))
		revisions[str(m, "portId")] = str(m, "ownershipRevision")
	}
	for i := range snapshot.Interfaces {
		if snapshot.Interfaces[i].PortID != "" {
			snapshot.Interfaces[i].OwnershipRevision = revisions[snapshot.Interfaces[i].PortID]
		}
	}
	return snapshot
}
func TestDeviceDiscoveryReviewPreservesAssignments(t *testing.T) {
	f := deviceFixture(t)
	snapshot := f.snapshot(time.Now().UTC(), "9007199254740993")
	if w := f.c.request("POST", "/api/devices/discoveries", snapshot); w.Code != 403 {
		t.Fatal("browser invented collector discovery")
	}
	wrong := snapshot
	wrong.SwitchID = f.otherSwitch
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/discoveries", wrong); w.Code != 403 {
		t.Fatal("collector crossed switch binding")
	}
	proposal := f.propose(t, snapshot)
	path := "/api/devices/discoveries/" + str(proposal, "id")
	preview := f.c.ok("GET", path+"/preview", nil)
	if w := bearerRequest(f.c.a, f.token, "POST", path+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": []Record{{"ifIndex": 1, "portId": f.port}}}); w.Code != 403 {
		t.Fatal("collector self-approved inventory changes")
	}
	f.c.ok("PUT", "/api/resources/switches/"+f.switchID, Record{"name": "switch-edited", "locationId": str(find(ptrState(f.c.a.store.Snapshot()), "switches", f.switchID), "locationId")})
	if w := f.c.request("POST", path+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": []Record{{"ifIndex": 1, "portId": f.port}}}); w.Code != 409 {
		t.Fatal("stale review applied")
	}
	result := f.approve(t, proposal)
	if len(result["created"].([]any)) != 1 || len(result["updated"].([]any)) != 2 {
		t.Fatal("missing reviewed inventory changes")
	}
	state := f.c.a.store.Snapshot()
	port := find(&state, "ports", f.port)
	if str(port, "memberId") != f.member || str(port, "name") != "reviewed-port-name" || number(port, "speed") != 1000 || str(port, "status") != "active" || number(port, "snmpIfIndex") != 1 {
		t.Fatal("discovery overwrote member-owned inventory intent")
	}
	created := find(&state, "ports", result["created"].([]any)[0].(string))
	if str(created, "status") != "inactive" || str(created, "memberId") != "" {
		t.Fatal("new discovered port became automatically assigned/active")
	}
	f.c.ok("PUT", "/api/resources/ports/"+f.port, Record{"name": "safe metadata edit", "switchId": f.switchID, "memberId": f.member, "status": "active", "speed": 1000})
	state = f.c.a.store.Snapshot()
	if number(find(&state, "ports", f.port), "snmpIfIndex") != 1 {
		t.Fatal("ordinary PUT erased reviewed mapping")
	}
	if w := f.c.request("PUT", "/api/resources/ports/"+f.port, Record{"name": "unsafe mapping", "switchId": f.switchID, "snmpIfIndex": 99}); w.Code != 400 {
		t.Fatal("generic mutation bypassed mapping review")
	}
	if w := f.c.request("POST", path+"/apply", Record{"baseChecksum": deviceBaseChecksum(&state, f.switchID), "ports": []Record{{"ifIndex": 1, "portId": f.port}}}); w.Code != 409 {
		t.Fatal("proposal applied twice")
	}
}
func TestDeviceObservationsCountersAndTenantIsolation(t *testing.T) {
	f := deviceFixture(t)
	proposal := f.propose(t, f.snapshot(time.Now().UTC(), "9007199254740993"))
	f.approve(t, proposal)
	f.c.create("users", Record{"name": "Tenant account", "email": "device-tenant@example.net", "role": "member", "memberId": f.member, "password": "device-member-password"})
	member := docLoginClient(t, f.c.a, "device-tenant@example.net", "device-member-password")
	start := time.Now().UTC().Add(time.Second)
	first := f.stamp(t, f.snapshot(start, "9007199254740993"))
	second := f.stamp(t, f.snapshot(start.Add(10*time.Second), "9007199254742243"))
	second.UptimeTicks = "101000"
	for _, snapshot := range []devicesnmp.Snapshot{first, second} {
		w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", snapshot)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	summary := member.ok("GET", "/api/devices/ports", nil)
	ports := summary["items"].([]any)
	if len(ports) != 1 {
		t.Fatal("member saw core/unassigned ports")
	}
	port := Record(ports[0].(map[string]any))
	if port["inBps"] != float64(1000) {
		t.Fatalf("64-bit counter delta lost precision: %+v", port)
	}
	raw, _ := json.Marshal(summary)
	for _, secret := range []string{"previous-tenant-alias", "internal interface detail", "private-noc", "inOctets", "inErrors", f.core} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("member diagnostics disclosed %s", secret)
		}
	}
	status := member.ok("GET", "/api/devices/status", nil)
	statusJSON, _ := json.Marshal(status)
	if strings.Contains(string(statusJSON), "identity") || strings.Contains(string(statusJSON), "device-private-hostname") {
		t.Fatal("device identity leaked to member")
	}
	if w := member.request("GET", "/api/devices/discoveries", nil); w.Code != 403 {
		t.Fatal("member read discovery/FDB inventory")
	}
	points := f.c.a.telemetry.Series(f.core, start.Add(-time.Second), start.Add(time.Minute))
	if len(points) != 2 || points[0].MemberID != "" || points[0].OwnershipRevision == "" {
		t.Fatal("unassigned core counters not retained as staff-only")
	}
	failure := devicesnmp.Failure(devicesnmp.Config{SwitchID: f.switchID, Version: "3"}, os.ErrDeadlineExceeded)
	failure.ObservedAt = start.Add(20 * time.Second)
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", failure); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if len(f.c.a.telemetry.Series(f.core, start.Add(-time.Second), start.Add(time.Minute))) != 2 {
		t.Fatal("failed poll fabricated a counter sample")
	}
	failed := member.ok("GET", "/api/devices/ports", nil)["items"].([]any)[0].(map[string]any)
	if failed["status"] != "error" || failed["inBps"] != nil {
		t.Fatal("failed collection displayed a current traffic rate")
	}
}
func TestDeviceBindingRevisionClosesReassignmentRace(t *testing.T) {
	f := deviceFixture(t)
	f.approve(t, f.propose(t, f.snapshot(time.Now().UTC(), "1000")))
	captured := f.stamp(t, f.snapshot(time.Now().UTC().Add(10*time.Second), "9007199254740993"))
	f.c.ok("PUT", "/api/resources/ports/"+f.port, Record{"name": "reviewed-port-name", "switchId": f.switchID, "memberId": f.otherMember, "speed": 1000, "status": "active"})
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", captured); w.Code != 422 || !strings.Contains(w.Body.String(), "ownership revision") {
		t.Fatal("captured old-owner sample attributed after reassignment")
	}
	if points := f.c.a.telemetry.Series(f.port, time.Now().Add(-time.Minute), time.Now().Add(time.Minute)); len(points) != 0 {
		t.Fatal("stale ownership sample entered history")
	}
	current := f.stamp(t, captured)
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", current); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	points := f.c.a.telemetry.Series(f.port, time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if len(points) != 1 || points[0].MemberID != f.otherMember {
		t.Fatal("server ownership was not stamped")
	}
}
func TestDeviceCounterDiscontinuityAndEngineChange(t *testing.T) {
	f := deviceFixture(t)
	proposal := f.propose(t, f.snapshot(time.Now().UTC(), "1000"))
	f.approve(t, proposal)
	start := time.Now().UTC().Add(time.Second)
	first := f.stamp(t, f.snapshot(start, "1000"))
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", first); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	second := f.stamp(t, f.snapshot(start.Add(10*time.Second), "2000"))
	second.UptimeTicks = "101000"
	second.Interfaces[0].DiscontinuityTicks = "100500"
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", second); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	points := f.c.a.telemetry.Series(f.port, start.Add(-time.Second), start.Add(time.Minute))
	if len(points) != 2 || !points[1].CounterReset || points[1].InBps != nil {
		t.Fatal("counter discontinuity caused fake rate")
	}
	third := f.stamp(t, f.snapshot(start.Add(20*time.Second), "3000"))
	third.UptimeTicks = "500"
	third.EngineEpoch = "new-device-boot"
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", third); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	points = f.c.a.telemetry.Series(f.port, start.Add(-time.Second), start.Add(time.Minute))
	if !points[2].CounterReset {
		t.Fatal("device reboot did not reset baseline")
	}
}
func TestDeviceFilesPrivateAndClosedInstanceCannotPublish(t *testing.T) {
	f := deviceFixture(t)
	proposal := f.propose(t, f.snapshot(time.Now().UTC(), "1000"))
	state := f.c.a.store.Snapshot()
	meta := discoveryByID(&state, str(proposal, "id"))
	info, err := os.Stat(filepath.Join(f.c.a.deviceDir(), meta.SnapshotFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("discovery snapshot not private")
	}
	f.c.a.Close()
	if _, err = f.c.a.writeDeviceFile(observationFile(f.switchID), deviceObservation{Version: 1, SwitchID: f.switchID}, false); err == nil {
		t.Fatal("closed old instance overwrote observation cache")
	}
	var snapshot devicesnmp.Snapshot
	if _, err = f.c.a.readDeviceFile(meta.SnapshotFile, &snapshot); err == nil {
		t.Fatal("closed old instance read potentially new-owner data")
	}
}

func TestDeviceMappingReplacementIsExplicitAndAtomic(t *testing.T) {
	f := deviceFixture(t)
	f.approve(t, f.propose(t, f.snapshot(time.Now().UTC(), "1000")))
	proposal := f.propose(t, f.snapshot(time.Now().UTC(), "2000"))
	path := "/api/devices/discoveries/" + str(proposal, "id")
	preview := f.c.ok("GET", path+"/preview", nil)
	selection := []Record{{"ifIndex": 1, "portId": f.core}, {"ifIndex": 2, "portId": f.port}}
	if w := f.c.request("POST", path+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": selection}); w.Code != 409 {
		t.Fatal("ifIndex remapping happened without explicit replacement")
	}
	before := f.c.a.store.Snapshot()
	if number(find(&before, "ports", f.port), "snmpIfIndex") != 1 || number(find(&before, "ports", f.core), "snmpIfIndex") != 2 {
		t.Fatal("rejected mapping partially applied")
	}
	for _, row := range selection {
		row["replaceMapping"] = true
	}
	f.c.ok("POST", path+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": selection})
	after := f.c.a.store.Snapshot()
	if number(find(&after, "ports", f.port), "snmpIfIndex") != 2 || number(find(&after, "ports", f.core), "snmpIfIndex") != 1 || str(find(&after, "ports", f.port), "memberId") != f.member {
		t.Fatal("atomic mapping swap lost identity/ownership")
	}
}
func TestDeviceRebootInvalidatesPendingDiscovery(t *testing.T) {
	f := deviceFixture(t)
	f.approve(t, f.propose(t, f.snapshot(time.Now().UTC(), "1000")))
	pending := f.propose(t, f.snapshot(time.Now().UTC(), "2000"))
	path := "/api/devices/discoveries/" + str(pending, "id")
	preview := f.c.ok("GET", path+"/preview", nil)
	observed := f.stamp(t, f.snapshot(time.Now().UTC().Add(time.Second), "3000"))
	observed.EngineEpoch = "changed-boot"
	observed.UptimeTicks = "100"
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", observed); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := f.c.request("POST", path+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": []Record{{"ifIndex": 1, "portId": f.port}}}); w.Code != 409 || !strings.Contains(w.Body.String(), "identity changed") {
		t.Fatal("old table applied after reboot")
	}
}

func TestUnmappedObservationRowsRemainStaffOnly(t *testing.T) {
	f := deviceFixture(t)
	f.approve(t, f.propose(t, f.snapshot(time.Now().UTC(), "1000")))
	f.c.create("users", Record{"name": "Tenant", "email": "unmapped-device@example.net", "role": "member", "memberId": f.member, "password": "unmapped-device-password"})
	member := docLoginClient(t, f.c.a, "unmapped-device@example.net", "unmapped-device-password")
	sample := f.stamp(t, f.snapshot(time.Now().UTC().Add(time.Second), "9999"))
	sample.Interfaces[0].PortID = ""
	sample.Interfaces[0].OwnershipRevision = ""
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", sample); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	response := member.ok("GET", "/api/devices/ports", nil)
	item := Record(response["items"].([]any)[0].(map[string]any))
	if item["status"] != "unknown" || item["observedAt"] != nil || item["speedMbps"] != nil {
		t.Fatal("unmapped full-table row bypassed collection ownership lineage")
	}
	stored, err := f.c.a.latestDevice(f.switchID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := stored.PortOwners[f.port]; exists {
		t.Fatal("unmapped row received an owner stamp")
	}
}

func TestDeviceIfIndexReuseRequiresIdentityReview(t *testing.T) {
	f := deviceFixture(t)
	f.approve(t, f.propose(t, f.snapshot(time.Now().UTC(), "1000")))
	changed := f.stamp(t, f.snapshot(time.Now().UTC().Add(time.Second), "999999"))
	changed.Interfaces[0].Name = "different-physical-interface"
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", changed); w.Code != 422 || !strings.Contains(w.Body.String(), "identity changed") {
		t.Fatal("ifIndex reuse attributed a different physical port to the old member")
	}
	if len(f.c.a.telemetry.Series(f.port, time.Now().Add(-time.Minute), time.Now().Add(time.Minute))) != 0 {
		t.Fatal("unreviewed interface identity reached traffic history")
	}
	proposal := f.propose(t, changed)
	path := "/api/devices/discoveries/" + str(proposal, "id")
	preview := f.c.ok("GET", path+"/preview", nil)
	if w := f.c.request("POST", path+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": []Record{{"ifIndex": 1, "portId": f.port}}}); w.Code != 409 {
		t.Fatal("identity changed without explicit replacement confirmation")
	}
	f.c.ok("POST", path+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": []Record{{"ifIndex": 1, "portId": f.port, "replaceMapping": true}}})
	state := f.c.a.store.Snapshot()
	if str(find(&state, "ports", f.port), "memberId") != f.member || str(find(&state, "ports", f.port), "snmpIfName") != "different-physical-interface" {
		t.Fatal("review did not preserve assignment while anchoring new identity")
	}
}

func TestDeviceWriteOnlyTokenCanAcquireBindingsButNotInventory(t *testing.T) {
	f := deviceFixture(t)
	raw := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "write-only SNMP collector", "currentPassword": "very-long-test-password", "scopes": []string{"devices:write"}, "switchIds": []string{f.switchID}}), "token")
	if w := bearerRequest(f.c.a, raw, "GET", "/api/devices/bindings?switchId="+f.switchID, nil); w.Code != 200 {
		t.Fatalf("write-only collector cannot bind before polling: %d %s", w.Code, w.Body.String())
	}
	if w := bearerRequest(f.c.a, raw, "GET", "/api/devices/status", nil); w.Code != 403 {
		t.Fatal("write-only collector gained inventory read scope")
	}
}

func TestDeviceRetentionResolvesPrunesAndRetainsReviewAudit(t *testing.T) {
	f := deviceFixture(t)
	pending := f.propose(t, f.snapshot(time.Now().UTC(), "1000"))
	id := str(pending, "id")
	path := "/api/devices/discoveries/" + id
	if w := f.c.request("POST", path+"/prune", Record{"reason": "must not remove pending evidence"}); w.Code != 409 {
		t.Fatal("pending discovery automatically pruned")
	}
	f.c.ok("POST", path+"/dismiss", Record{"reason": "not the desired ports"})
	pruned := f.c.ok("POST", path+"/prune", Record{"reason": "reviewed and retained checksum"})
	if pruned["prunedAt"] == nil {
		t.Fatal("prune state missing")
	}
	state := f.c.a.store.Snapshot()
	meta := discoveryByID(&state, id)
	if meta == nil || meta.PrunedAt == nil {
		t.Fatal("prune erased metadata")
	}
	if _, err := os.Stat(filepath.Join(f.c.a.deviceDir(), meta.SnapshotFile)); !os.IsNotExist(err) {
		t.Fatal("raw snapshot still consumes quota")
	}
	if w := f.c.request("GET", path+"/preview", nil); w.Code != 410 {
		t.Fatal("pruned raw snapshot remained previewable")
	}
	if w := f.c.request("DELETE", path, Record{"checksum": pending["checksum"], "reason": "missing explicit acknowledgement"}); w.Code != 409 {
		t.Fatal("history metadata forgotten without acknowledgement")
	}
	f.c.ok("DELETE", path, Record{"checksum": pending["checksum"], "reason": "detailed backup retained separately", "acknowledge": true})
	state = f.c.a.store.Snapshot()
	if discoveryByID(&state, id) != nil {
		t.Fatal("metadata quota not released")
	}
	last := state.Audit[len(state.Audit)-1]
	if last.Action != "forget-device-discovery" || last.Details["checksum"] != pending["checksum"] || last.Details["switchId"] != f.switchID {
		t.Fatal("cleanup lost immutable audit summary")
	}
	applied := f.propose(t, f.snapshot(time.Now().UTC(), "2000"))
	f.approve(t, applied)
	path = "/api/devices/discoveries/" + str(applied, "id")
	f.c.ok("POST", path+"/prune", Record{"reason": "review completed"})
	state = f.c.a.store.Snapshot()
	meta = discoveryByID(&state, str(applied, "id"))
	if len(meta.Changes) != 3 || meta.ReviewBaseChecksum == "" {
		t.Fatal("pruning lost reviewed mapping changes")
	}
	usage := f.c.ok("GET", "/api/devices/usage", nil)
	if number(Record(usage["discovery"].(map[string]any)), "usedBytes") != 0 {
		t.Fatal("pruning failed to reclaim discovery storage")
	}
	if w := bearerRequest(f.c.a, f.token, "GET", "/api/devices/usage", nil); w.Code != 403 {
		t.Fatal("bound machine token read unscoped global quota inventory")
	}
}
func TestDeviceObservationClearIsExplicitAndPreservesTelemetry(t *testing.T) {
	f := deviceFixture(t)
	f.approve(t, f.propose(t, f.snapshot(time.Now().UTC(), "1000")))
	at := time.Now().UTC().Add(time.Second)
	sample := f.stamp(t, f.snapshot(at, "9007199254740993"))
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", sample); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	usage := f.c.ok("GET", "/api/devices/usage", nil)
	items := Record(usage["observations"].(map[string]any))["items"].([]any)
	if len(items) != 1 {
		t.Fatal("observation cache usage missing")
	}
	checksum := str(Record(items[0].(map[string]any)), "checksum")
	if w := f.c.request("POST", "/api/devices/observations/clear", Record{"switchId": f.switchID, "checksum": strings.Repeat("b", 64), "reason": "test stale UI", "acknowledge": true}); w.Code != 409 {
		t.Fatal("stale cache cleanup accepted")
	}
	f.c.ok("POST", "/api/devices/observations/clear", Record{"switchId": f.switchID, "checksum": checksum, "reason": "reset stale diagnostics cache", "acknowledge": true})
	if _, err := os.Stat(filepath.Join(f.c.a.deviceDir(), observationFile(f.switchID))); !os.IsNotExist(err) {
		t.Fatal("cache file not reclaimed")
	}
	if len(f.c.a.telemetry.Series(f.port, at.Add(-time.Second), at.Add(time.Minute))) != 1 {
		t.Fatal("cache cleanup erased traffic history")
	}
}
func TestDeviceDiscoveryQuotaCountsActualStoredBytes(t *testing.T) {
	f := deviceFixture(t)
	filler := filepath.Join(f.c.a.deviceDir(), "discovery-"+strings.Repeat("f", 32)+".json")
	file, err := os.OpenFile(filler, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(deviceDiscoveryQuota); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	before := len(f.c.a.store.Snapshot().DeviceDiscoveries)
	w := bearerRequest(f.c.a, f.token, "POST", "/api/devices/discoveries", f.snapshot(time.Now().UTC(), "1000"))
	if w.Code != 422 || !strings.Contains(w.Body.String(), "quota exceeded") {
		t.Fatal("unbounded discovery file storage accepted")
	}
	if len(f.c.a.store.Snapshot().DeviceDiscoveries) != before {
		t.Fatal("quota failure created metadata")
	}
}
