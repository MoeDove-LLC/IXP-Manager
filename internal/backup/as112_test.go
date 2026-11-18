package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/as112"
)

func TestAS112BackupPreservesBoundRouterDNSAndEncryptedCredentials(t *testing.T) {
	dir := t.TempDir()
	config := app.Config{DataDir: dir, SetupToken: "as112-backup-setup", NamedCheckzone: os.Getenv("AS112_NAMED_CHECKZONE_BIN"), PDNSServer: os.Getenv("AS112_PDNS_SERVER_BIN")}
	server, err := app.New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	c := &provisionBackupClient{t: t, a: server}
	c.call("POST", "/api/setup", app.Record{"name": "AS112 backup admin", "email": "as112-backup@example.test", "password": provisionBackupPassword, "ixpName": "AS112 backup", "asn": 64500, "setupToken": "as112-backup-setup"})
	location := c.call("POST", "/api/resources/locations", app.Record{"name": "Backup facility"})
	sw := c.call("POST", "/api/resources/switches", app.Record{"name": "Backup switch", "locationId": location["id"]})
	port := c.call("POST", "/api/resources/ports", app.Record{"name": "Backup port", "switchId": sw["id"], "status": "active"})
	member := c.call("POST", "/api/resources/members", app.Record{"name": "Explicit peer", "asn": 64501, "status": "active"})
	vlan := c.call("POST", "/api/resources/vlans", app.Record{"name": "AS112 peering", "tag": 112, "ipv4Prefix": "192.0.2.0/24", "ipv6Prefix": "2001:db8::/64"})
	c.call("POST", "/api/resources/connections", app.Record{"name": "Peer connection", "memberId": member["id"], "portId": port["id"], "vlanId": vlan["id"], "ipv4": "192.0.2.10", "status": "active"})
	router := c.call("POST", "/api/resources/routers", app.Record{"name": "AS112 front router", "platform": "junos", "role": "as112", "asn": 112, "routerId": "192.0.2.112", "localIpv4": "192.0.2.112", "localIpv6": "2001:db8::112", "vlanId": vlan["id"], "as112Service": "both", "as112IPv4": true, "as112IPv6": true, "as112NextHop4": "192.0.2.200", "as112NextHop6": "2001:db8::200"})
	peer := c.call("POST", "/api/resources/peers", app.Record{"name": "Opt-in client", "routerId": router["id"], "memberId": member["id"], "asn": 64501, "address": "192.0.2.10", "enabled": true, "maxPrefixes": 10, "allowedPrefixes": []string{}, "allowedRoutes": []any{}})
	credentialPath := "/api/peer-credentials/" + peer["id"].(string)
	credential := c.call("GET", credentialPath, nil)
	secret := "AS112-backup-private-MD5"
	c.call("PUT", credentialPath, app.Record{"expectedRevision": credential["revision"], "expectedPeerChecksum": credential["peerChecksum"], "currentPassword": provisionBackupPassword, "secret": secret})
	bgp := c.call("POST", "/api/deployments", app.Record{"routerId": router["id"]})
	bgpPath := "/api/deployments/" + bgp["id"].(string)
	c.call("POST", bgpPath+"/validate", nil)
	c.call("POST", bgpPath+"/approve", app.Record{"checksum": bgp["checksum"]})
	bgpContent := c.request("GET", bgpPath+"/config", nil)
	if bgpContent.Code != 200 || !strings.Contains(bgpContent.Body.String(), secret) {
		t.Fatal("Junos structural reviewed fixture is incomplete")
	}
	profile := c.call("POST", "/api/resources/provisioning-profiles", app.Record{"name": "AS112 authoritative service", "options": app.Record{"kind": "as112-dns", "routerId": router["id"], "as112": app.Record{"serverName": "as112.example.test", "responsibleMailbox": "hostmaster.example.test", "facility": "Backup AS112 facility", "location": "Offline fixture", "uniqueIP": "192.0.2.200", "loc": ""}}})
	dns := c.call("POST", "/api/provisioning", app.Record{"profileId": profile["id"]})
	dnsPath := "/api/provisioning/" + dns["id"].(string)
	dnsContent := c.call("GET", dnsPath, nil)["config"].(string)
	bundle, err := as112.Decode(dnsContent)
	if err != nil || bundle.RouterCreatedAt != router["createdAt"] || bundle.Service != as112.Both {
		t.Fatal("DNS source binding incomplete", err)
	}
	native := config.NamedCheckzone != "" && config.PDNSServer != ""
	if native {
		c.call("POST", dnsPath+"/validate", nil)
		c.call("POST", dnsPath+"/approve", app.Record{"checksum": dns["checksum"]})
		download := c.request("GET", dnsPath+"/config", nil)
		if download.Code != 200 || download.Body.String() != dnsContent || download.Header().Get("X-Provisioning-Router-ID") != router["id"] {
			t.Fatal("native-approved DNS download changed")
		}
	}
	t.Logf("actual_powerdns_and_zone_native_validation=%v; junos_validation=structural_only; no_service_started=true", native)
	server.Close()
	before := noteBackupState(t, dir)
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte("local-address-nonexist-fail=yes")) {
		t.Fatal("plaintext BGP/DNS artifact persisted outside encryption")
	}
	archive := filepath.Join(t.TempDir(), "as112.tar.gz")
	if err = Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err = Restore(archive, restoredDir); err != nil {
		t.Fatal(err)
	}
	after := noteBackupState(t, restoredDir)
	for name, pair := range map[string][2]any{"routers": {before.Resources["routers"], after.Resources["routers"]}, "profiles": {before.Resources["provisioning-profiles"], after.Resources["provisioning-profiles"]}, "BGP deployments": {before.Deployments, after.Deployments}, "DNS revisions": {before.ProvisionRevisions, after.ProvisionRevisions}, "credentials": {before.PeerCredentials, after.PeerCredentials}} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Fatal("backup changed " + name)
		}
	}
	for _, entry := range []struct{ folder, id string }{{"deployments", bgp["id"].(string)}, {"provisioning", dns["id"].(string)}} {
		a, e1 := os.ReadFile(filepath.Join(dir, entry.folder, entry.id+".enc"))
		b, e2 := os.ReadFile(filepath.Join(restoredDir, entry.folder, entry.id+".enc"))
		if e1 != nil || e2 != nil || !bytes.Equal(a, b) {
			t.Fatal("backup changed immutable encrypted artifact")
		}
	}
	config.DataDir = restoredDir
	restored, err := app.New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	admin := &provisionBackupClient{t: t, a: restored}
	admin.call("POST", "/api/login", app.Record{"email": "as112-backup@example.test", "password": provisionBackupPassword})
	if w := admin.request("GET", bgpPath+"/config", nil); w.Code != 200 || w.Body.String() != bgpContent.Body.String() {
		t.Fatal("restored BGP artifact/credentials changed")
	}
	if got := admin.call("GET", dnsPath, nil)["config"]; got != dnsContent {
		t.Fatal("restored DNS bundle changed")
	}
	if native {
		if w := admin.request("GET", dnsPath+"/config", nil); w.Code != 200 || w.Body.String() != dnsContent {
			t.Fatal("restored native-approved DNS artifact unavailable")
		}
	}
	edit := app.Record{}
	for key, value := range router {
		if key != "id" && key != "createdAt" && key != "updatedAt" && key != "_etag" {
			edit[key] = value
		}
	}
	edit["as112Service"] = "dname"
	admin.call("PUT", "/api/resources/routers/"+router["id"].(string), edit)
	if w := admin.request("GET", bgpPath+"/config", nil); w.Code != 409 {
		t.Fatal("old AS112 BGP approval survived service change")
	}
	action := dnsPath + "/validate"
	method := "POST"
	if native {
		action, method = dnsPath+"/config", "GET"
	}
	if w := admin.request(method, action, nil); w.Code != 409 {
		t.Fatal("old DNS revision survived bound-router service change", w.Code)
	}
}
