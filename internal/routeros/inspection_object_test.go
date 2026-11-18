package routeros

import (
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"testing"
)

func TestNormalizeObservedObjectIsIndependentAndPreservesUnknown(t *testing.T) {
	p, _, _, _ := inspectFixture(t)
	row := api.Record{".id": "*a", "chain": "owned_root", "comment": "owned:in", "rule": "reject;", "inactive": "false"}
	got, e := NormalizeObservedObject(api.FilterRule, row, p.Target, nil)
	if e != nil || got[".id"] != "*A" {
		t.Fatal(e)
	}
	if _, known := got["disabled"]; known {
		t.Fatal("missing property became false")
	}
	got["rule"] = "changed"
	if row["rule"] != "reject;" {
		t.Fatal("caller record aliased")
	}
	proof := []VersionPropertyEvidence{{Resource: api.FilterRule, ID: "*A", Field: "disabled", Completed: true, Property: api.PropertyValue{Known: true, Value: "false"}}}
	got, e = NormalizeObservedObject(api.FilterRule, row, p.Target, proof)
	if e != nil || got["disabled"] != "false" {
		t.Fatal("explicit getter evidence lost", e)
	}
	proof[0].Property = api.PropertyValue{}
	if _, e = NormalizeObservedObject(api.FilterRule, row, p.Target, proof); e == nil {
		t.Fatal("unknown flag became false")
	}
}
func TestNormalizeObservedObjectRejectsForeignOrSensitiveEvidence(t *testing.T) {
	p, _, _, _ := inspectFixture(t)
	row := api.Record{".id": "*1", "chain": "owned_root", "comment": "owned:in", "rule": "reject;", "disabled": "false", "inactive": "false"}
	for _, proof := range []VersionPropertyEvidence{
		{Resource: api.FilterRule, ID: "*2", Field: "disabled", Completed: true, Property: api.PropertyValue{Known: true, Value: "false"}},
		{Resource: api.FilterRule, ID: "*1", Field: "disabled", Completed: false, Property: api.PropertyValue{Known: true, Value: "false"}},
		{Resource: api.FilterRule, ID: "*1", Field: "tcp-md5-key", Completed: true, Property: api.PropertyValue{Known: true, Value: "private"}},
	} {
		if _, e := NormalizeObservedObject(api.FilterRule, row, p.Target, []VersionPropertyEvidence{proof}); e == nil {
			t.Fatal("invalid object evidence accepted")
		}
	}
	if _, e := NormalizeObservedObject(api.RoutingTable, row, p.Target, nil); e == nil {
		t.Fatal("table accepted filter-menu attributes")
	}
}

func TestNormalizeTableObjectKeepsMissingFIBUnknown(t *testing.T) {
	p, _, _, _ := inspectFixture(t)
	row := api.Record{".id": "*a", "name": "owned-table", "disabled": "false", "dynamic": "false", "invalid": "false", "comment": ""}
	got, err := NormalizeObservedObject(api.RoutingTable, row, p.Target, nil)
	if err != nil || got[".id"] != "*A" {
		t.Fatal(err)
	}
	if _, known := got["fib"]; known {
		t.Fatal("missing table FIB became a known false value")
	}
	proof := []VersionPropertyEvidence{{Resource: api.RoutingTable, ID: "*A", Field: "fib", Completed: true, Property: api.PropertyValue{Known: true, Value: "false"}}}
	got, err = NormalizeObservedObject(api.RoutingTable, row, p.Target, proof)
	if err != nil || got["fib"] != "false" {
		t.Fatal("actual FIB getter not preserved", err)
	}
}
