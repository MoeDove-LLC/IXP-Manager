package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/moedove/ixp-manager/internal/core"
)

type coreAPIFixture struct {
	c                                      *client
	swA, swB, portA, portB, spareA, spareB string
}

func newCoreAPIFixture(t *testing.T) coreAPIFixture {
	c, _ := setupClient(t)
	loc := c.create("locations", Record{"name": "core DC"})
	a := c.create("switches", Record{"name": "core switch A", "locationId": loc["id"], "asn": 64500, "loopbackIpv4": "10.0.0.1", "loopbackName": "lo0"})
	b := c.create("switches", Record{"name": "core switch B", "locationId": loc["id"], "asn": 64501, "loopbackIpv4": "10.0.0.2", "loopbackName": "lo0"})
	port := func(sw Record, name string) string {
		return str(c.create("ports", Record{"name": name, "switchId": sw["id"], "speed": 10000}), "id")
	}
	return coreAPIFixture{c, str(a, "id"), str(b, "id"), port(a, "A1"), port(b, "B1"), port(a, "A2"), port(b, "B2")}
}
func (f coreAPIFixture) bundle() core.Bundle {
	return core.Bundle{Name: "核心互联", Type: core.ECMP, Enabled: true, SideA: core.Side{SwitchID: f.swA}, SideB: core.Side{SwitchID: f.swB}, Links: []core.Link{{Enabled: true, BFD: true, IPv4Subnet: "192.0.2.0/31", IPv6Subnet: "2001:db8::/127", SideA: core.Endpoint{PortID: f.portA, Duplex: "full", Autoneg: true}, SideB: core.Endpoint{PortID: f.portB, Duplex: "full", Autoneg: true}}}}
}
func coreBundleRecord(t *testing.T, r Record) core.Bundle {
	t.Helper()
	raw, _ := json.Marshal(r)
	var b core.Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b
}
func (f coreAPIFixture) tags(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, raw := range f.c.ok("GET", "/api/core/inventory", nil)["ports"].([]any) {
		r := Record(raw.(map[string]any))
		out[str(r, "id")] = str(r, "_etag")
	}
	return out
}
func (f coreAPIFixture) preview(t *testing.T, b core.Bundle, adoptions []coreAdoption) core.Bundle {
	t.Helper()
	r := f.c.ok("POST", "/api/core/preview", coreMutation{Bundle: b, AdoptLinks: adoptions})
	return coreBundleRecord(t, Record(r["bundle"].(map[string]any)))
}
func (f coreAPIFixture) create(t *testing.T) Record {
	t.Helper()
	b := f.preview(t, f.bundle(), nil)
	return f.c.ok("POST", "/api/core/bundles", coreMutation{Bundle: b, PortETags: f.tags(t)})
}

func TestCoreAPIAtomicCASAndDeletionPreservesPhysicalPorts(t *testing.T) {
	f := newCoreAPIFixture(t)
	b := f.preview(t, f.bundle(), nil)
	before := f.c.a.store.Snapshot()
	if len(before.CoreBundles) != 0 {
		t.Fatal("preview changed inventory")
	}
	if find(&before, "ports", f.portA)["coreReserved"] == true {
		t.Fatal("preview reserved a real port")
	}
	tags := f.tags(t)
	oldA := str(find(&before, "ports", f.portA), "ownershipSince")
	if w := f.c.request("POST", "/api/core/bundles", coreMutation{Bundle: b}); w.Code != 428 {
		t.Fatal("missing review accepted", w.Code, w.Body.String())
	}
	f.c.ok("PUT", "/api/resources/switches/"+f.swA, Record{"name": "switch A reviewed change", "locationId": str(find(&before, "switches", f.swA), "locationId"), "asn": 64510, "loopbackIpv4": "10.0.0.1", "loopbackName": "lo0"})
	if w := f.c.request("POST", "/api/core/bundles", coreMutation{Bundle: b, PortETags: tags}); w.Code != 409 {
		t.Fatal("switch dependency change escaped CAS", w.Code, w.Body.String())
	}
	created := f.c.ok("POST", "/api/core/bundles", coreMutation{Bundle: b, PortETags: f.tags(t)})
	b = coreBundleRecord(t, created)
	d := Record(created["derived"].(map[string]any))
	if d["capacityMbps"] != float64(10000) || d["bgpReady"] != true {
		t.Fatalf("bad desired projection: %+v", d)
	}
	after := f.c.a.store.Snapshot()
	if len(after.CoreBundles) != 1 || find(&after, "ports", f.portA)["coreReserved"] != true || str(find(&after, "ports", f.portA), "ownershipSince") == oldA {
		t.Fatal("core ownership not stamped")
	}
	corrupt := cloneState(after)
	delete(find(&corrupt, "ports", f.portA), "coreReserved")
	if err := validateCore(&corrupt); err == nil {
		t.Fatal("stored bundle accepted a missing core-reserved flag")
	}
	if w := f.c.request("DELETE", "/api/resources/ports/"+f.portA, nil); w.Code < 400 {
		t.Fatal("generic deletion orphaned core endpoint")
	}
	revision := str(created, "revision")
	b.Name = "new reviewed name"
	next := f.c.ok("PUT", "/api/core/bundles/"+b.ID, coreMutation{Bundle: b, ExpectedRevision: revision, PortETags: f.tags(t)})
	if str(next, "bindingRevision") == str(created, "bindingRevision") {
		t.Fatal("topology revision did not rotate")
	}
	if w := f.c.request("PUT", "/api/core/bundles/"+b.ID, coreMutation{Bundle: b, ExpectedRevision: revision, PortETags: f.tags(t)}); w.Code != 409 {
		t.Fatal("stale save accepted", w.Code, w.Body.String())
	}
	bad := coreBundleRecord(t, next)
	bad.Links = nil
	if w := f.c.request("PUT", "/api/core/bundles/"+b.ID, coreMutation{Bundle: bad, ExpectedRevision: str(next, "revision"), PortETags: f.tags(t)}); w.Code != 422 {
		t.Fatal("last link deletion accepted", w.Code)
	}
	f.c.ok("DELETE", "/api/core/bundles/"+b.ID, Record{"expectedRevision": next["revision"], "reason": "planned decommission"})
	state := f.c.a.store.Snapshot()
	if len(state.CoreBundles) != 0 || find(&state, "ports", f.portA) == nil || find(&state, "ports", f.portB) == nil {
		t.Fatal("delete removed physical inventory")
	}
	if find(&state, "ports", f.portA)["coreReserved"] != true {
		t.Fatal("delete silently freed reserved core port")
	}
}

