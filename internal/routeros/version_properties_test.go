package routeros

import (
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

func TestVersionProperties7246RequiresCompletedGetter(t *testing.T) {
	_, inv, known, e := inspectFixture(t)
	inv.Resources[0]["version"] = "7.24.6 (long-term)"
	delete(inv.Tables[0], "fib")
	if _, err := buildSnapshot(inv, known, e, matchedCredential); err == nil {
		t.Fatal("print absence became non-FIB without a getter")
	}
	e.Properties = []VersionPropertyEvidence{{Resource: api.RoutingTable, ID: "*1", Field: "fib", Completed: true, Property: api.PropertyValue{Known: false}}}
	s, err := buildSnapshot(inv, known, e, matchedCredential)
	if err != nil {
		t.Fatal(err)
	}
	if s.TableFIB {
		t.Fatal("verified native unset became FIB")
	}
	if _, exists := inv.Tables[0]["fib"]; exists {
		t.Fatal("normalization modified original print evidence")
	}
	if _, err := NewAPIResolver(inv, known.Target, e); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*TargetEvidence){
		"not completed":         func(e *TargetEvidence) { e.Properties[0].Completed = false },
		"contradictory unknown": func(e *TargetEvidence) { e.Properties[0].Property.Value = "true" },
		"wrong object":          func(e *TargetEvidence) { e.Properties[0].ID = "*2" },
		"duplicate getter":      func(e *TargetEvidence) { e.Properties = append(e.Properties, e.Properties[0]) },
		"sensitive field": func(e *TargetEvidence) {
			e.Properties[0].Resource = api.BGPConnection
			e.Properties[0].Field = "tcp-md5-key"
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := e
			copy.Properties = append([]VersionPropertyEvidence(nil), e.Properties...)
			mutate(&copy)
			if _, err := buildSnapshot(inv, known, copy, matchedCredential); err == nil {
				t.Fatal("unverified getter evidence accepted")
			}
		})
	}
}

func TestVersionPropertiesNoRetWhitelistAndVersionAreExact(t *testing.T) {
	_, inv, known, e := inspectFixture(t)
	for resource, values := range unsetProperties7246 {
		var row api.Record
		switch resource {
		case api.RoutingTable:
			row = inv.Tables[0]
		case api.BGPInstance:
			row = inv.Instances[0]
		case api.BGPConnection:
			row = inv.Connections[0]
		}
		for field := range values {
			if field == "input.limit-process-routes-ipv4" || field == "as" {
				continue
			} // active limit must remain configured
			delete(row, field)
			e.Properties = append(e.Properties, VersionPropertyEvidence{Resource: resource, ID: "*1", Field: field, Completed: true})
		}
	}
	if _, err := buildSnapshot(inv, known, e, matchedCredential); err != nil {
		t.Fatal(err)
	}
	other := known.Target
	other.Version = "7.24.7"
	if _, err := normalizeVersionProperties(inv, other, e.Properties); err == nil {
		t.Fatal("new version inherited unset semantics without measurement")
	}
	if observedVersionMatches("7.24.7 (long-term)", "7.24.7") || observedVersionMatches("7.24.6 (stable)", "7.24.6") || observedVersionMatches("7.24.6beta1 (long-term)", "7.24.6") {
		t.Fatal("arbitrary version suffix was stripped")
	}
	if !observedVersionMatches("7.24.6 (long-term)", "7.24.6") {
		t.Fatal("measured exact version rejected")
	}
	// A no-ret comment is not proven empty merely because another property
	// on the same object uses absence as its default.
	e.Properties = append(e.Properties, VersionPropertyEvidence{Resource: api.RoutingTable, ID: "*1", Field: "comment", Completed: true})
	if _, err := buildSnapshot(inv, known, e, matchedCredential); err == nil {
		t.Fatal("unlisted no-ret property became a default")
	}
}

func TestVersionPropertiesPresenceConflictsFailBothDirections(t *testing.T) {
	for _, entry := range []struct {
		resource api.Resource
		field    string
	}{{api.RoutingTable, "fib"}, {api.RoutingTable, "dynamic"}, {api.BGPConnection, "dynamic"}} {
		t.Run(string(entry.resource)+"/"+entry.field, func(t *testing.T) {
			_, inv, known, e := inspectFixture(t)
			var row api.Record
			if entry.resource == api.RoutingTable {
				row = inv.Tables[0]
			} else {
				row = inv.Connections[0]
			}
			row[entry.field] = ""
			e.Properties = []VersionPropertyEvidence{{Resource: entry.resource, ID: "*1", Field: entry.field, Completed: true}}
			if _, err := normalizeVersionProperties(inv, known.Target, e.Properties); err == nil {
				t.Fatal("print presence plus getter no-ret accepted")
			}
			delete(row, entry.field)
			e.Properties[0].Property = api.PropertyValue{Known: true, Value: "true"}
			if _, err := normalizeVersionProperties(inv, known.Target, e.Properties); err == nil {
				t.Fatal("print absence plus getter true accepted")
			}
			row[entry.field] = ""
			normalized, err := normalizeVersionProperties(inv, known.Target, e.Properties)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			if entry.resource == api.RoutingTable {
				got = normalized.Tables[0][entry.field]
			} else {
				got = normalized.Connections[0][entry.field]
			}
			if got != "true" {
				t.Fatal("explicit getter did not preserve true presence")
			}
		})
	}
}

func TestFilterInactiveIsValidityButConnectionInactiveIsNotHealth(t *testing.T) {
	_, inv, known, e := inspectFixture(t)
	inv.Instances[0]["inactive"] = "true"
	inv.Connections[0]["inactive"] = "true"
	if _, err := buildSnapshot(inv, known, e, matchedCredential); err != nil {
		t.Fatalf("runtime inactivity was confused with config invalidity: %v", err)
	}
	inv.FilterRules[0]["inactive"] = "true"
	if _, err := buildSnapshot(inv, known, e, matchedCredential); err == nil {
		t.Fatal("unusable filter accepted")
	}
	inv.FilterRules[0]["inactive"] = "false"
	inv.FilterRules[0]["invalid"] = "false"
	if _, err := buildSnapshot(inv, known, e, matchedCredential); err == nil {
		t.Fatal("nonexistent native invalid property silently accepted")
	}
}
