package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMutationWhitelistRejectsPathsIndicesUnknownPropertiesAndInjection(t *testing.T) {
	good := Mutation{Resource: RoutingTable, Action: Add, Attributes: []Attribute{{Name: "name", Value: "md_table"}}}
	if e := ValidateMutation(good); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []Mutation{{Resource: Resource("/system/reboot"), Action: Add}, {Resource: SafeMode, Action: Action("take")}, {Resource: BGPConnection, Action: Set, ID: "0", Attributes: []Attribute{{Name: "disabled", Value: "yes"}}}, {Resource: BGPConnection, Action: Remove, ID: "[find name=anything]"}, {Resource: RoutingTable, Action: Set, ID: "*1", Attributes: []Attribute{{Name: "password", Value: "fake"}}}, {Resource: RoutingTable, Action: Add, Attributes: []Attribute{{Name: "name", Value: "md_table"}, {Name: "name", Value: "other"}}}, {Resource: RoutingTable, Action: Add, Attributes: []Attribute{{Name: "name", Value: "md_table\n/system/reboot"}}}, {Resource: FilterRule, Action: Add, Attributes: []Attribute{{Name: "chain", Value: "md_in"}, {Name: "rule", Value: "accept\n/system/reboot"}}}} {
		if e := ValidateMutation(bad); e == nil {
			t.Fatal("unsafe operation accepted")
		}
	}
	if e := ValidateMutation(Mutation{Resource: BGPConnection, Action: Set, ID: "*A", Attributes: []Attribute{{Name: "input.limit-process-routes-ipv6", Value: ""}}}); e == nil {
		t.Fatal("unsupported empty limit setter accepted")
	}
}
func TestAddReturnsStableIDAndLostCompletionIsUncertain(t *testing.T) {
	for _, mode := range []string{"id", "missing-id", "eof"} {
		t.Run(mode, func(t *testing.T) {
			f := mockTLS(t, func(p *mockPeer) error {
				if e := loginOK(p); e != nil {
					return e
				}
				words, e := p.read()
				if e != nil {
					return e
				}
				if words[0] != "/routing/table/add" || requestValue(words, "name") != "md_test" || requestHas(words, "fib") {
					return errors.New("wrong typed mutation")
				}
				if mode == "eof" {
					return nil
				}
				values := Record{}
				if mode == "id" {
					values["ret"] = "*A"
				}
				return p.response("!done", requestTag(words), values)
			})
			client := dialFixture(t, f)
			result, e := client.Apply(context.Background(), Mutation{Resource: RoutingTable, Action: Add, Attributes: []Attribute{{Name: "name", Value: "md_test"}}})
			if mode == "id" {
				if e != nil || !result.Acknowledged || result.ID != "*A" {
					t.Fatal("acknowledged native ID lost", e)
				}
			} else if e == nil || !IsUncertain(e) || result.Acknowledged {
				t.Fatal("unconfirmed add was reported as safe/certain", e)
			}
		})
	}
}
func TestInventorySafeModeProbePreservesUnsupportedAndUnknown(t *testing.T) {
	for _, safeMode := range []string{"supported", "unsupported", "unknown"} {
		t.Run(safeMode, func(t *testing.T) {
			f := mockTLS(t, func(p *mockPeer) error {
				if e := loginOK(p); e != nil {
					return e
				}
				for i := 0; i < 9; i++ {
					words, e := p.read()
					if e != nil {
						return e
					}
					path := words[0]
					tag := requestTag(words)
					properties := requestValue(words, ".proplist")
					if strings.Contains(properties, "tcp-md5-key") || strings.Contains(properties, "license-key") || properties == "" {
						return errors.New("default inventory requested sensitive/unspecified properties")
					}
					switch path {
					case "/system/resource/print":
						if e = p.response("!re", tag, Record{"version": "7.24.6 (stable)", "board-name": "CHR"}); e != nil {
							return e
						}
					case "/system/license/print":
						if properties != "system-id,software-id,level" {
							return errors.New("license whitelist changed")
						}
						if e = p.response("!re", tag, Record{"system-id": "synthetic-chr-id", "level": "p1", "license-key": "must-be-dropped"}); e != nil {
							return e
						}
					case "/system/identity/print":
						if e = p.response("!re", tag, Record{"name": "synthetic-router"}); e != nil {
							return e
						}
					case "/system/routerboard/print":
						if e = p.response("!trap", tag, Record{"category": "0", "message": "not a RouterBOARD"}); e != nil {
							return e
						}
					case "/routing/filter/rule/print":
						if !strings.Contains(properties, "invalid") {
							return errors.New("filter invalid flag not requested")
						}
						if e = p.response("!re", tag, Record{".id": "*1", "chain": "md_in", "rule": "reject"}); e != nil {
							return e
						}
					case "/safe-mode/print":
						switch safeMode {
						case "supported":
							if e = p.response("!re", tag, Record{"enabled": "false", "current": "false"}); e != nil {
								return e
							}
						case "unsupported":
							if e = p.response("!trap", tag, Record{"category": "0", "message": "unavailable"}); e != nil {
								return e
							}
						case "unknown":
							if e = p.response("!re", tag, Record{"owner": "console"}); e != nil {
								return e
							}
						}
					default:
						if e = p.response("!empty", tag, nil); e != nil {
							return e
						}
					}
					if e = p.response("!done", tag, nil); e != nil {
						return e
					}
				}
				return nil
			})
			client := dialFixture(t, f)
			inventory, e := client.Inventory(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if len(inventory.Licenses) != 1 || inventory.Licenses[0]["license-key"] != "" || inventory.Capabilities[RouterBoard] != Unsupported {
				t.Fatal("optional hardware/license discovery failed")
			}
			if _, present := inventory.FilterRules[0]["invalid"]; present {
				t.Fatal("missing native flag was fabricated as false")
			}
			switch safeMode {
			case "supported":
				if inventory.SafeMode == nil || inventory.SafeMode.Enabled || inventory.Capabilities[SafeMode] != Supported {
					t.Fatal("explicit safe-mode read lost")
				}
			case "unsupported":
				if inventory.SafeMode != nil || inventory.Capabilities[SafeMode] != Unsupported {
					t.Fatal("unavailable safe mode got a fake owner")
				}
			case "unknown":
				if inventory.SafeMode != nil || inventory.Capabilities[SafeMode] != Unknown {
					t.Fatal("incomplete safe mode became false")
				}
			}
		})
	}
}
func TestExplicitPropertyReadKeepsMissingRetUnknown(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		for i := 0; i < 2; i++ {
			words, e := p.read()
			if e != nil {
				return e
			}
			if words[0] != "/routing/filter/rule/get" || requestValue(words, ".id") != "*1" || requestValue(words, "value-name") != "invalid" {
				return errors.New("property get escaped fixed dictionary")
			}
			values := Record{}
			if i == 0 {
				values["ret"] = "false"
			}
			if e = p.response("!done", requestTag(words), values); e != nil {
				return e
			}
		}
		return nil
	})
	client := dialFixture(t, f)
	first, e := client.ReadProperty(context.Background(), FilterRule, "*1", "invalid")
	if e != nil || !first.Known || first.Value != "false" {
		t.Fatal("explicit false was not retained", e)
	}
	second, e := client.ReadProperty(context.Background(), FilterRule, "*1", "invalid")
	if e != nil || second.Known {
		t.Fatal("absent ret became false")
	}
	if _, e = client.ReadProperty(context.Background(), BGPConnection, "*1", "tcp-md5-key"); e == nil {
		t.Fatal("ordinary property reader exposed sensitive getter")
	}
}
func TestWaitingContextDoesNotCancelOtherInFlightCommand(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		words, e := p.read()
		if e != nil {
			return e
		}
		close(started)
		<-release
		if e = p.response("!re", requestTag(words), Record{"name": "completed"}); e != nil {
			return e
		}
		return p.response("!done", requestTag(words), nil)
	})
	client := dialFixture(t, f)
	first := make(chan error, 1)
	go func() { _, e := client.Read(context.Background(), Identity); first <- e }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, e := client.Apply(ctx, Mutation{Resource: BGPConnection, Action: Disable, ID: "*1"})
	if e == nil || IsUncertain(e) || client.closed.Load() {
		t.Fatal("unsent queued operation corrupted the active command", e)
	}
	close(release)
	if e = <-first; e != nil {
		t.Fatal("waiting cancellation disrupted another read", e)
	}
}