func TestCoreAPICollisionAdoptionAndMetadataTampering(t *testing.T) {
	f := newCoreAPIFixture(t)
	flat := f.c.create("core-links", Record{"name": "old reviewed link", "type": "layer3", "portAId": f.portA, "portBId": f.portB, "notes": "preserve in adoption audit"})
	if w := f.c.request("POST", "/api/core/preview", coreMutation{Bundle: f.bundle()}); w.Code != 422 {
		t.Fatal("implicit adoption accepted", w.Code, w.Body.String())
	}
	adoptions := []coreAdoption{{ID: str(flat, "id"), ETag: str(flat, "_etag")}}
	b := f.preview(t, f.bundle(), adoptions)
	bad := b
	bad.Links = append([]core.Link(nil), b.Links...)
	bad.Links[0].SideB.PortID = f.spareB
	if w := f.c.request("POST", "/api/core/bundles", coreMutation{Bundle: bad, PortETags: f.tags(t), AdoptLinks: adoptions}); w.Code != 422 {
		t.Fatal("adoption silently discarded old endpoint", w.Code, w.Body.String())
	}
	state := f.c.a.store.Snapshot()
	if len(state.CoreBundles) != 0 || len(state.Resources["core-links"]) != 1 {
		t.Fatal("failed adoption partially committed")
	}
	b.BindingRevision = strings.Repeat("a", 22)
	if w := f.c.request("POST", "/api/core/bundles", coreMutation{Bundle: b, PortETags: f.tags(t), AdoptLinks: adoptions}); w.Code != 422 {
		t.Fatal("client forged binding metadata", w.Code)
	}
	b.BindingRevision = ""
	created := f.c.ok("POST", "/api/core/bundles", coreMutation{Bundle: b, PortETags: f.tags(t), AdoptLinks: adoptions})
	state = f.c.a.store.Snapshot()
	if len(state.Resources["core-links"]) != 0 || len(state.CoreBundles) != 1 {
		t.Fatal("adoption incomplete")
	}
	last := state.Audit[len(state.Audit)-1]
	raw, _ := json.Marshal(last.Details)
	if !strings.Contains(string(raw), "preserve in adoption audit") {
		t.Fatal("old link metadata was lost")
	}
	member := f.c.create("members", Record{"name": "member", "asn": 64512})
	vlan := f.c.create("vlans", Record{"name": "peering", "tag": 100, "ipv4Prefix": "198.51.100.0/24"})
	if w := f.c.request("POST", "/api/resources/connections", Record{"name": "wrong reuse", "memberId": member["id"], "vlanId": vlan["id"], "portId": f.portA}); w.Code < 400 {
		t.Fatal("member connection occupied core endpoint")
	}
	duplicate := f.bundle()
	if w := f.c.request("POST", "/api/core/bundles", coreMutation{Bundle: duplicate, PortETags: f.tags(t)}); w.Code != 422 {
		t.Fatal("core port reused", w.Code, w.Body.String())
	}
	if len(f.c.a.store.Snapshot().CoreBundles) != 1 || str(created, "id") == "" {
		t.Fatal("failed writes changed topology")
	}
}

