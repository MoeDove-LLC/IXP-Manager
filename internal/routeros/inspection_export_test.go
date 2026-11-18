package routeros

import (
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

func TestNormalizeObservedInventoryWrapper(t *testing.T) {
	p, inv, _, e := inspectFixture(t)
	out, err := NormalizeObservedInventory(inv, p.Target, e)
	if err != nil {
		t.Fatal(err)
	}
	out.Tables[0]["name"] = "changed"
	out.Resources[0]["version"] = "changed"
	out.Capabilities[api.FilterRule] = api.Unknown
	if inv.Tables[0]["name"] != p.Owner || inv.Resources[0]["version"] != p.Target.Version || inv.Capabilities[api.FilterRule] != api.Supported {
		t.Fatal("normalization returned caller aliases")
	}
	e.APIAuthenticated = false
	if _, err := NormalizeObservedInventory(inv, p.Target, e); err == nil {
		t.Fatal("unverified identity")
	}
	e.APIAuthenticated = true
	inv.Capabilities[api.FilterRule] = api.Unknown
	if _, err := NormalizeObservedInventory(inv, p.Target, e); err == nil {
		t.Fatal("incomplete menu")
	}
	inv.Capabilities[api.FilterRule] = api.Supported
	delete(inv.Tables[0], "fib")
	out, err = NormalizeObservedInventory(inv, p.Target, e)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := out.Tables[0]["fib"]; exists {
		t.Fatal("missing getter became false")
	}
	inv.Tables[0]["name"] = string(make([]byte, 16385))
	if _, err := NormalizeObservedInventory(inv, p.Target, e); err == nil {
		t.Fatal("field bound missing")
	}
	if _, err := ManagedConnectionName("r", "bad;command"); err == nil {
		t.Fatal("unsafe peer ID")
	}
}