func TestDriftPropertiesAreReadOnlyAndDefaultInventoryNeverRequestsMD5(t *testing.T) {
	for _, name := range []string{"input.filter-nlri", "output.filter-select", "output.network", "output.redistribute"} {
		if !has(dictionaries[BGPConnection].properties, name) {
			t.Fatal("drift property missing", name)
		}
		if e := ValidateMutation(Mutation{Resource: BGPConnection, Action: Set, ID: "*1", Attributes: []Attribute{{Name: name, Value: ""}}}); e == nil {
			t.Fatal("read-only drift property became writable", name)
		}
	}
	for _, definition := range dictionaries {
		seen := map[string]bool{}
		for _, name := range definition.properties {
			if sensitiveName(name) || seen[name] {
				t.Fatal("sensitive or duplicate default property", name)
			}
			seen[name] = true
		}
	}
}

func requestHas(words []string, name string) bool {
	for _, word := range words {
		if strings.HasPrefix(word, "="+name+"=") {
			return true
		}
	}
	return false
}
func TestNativeSwitchPresenceCannotEncodeFalseAndUnsetIsRestricted(t *testing.T) {
	for _, value := range []string{"no", "false", "", "0"} {
		for _, action := range []Action{Add, Set} {
			m := Mutation{Resource: RoutingTable, Action: action, Attributes: []Attribute{{Name: "fib", Value: value}}}
			if action == Add {
				m.Attributes = append(m.Attributes, Attribute{Name: "name", Value: "md_switch"})
			} else {
				m.ID = "*1"
			}
			if e := ValidateMutation(m); e == nil {
				t.Fatalf("unsafe FIB false/presence form accepted: %q", value)
			}
		}
	}
	for _, value := range []string{"yes", "true"} {
		words, e := mutationWords(Mutation{Resource: RoutingTable, Action: Set, ID: "*1", Attributes: []Attribute{{Name: "fib", Value: value}}})
		if e != nil || !requestHas(words, "fib") || requestValue(words, "fib") != "" {
			t.Fatal("switch-on did not produce bare API attribute", e)
		}
	}
	for _, m := range []Mutation{{Resource: RoutingTable, Action: Unset, ID: "*1", Property: "fib"}, {Resource: BGPConnection, Action: Unset, ID: "*2", Property: "input.limit-process-routes-ipv4"}, {Resource: BGPConnection, Action: Unset, ID: "*2", Property: "input.limit-process-routes-ipv6"}} {
		words, e := mutationWords(m)
		if e != nil || words[0] != dictionaries[m.Resource].path+"/unset" || requestValue(words, ".id") != m.ID || requestValue(words, "value-name") != m.Property || len(words) != 3 {
			t.Fatal("typed unset command shape changed", e)
		}
	}
	for _, m := range []Mutation{{Resource: RoutingTable, Action: Unset, ID: "0", Property: "fib"}, {Resource: RoutingTable, Action: Unset, ID: "*1", Property: "name"}, {Resource: BGPInstance, Action: Unset, ID: "*1", Property: "fib"}, {Resource: BGPConnection, Action: Unset, ID: "*1", Property: "tcp-md5-key"}, {Resource: BGPConnection, Action: Unset, ID: "*1", Property: "input.limit-process-routes-ipv6", Attributes: []Attribute{{Name: "disabled", Value: "yes"}}}, {Resource: RoutingTable, Action: Remove, ID: "*1", Property: "fib"}, {Resource: RoutingTable, Action: Set, ID: "*1", Property: "fib", Attributes: []Attribute{{Name: "name", Value: "md_other"}}}} {
		if e := ValidateMutation(m); e == nil {
			t.Fatal("unset escaped its narrow property/action selector")
		}
	}
}
func TestSwitchOnAndUnsetWireAreLiteralAndAcknowledged(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		for i := 0; i < 2; i++ {
			words, e := p.read()
			if e != nil {
				return e
			}
			if requestValue(words, ".id") != "*A" {
				return errors.New("mutation selected wrong native ID")
			}
			if i == 0 {
				if words[0] != "/routing/table/set" || !requestHas(words, "fib") || requestValue(words, "fib") != "" {
					return errors.New("switch-on was not encoded by presence")
				}
			} else if words[0] != "/routing/table/unset" || requestValue(words, "value-name") != "fib" || requestHas(words, "fib") {
				return errors.New("unset was encoded as a false setter")
			}
			if e = p.response("!done", requestTag(words), nil); e != nil {
				return e
			}
		}
		return nil
	})
	client := dialFixture(t, f)
	for _, m := range []Mutation{{Resource: RoutingTable, Action: Set, ID: "*A", Attributes: []Attribute{{Name: "fib", Value: "yes"}}}, {Resource: RoutingTable, Action: Unset, ID: "*A", Property: "fib"}} {
		result, e := client.Apply(context.Background(), m)
		if e != nil || !result.Acknowledged {
			t.Fatal("typed switch operation failed", e)
		}
	}
}

