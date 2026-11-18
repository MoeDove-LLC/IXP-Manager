package app

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/core"
	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
)

func coreTestSnapshot(f coreAPIFixture, side string, at time.Time, count int) devicesnmp.Snapshot {
	id, ports, names, engine := f.swA, []string{f.portA, f.spareA}, []string{"A1", "A2"}, "a"
	if side == "b" {
		id, ports, names, engine = f.swB, []string{f.portB, f.spareB}, []string{"B1", "B2"}, "b"
	}
	s := devicesnmp.Snapshot{Version: 1, SwitchID: id, ObservedAt: at, Security: "authPriv", EngineIdentity: strings.Repeat(engine, 64), Identity: devicesnmp.Identity{Name: "core-switch-" + side}, UptimeTicks: fmt.Sprint(100000 + count*1000), EngineEpoch: "core-test-boot", Interfaces: []devicesnmp.Interface{}, Entities: []devicesnmp.Entity{}, Sensors: []devicesnmp.Sensor{}, FDB: []devicesnmp.FDBEntry{}, Warnings: []string{}}
	for i, p := range ports {
		s.Interfaces = append(s.Interfaces, devicesnmp.Interface{IfIndex: uint32(i + 1), PortID: p, Name: names[i], Type: 6, MTU: 9000, SpeedMbps: 10000, AdminStatus: "up", OperStatus: "up", InOctets: fmt.Sprint(1000 + count*1250), OutOctets: fmt.Sprint(2000 + count*2500), CounterBits: 64, DiscontinuityTicks: "0"})
	}
	return s
}
func coreStampSnapshot(t *testing.T, c *client, raw string, snapshot devicesnmp.Snapshot) devicesnmp.Snapshot {
	t.Helper()
	out := responseRecord(t, bearerRequest(c.a, raw, "GET", "/api/devices/bindings?switchId="+snapshot.SwitchID, nil))
	revs := map[string]string{}
	for _, raw := range out["mappings"].([]any) {
		m := Record(raw.(map[string]any))
		revs[str(m, "portId")] = str(m, "ownershipRevision")
	}
	for i := range snapshot.Interfaces {
		snapshot.Interfaces[i].OwnershipRevision = revs[snapshot.Interfaces[i].PortID]
	}
	return snapshot
}
func TestCoreObservationsUseBoundSNMPAndKeepTrafficSidesSeparate(t *testing.T) {
	f := newCoreAPIFixture(t)
	b := f.bundle()
	// An endpoint's requested configuration cannot inflate physical inventory
	// capacity; its conflicting desired speed remains visible in the bundle.
	desiredSpeed := uint64(40000)
	b.Links[0].SideA.SpeedMbps = &desiredSpeed
	b.Links = append(b.Links, core.Link{Enabled: true, BFD: true, IPv4Subnet: "192.0.2.2/31", IPv6Subnet: "2001:db8::2/127", SideA: core.Endpoint{PortID: f.spareA, Duplex: "full"}, SideB: core.Endpoint{PortID: f.spareB, Duplex: "full"}})
	b = f.preview(t, b, nil)
	created := f.c.ok("POST", "/api/core/bundles", coreMutation{Bundle: b, PortETags: f.tags(t)})
	b = coreBundleRecord(t, created)
	path := "/api/core/bundles/" + b.ID
	initial := f.c.ok("GET", path+"/observations", nil)
	if initial["physical"].(map[string]any)["status"] != "unknown" || initial["bgp"].(map[string]any)["status"] != "unsupported" {
		t.Fatal("desired topology fabricated protocol health")
	}
	raw := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "Core SNMP", "currentPassword": "very-long-test-password", "scopes": []string{"devices:write", "core:read"}, "switchIds": []string{f.swA, f.swB}}), "token")
	for _, side := range []string{"a", "b"} {
		snapshot := coreTestSnapshot(f, side, time.Now().UTC(), 0)
		proposal := responseRecord(t, bearerRequest(f.c.a, raw, "POST", "/api/devices/discoveries", snapshot))
		base := "/api/devices/discoveries/" + str(proposal, "id")
		preview := f.c.ok("GET", base+"/preview", nil)
		selections := []Record{}
		for _, p := range snapshot.Interfaces {
			selections = append(selections, Record{"ifIndex": p.IfIndex, "portId": p.PortID})
		}
		f.c.ok("POST", base+"/apply", Record{"baseChecksum": preview["baseChecksum"], "ports": selections})
	}
	start := time.Now().UTC().Add(time.Second)
	for count := 0; count < 2; count++ {
		for _, side := range []string{"a", "b"} {
			snapshot := coreStampSnapshot(t, f.c, raw, coreTestSnapshot(f, side, start.Add(time.Duration(count)*10*time.Second), count))
			if count == 1 && side == "b" {
				snapshot.Interfaces[1].CounterBits = 0
				snapshot.Interfaces[1].InOctets = ""
				snapshot.Interfaces[1].OutOctets = ""
			}
			if w := bearerRequest(f.c.a, raw, "POST", "/api/devices/observations", snapshot); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
	observed := f.c.ok("GET", path+"/observations", nil)
	if observed["physical"].(map[string]any)["status"] != "up" || observed["bfd"].(map[string]any)["status"] != "unsupported" {
		t.Fatal("physical/protocol observations mixed")
	}
	traffic := observed["traffic"].(map[string]any)
	left, right := traffic["sideA"].(map[string]any), traffic["sideB"].(map[string]any)
	if left["inBps"] != float64(2000) || left["outBps"] != float64(4000) || right["inBps"] != nil || right["coveredPorts"] != float64(1) {
		t.Fatalf("invented or double-counted core traffic: %#v", traffic)
	}
	capacity := observed["capacity"].(map[string]any)
	if capacity["sideA"].(map[string]any)["totalMbps"] != float64(20000) || capacity["sideB"].(map[string]any)["totalMbps"] != float64(20000) {
		t.Fatal("directional capacity changed")
	}
	series := f.c.ok("GET", path+"/series?side=a&to="+start.Add(25*time.Second).Format(time.RFC3339), nil)
	if len(series["points"].([]any)) == 0 {
		t.Fatal("current core history absent")
	}
	state := f.c.a.store.Snapshot()
	old := *coreBundleByID(&state, b.ID)
	stale := f.c.a.coreObservations(&state, old, start.Add(3*time.Minute))
	if stale["physical"].(coreLayer).Status != "stale" {
		t.Fatal("stale sample shown Up")
	}
	boundA := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "A only", "currentPassword": "very-long-test-password", "scopes": []string{"core:read"}, "switchIds": []string{f.swA}}), "token")
	if w := bearerRequest(f.c.a, boundA, "GET", path+"/observations", nil); w.Code != 404 {
		t.Fatal("one-switch token read the far endpoint")
	}
	if w := bearerRequest(f.c.a, raw, "GET", path+"/observations", nil); w.Code != 200 {
		t.Fatal("both-side token rejected", w.Body.String())
	}
	// Round-trip topology edits must not revive samples from the old epoch.
	for _, name := range []string{"changed binding", b.Name} {
		current := f.c.ok("GET", path, nil)
		edit := coreBundleRecord(t, current)
		edit.Name = name
		f.c.ok("PUT", path, coreMutation{Bundle: edit, ExpectedRevision: str(current, "revision"), PortETags: f.tags(t)})
	}
	if view := f.c.ok("GET", path+"/observations", nil); view["physical"].(map[string]any)["status"] != "unknown" {
		t.Fatal("old core samples survived A-B-A")
	}
	if history := f.c.ok("GET", path+"/series?side=a", nil); len(history["points"].([]any)) != 0 {
		t.Fatal("old topology traffic entered new bundle history")
	}
	current := f.c.ok("GET", path, nil)
	edit := coreBundleRecord(t, current)
	for i := range edit.Links {
		edit.Links[i].Enabled = false
	}
	f.c.ok("PUT", path, coreMutation{Bundle: edit, ExpectedRevision: str(current, "revision"), PortETags: f.tags(t)})
	if view := f.c.ok("GET", path+"/observations", nil); view["physical"].(map[string]any)["status"] != "unknown" {
		t.Fatal("zero enabled links reported healthy")
	}
}

