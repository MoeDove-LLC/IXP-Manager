package as112

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func identityFixture() Identity {
	return Identity{ServerName: "as112.example.test", ResponsibleMailbox: "hostmaster.example.test", Facility: "隔离测试机房", Location: "上海, 中国", UniqueIP: "192.0.2.112", LOC: "45 25 0.000 N 75 42 0.000 W 80.00m 1m 10000m 10m"}
}
func TestAS112FixedServicesZonesAndCanonicalBundle(t *testing.T) {
	b, err := Render("router_fixture", Service{Both, true, true}, identityFixture())
	if err != nil {
		t.Fatal(err)
	}
	if b.ASN != 112 || len(b.ListenAddresses) != 8 || len(b.AdvertisePrefixes) != 4 || len(b.Zones) != 24 || b.HealthPlan.Executed || !b.HealthPlan.RequireLocalAddresses {
		t.Fatal("fixed service contract lost")
	}
	for _, zone := range []string{"home.arpa.", "service.arpa.", "empty.as112.arpa.", "hostname.as112.net.", "hostname.as112.arpa."} {
		found := false
		for _, value := range b.Zones {
			found = found || zone == value
		}
		if !found {
			t.Fatal("legacy supported zone omitted", zone)
		}
	}
	raw, err := Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil || !reflect.DeepEqual(b, decoded) {
		t.Fatal("bundle changed on roundtrip", err)
	}
	for _, file := range b.Files {
		if strings.Contains(file.Content, " IN DNAME ") {
			t.Fatal("empty sink zone invented a DNAME loop")
		}
		if file.Path == "pdns.conf" && (!strings.Contains(file.Content, "resolver=no\n") || strings.Contains(file.Content, "local-address=0.0.0.0") || !strings.Contains(file.Content, "disable-axfr=yes")) {
			t.Fatal("unsafe authoritative listener/proxy policy")
		}
	}
	directory := filepath.Join(t.TempDir(), "bundle")
	if err = WriteNewDirectory(directory, b); err != nil {
		t.Fatal(err)
	}
	for _, file := range b.Files {
		data, e := os.ReadFile(filepath.Join(directory, file.Path))
		if e != nil || string(data) != file.Content {
			t.Fatal("safe extraction changed file bytes")
		}
	}
	if WriteNewDirectory(directory, b) == nil {
		t.Fatal("existing configuration directory overwritten")
	}
	for _, bad := range []string{strings.Replace(raw, "192.175.48.0/24", "192.175.48.0/25", 1), strings.Replace(raw, "resolver=no", "resolver=127.0.0.1", 1), strings.Replace(raw, "zones/zone-00.zone", "../../escape", 1)} {
		if _, err := Decode(bad); err == nil {
			t.Fatal("modified fixed bundle accepted")
		}
	}
}
func TestAS112IdentityAndAddressInputsRejectInjection(t *testing.T) {
	for _, change := range []func(*Identity){func(i *Identity) { i.ServerName = "node\napi=yes" }, func(i *Identity) { i.Facility = "site\n@ IN NS evil." }, func(i *Identity) { i.UniqueIP = "192.175.48.1" }, func(i *Identity) { i.UniqueIP = "127.0.0.1" }, func(i *Identity) { i.LOC = "91 0 0 N 0 0 0 E 0m" }, func(i *Identity) { i.LOC = "45 0 0 N 0 0 0 E 0m\n@ IN A 1.2.3.4" }} {
		i := identityFixture()
		change(&i)
		if _, err := NormalizeIdentity(i); err == nil {
			t.Fatal("unsafe AS112 identity input accepted")
		}
	}
	for _, s := range []Service{{"invalid", true, true}, {Both, false, false}} {
		if s.Validate() == nil {
			t.Fatal("invalid service accepted")
		}
	}
}