func TestCoreAPIConcurrentCreateOneWinnerAndScopedRead(t *testing.T) {
	f := newCoreAPIFixture(t)
	b1 := f.preview(t, f.bundle(), nil)
	b2 := f.preview(t, f.bundle(), nil)
	tags := f.tags(t)
	results := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for _, b := range []core.Bundle{b1, b2} {
		wg.Add(1)
		go func(b core.Bundle) {
			defer wg.Done()
			results <- f.c.request("POST", "/api/core/bundles", coreMutation{Bundle: b, PortETags: tags})
		}(b)
	}
	wg.Wait()
	close(results)
	codes := map[int]int{}
	for r := range results {
		codes[r.Code]++
	}
	if codes[201] != 1 || codes[409] != 1 {
		t.Fatalf("concurrent create results: %+v", codes)
	}
	state := f.c.a.store.Snapshot()
	id := state.CoreBundles[0].ID
	tokenFor := func(ids []string) string {
		return str(f.c.ok("POST", "/api/account/tokens", Record{"name": "core reader", "currentPassword": "very-long-test-password", "scopes": []string{"core:read"}, "switchIds": ids}), "token")
	}
	one := tokenFor([]string{f.swA})
	both := tokenFor([]string{f.swA, f.swB})
	if w := bearerRequest(f.c.a, one, "GET", "/api/core/bundles/"+id, nil); w.Code != 404 {
		t.Fatal("single-side token saw opposite switch", w.Code, w.Body.String())
	}
	list := responseRecord(t, bearerRequest(f.c.a, one, "GET", "/api/core", nil))
	if len(list["items"].([]any)) != 0 {
		t.Fatal("partial-scope bundle leaked")
	}
	if w := bearerRequest(f.c.a, both, "GET", "/api/core/bundles/"+id, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := bearerRequest(f.c.a, both, "DELETE", "/api/core/bundles/"+id, Record{}); w.Code != 403 {
		t.Fatal("API token mutated topology", w.Code)
	}
	member := f.c.create("members", Record{"name": "member", "asn": 64555})
	f.c.create("users", Record{"name": "member account", "email": "core-member@example.net", "role": "member", "memberId": member["id"], "password": "core-member-password"})
	m := docLoginClient(t, f.c.a, "core-member@example.net", "core-member-password")
	if w := m.request("GET", "/api/core", nil); w.Code != 403 {
		t.Fatal("member saw core infrastructure", w.Code)
	}
}

func TestCoreValidationFabricSubnetAndUnknownCapacity(t *testing.T) {
	f := newCoreAPIFixture(t)
	a := f.c.create("infrastructures", Record{"name": "fabric A"})
	z := f.c.create("infrastructures", Record{"name": "fabric B"})
	f.c.ok("POST", "/api/fabrics/assign", Record{"infrastructureId": a["id"], "switchIds": []string{f.swA}})
	f.c.ok("POST", "/api/fabrics/assign", Record{"infrastructureId": z["id"], "switchIds": []string{f.swB}})
	b := f.bundle()
	group := uint32(0)
	b.Type = core.L2LAG
	b.SideA.ChannelGroup = &group
	b.SideB.ChannelGroup = &group
	b.SideA.InterfaceName = "lag0"
	b.SideB.InterfaceName = "lag0"
	b.Links[0].BFD = false
	b.Links[0].IPv4Subnet = ""
	b.Links[0].IPv6Subnet = ""
	if w := f.c.request("POST", "/api/core/preview", coreMutation{Bundle: b}); w.Code != 422 {
		t.Fatal("L2 crossed fabric", w.Code, w.Body.String())
	}
	created := f.create(t)
	next := f.bundle()
	next.Links[0].SideA.PortID = f.spareA
	next.Links[0].SideB.PortID = f.spareB
	if w := f.c.request("POST", "/api/core/preview", coreMutation{Bundle: next}); w.Code != 422 {
		t.Fatal("overlapping L3 subnets accepted", w.Code, w.Body.String())
	}
	f.c.ok("PUT", "/api/resources/ports/"+f.portA, Record{"name": "unknown-speed port", "switchId": f.swA, "coreReserved": true})
	view := f.c.ok("GET", "/api/core/bundles/"+str(created, "id"), nil)
	derived := view["derived"].(map[string]any)
	if derived["capacityKnown"] != false || derived["capacityMbps"] != nil {
		t.Fatalf("unknown speed fabricated capacity: %+v", derived)
	}
	if str(view, "revision") == str(created, "revision") {
		t.Fatal("port-dependent review revision did not change")
	}
	if w := f.c.request("DELETE", "/api/core/bundles/"+str(created, "id"), Record{"expectedRevision": created["revision"], "reason": "stale"}); w.Code != 409 {
		t.Fatal("delete ignored dependency CAS", fmt.Sprint(w.Code), w.Body.String())
	}
}