func TestCoreImportAndPortReleasePreserveOwnershipBoundaries(t *testing.T) {
	f := newCoreAPIFixture(t)
	created := f.create(t)
	b := coreBundleRecord(t, created)
	f.c.ok("PUT", "/api/resources/ports/"+f.portA, Record{"name": "safe renamed port", "switchId": f.swA, "speed": 10000})
	if view := f.c.ok("GET", "/api/resources/ports/"+f.portA, nil); view["coreReserved"] != true {
		t.Fatal("omitted flag released a core port")
	}
	if w := f.c.request("PUT", "/api/resources/ports/"+f.portA, Record{"name": "bad release", "switchId": f.swA, "coreReserved": false}); w.Code < 400 {
		t.Fatal("referenced core port released")
	}
	for _, invalid := range []any{nil, ""} {
		if w := f.c.request("PUT", "/api/resources/ports/"+f.portA, Record{"name": "invalid release", "switchId": f.swA, "coreReserved": invalid}); w.Code < 400 {
			t.Fatal("non-boolean core reservation bypassed explicit release")
		}
	}
	old := f.c.a.store.Snapshot()
	oldBinding := deviceBindingRevision(&old, find(&old, "ports", f.portA))
	exported := f.c.ok("GET", "/api/export", nil)
	raw, _ := json.Marshal(exported)
	var body struct {
		CoreBundles []core.Bundle `json:"coreBundles"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.CoreBundles) != 1 || body.CoreBundles[0].BindingRevision != "" {
		t.Fatal("resource export leaked sample authority or lost topology")
	}
	current := f.c.ok("GET", "/api/core/bundles/"+b.ID, nil)
	f.c.ok("DELETE", "/api/core/bundles/"+b.ID, Record{"expectedRevision": current["revision"], "reason": "test restore"})
	body.CoreBundles[0].BindingRevision = b.BindingRevision
	f.c.ok("POST", "/api/import", Record{"version": 1, "resources": Record{}, "coreBundles": body.CoreBundles})
	state := f.c.a.store.Snapshot()
	restored := coreBundleByID(&state, b.ID)
	if restored == nil || restored.BindingRevision == b.BindingRevision || deviceBindingRevision(&state, find(&state, "ports", f.portA)) == oldBinding {
		t.Fatal("resource import restored old sample lineage")
	}
	if w := f.c.request("POST", "/api/import", Record{"version": 1, "resources": Record{}, "coreBundles": body.CoreBundles}); w.Code < 400 {
		t.Fatal("duplicate core import was not atomic")
	}
}

func TestCoreLegacyReservationsMigrateOnceAndSurviveRestart(t *testing.T) {
	f := newCoreAPIFixture(t)
	f.c.create("core-links", Record{"name": "Old unbundled cable", "portAId": f.portA, "portBId": f.portB, "type": "layer3"})
	if err := f.c.a.store.Update(func(s *State) error {
		for _, id := range []string{f.portA, f.portB} {
			delete(find(s, "ports", id), "coreReserved")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(f.c.a.store.path)
	f.c.a.Close()
	first, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	one := first.Snapshot()
	first.Close()
	second, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	two := second.Snapshot()
	for _, id := range []string{f.portA, f.portB} {
		a, z := find(&one, "ports", id), find(&two, "ports", id)
		if a["coreReserved"] != true || z["coreReserved"] != true || str(a, "ownershipSince") != str(z, "ownershipSince") {
			t.Fatal("legacy core reservation migration did not persist its lineage")
		}
	}
	count := 0
	for _, entry := range two.Audit {
		if entry.Action == "core-reservation-migrate" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("legacy migration repeated or lacked audit", count)
	}
}