func TestConnectionLocalASNHasClosedScalarSetter(t *testing.T) {
	makeMutation := func(action Action, value string) Mutation {
		m := Mutation{Resource: BGPConnection, Action: action, Attributes: []Attribute{{Name: "as", Value: value}}}
		if action == Set {
			m.ID = "*1"
		} else {
			m.Attributes = append(m.Attributes, []Attribute{{Name: "name", Value: "md_peer"}, {Name: "instance", Value: "md_instance"}, {Name: "disabled", Value: "yes"}, {Name: "afi", Value: "ip"}, {Name: "local.address", Value: "192.0.2.1"}, {Name: "local.role", Value: "ebgp-rs"}, {Name: "remote.address", Value: "192.0.2.2"}, {Name: "remote.as", Value: "64501"}, {Name: "input.filter", Value: "md_in"}, {Name: "output.filter-chain", Value: "md_out"}}...)
		}
		return m
	}
	for _, action := range []Action{Add, Set} {
		for _, value := range []string{"1", "64500", "4294967294"} {
			mutation := makeMutation(action, value)
			if e := ValidateMutation(mutation); e != nil {
				t.Fatalf("valid scalar local ASN rejected: %v", e)
			}
		}
		for _, value := range []string{"", "0", "4294967295", "4294967296", "064500", "+64500", "-1", "64500/0", "64500,64501", "64500\n/system/reboot", "[find]"} {
			mutation := makeMutation(action, value)
			if e := ValidateMutation(mutation); e == nil {
				t.Fatal("noncanonical scalar local ASN accepted")
			}
		}
	}
	if e := ValidateMutation(Mutation{Resource: BGPConnection, Action: Unset, ID: "*1", Property: "as"}); e == nil {
		t.Fatal("local ASN could revert to ambiguous inheritance")
	}
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		words, e := p.read()
		if e != nil {
			return e
		}
		if words[0] != "/routing/bgp/connection/set" || requestValue(words, ".id") != "*1" || requestValue(words, "as") != "64500" {
			return errors.New("local ASN setter not fixed typed wire command")
		}
		return p.response("!done", requestTag(words), nil)
	})
	client := dialFixture(t, f)
	if result, e := client.Apply(context.Background(), Mutation{Resource: BGPConnection, Action: Set, ID: "*1", Attributes: []Attribute{{Name: "as", Value: "64500"}}}); e != nil || !result.Acknowledged {
		t.Fatalf("typed local ASN setter failed: %v", e)
	}
}

