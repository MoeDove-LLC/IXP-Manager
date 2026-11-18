package backup

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/core"
)

func TestCoreBackupPreservesTopologyLineageAndReviewedPlan(t *testing.T) {
	f := makeProvisionBackupFixture(t, false)
	a, err := app.New(app.Config{DataDir: f.dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/login", app.Record{"email": "provision-backup@example.net", "password": provisionBackupPassword})
	loc := c.call("POST", "/api/resources/locations", app.Record{"name": "Core backup DC"})
	sa := c.call("POST", "/api/resources/switches", app.Record{"name": "Core A", "locationId": loc["id"], "asn": 64510, "loopbackIpv4": "10.0.0.1", "loopbackName": "lo0"})
	sb := c.call("POST", "/api/resources/switches", app.Record{"name": "Core B", "locationId": loc["id"], "asn": 64511, "loopbackIpv4": "10.0.0.2", "loopbackName": "lo0"})
	pa := c.call("POST", "/api/resources/ports", app.Record{"name": "Ethernet1", "switchId": sa["id"], "speed": 10000})
	pb := c.call("POST", "/api/resources/ports", app.Record{"name": "Ethernet2", "switchId": sb["id"], "speed": 10000})
	b := core.Bundle{Name: "Core restore", Type: core.ECMP, Enabled: true, SideA: core.Side{SwitchID: sa["id"].(string), MTU: 9000}, SideB: core.Side{SwitchID: sb["id"].(string), MTU: 9000}, Links: []core.Link{{Enabled: true, BFD: true, IPv4Subnet: "198.18.0.0/31", IPv6Subnet: "2001:db8:abcd::/127", SideA: core.Endpoint{PortID: pa["id"].(string), Duplex: "full"}, SideB: core.Endpoint{PortID: pb["id"].(string), Duplex: "full"}}}}
	preview := c.call("POST", "/api/core/preview", app.Record{"bundle": b})
	tags := map[string]string{}
	for _, raw := range c.call("GET", "/api/core/inventory", nil)["ports"].([]any) {
		p := raw.(map[string]any)
		tags[p["id"].(string)] = p["_etag"].(string)
	}
	created := c.call("POST", "/api/core/bundles", app.Record{"bundle": preview["bundle"], "portEtags": tags})
	profile := c.call("POST", "/api/resources/provisioning-profiles", app.Record{"name": "Core backup plan", "options": app.Record{"kind": "switch-core", "switchId": sa["id"]}})
	draft := c.call("POST", "/api/provisioning", app.Record{"profileId": profile["id"]})
	path := "/api/provisioning/" + draft["id"].(string)
	c.call("POST", path+"/validate", nil)
	c.call("POST", path+"/approve", app.Record{"checksum": draft["checksum"]})
	before := c.call("GET", path, nil)
	beforeConfig := before["config"]
	a.Close()
	archive := filepath.Join(t.TempDir(), "core.gz")
	if err := Create(f.dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	a, err = app.New(app.Config{DataDir: target})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c = &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/login", app.Record{"email": "provision-backup@example.net", "password": provisionBackupPassword})
	after := c.call("GET", "/api/core/bundles/"+created["id"].(string), nil)
	if after["bindingRevision"] != created["bindingRevision"] || after["revision"] != created["revision"] {
		t.Fatal("complete backup changed topology or review identity")
	}
	x, _ := json.Marshal(created["links"])
	y, _ := json.Marshal(after["links"])
	if !bytes.Equal(x, y) {
		t.Fatal("stable core link/endpoint IDs changed")
	}
	for _, port := range []app.Record{pa, pb} {
		if c.call("GET", "/api/resources/ports/"+port["id"].(string), nil)["coreReserved"] != true {
			t.Fatal("restore released core port reservation")
		}
	}
	restored := c.call("GET", path, nil)
	if restored["config"] != beforeConfig || restored["checksum"] != draft["checksum"] || restored["status"] != "approved" {
		t.Fatal("approved encrypted core plan lost")
	}
}