func TestDefaultPrependZeroIsTypedAndReadBackWithoutInheritance(t *testing.T) {
	for _, value := range []string{"0", "1", "255"} {
		if e := ValidateMutation(Mutation{Resource: BGPConnection, Action: Set, ID: "*1", Attributes: []Attribute{{Name: "output.default-prepend", Value: value}}}); e != nil {
			t.Fatal("valid uint8 prepend rejected", e)
		}
	}
	for _, value := range []string{"", "00", "-1", "+0", "256", "0/0", "0\n/system/reboot"} {
		if e := ValidateMutation(Mutation{Resource: BGPConnection, Action: Set, ID: "*1", Attributes: []Attribute{{Name: "output.default-prepend", Value: value}}}); e == nil {
			t.Fatal("noncanonical prepend accepted")
		}
	}
	if e := ValidateMutation(Mutation{Resource: BGPConnection, Action: Unset, ID: "*1", Property: "output.default-prepend"}); e == nil {
		t.Fatal("prepend could silently revert to inherited default")
	}
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		words, e := p.read()
		if e != nil {
			return e
		}
		if words[0] != "/routing/bgp/connection/set" || requestValue(words, ".id") != "*1" || requestValue(words, "output.default-prepend") != "0" {
			return errors.New("explicit zero prepend not encoded")
		}
		if e = p.response("!done", requestTag(words), nil); e != nil {
			return e
		}
		words, e = p.read()
		if e != nil {
			return e
		}
		if words[0] != "/routing/bgp/connection/get" || requestValue(words, ".id") != "*1" || requestValue(words, "value-name") != "output.default-prepend" {
			return errors.New("prepend getter is not fixed")
		}
		return p.response("!done", requestTag(words), Record{"ret": "0"})
	})
	client := dialFixture(t, f)
	if _, e := client.Apply(context.Background(), Mutation{Resource: BGPConnection, Action: Set, ID: "*1", Attributes: []Attribute{{Name: "output.default-prepend", Value: "0"}}}); e != nil {
		t.Fatal(e)
	}
	value, e := client.ReadProperty(context.Background(), BGPConnection, "*1", "output.default-prepend")
	if e != nil || !value.Known || value.Value != "0" {
		t.Fatal("explicit zero was lost", e)
	}
}
