package api

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestObjectReadUsesExactNativeIDAndFixedCompletePropertyList(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if err := loginOK(p); err != nil {
			return err
		}
		request, err := p.read()
		if err != nil {
			return err
		}
		want := []string{"/routing/filter/rule/print", "=.proplist=" + strings.Join(dictionaries[FilterRule].properties, ","), "=from=*A", ".tag=" + requestTag(request)}
		if !reflect.DeepEqual(request, want) {
			return errors.New("object query was not the closed ID read")
		}
		if err = p.response("!re", requestTag(request), Record{".id": "*a", "chain": "owned", "rule": "reject;", "tcp-md5-key": "unrequested-private-value"}); err != nil {
			return err
		}
		return p.response("!done", requestTag(request), nil)
	})
	c := dialFixture(t, f)
	row, err := c.ReadObject(context.Background(), FilterRule, "*A")
	if err != nil || row[".id"] != "*a" || row["rule"] != "reject;" {
		t.Fatal("exact ID row was lost", err)
	}
	if _, present := row["disabled"]; present {
		t.Fatal("missing property was guessed")
	}
	if _, present := row["tcp-md5-key"]; present {
		t.Fatal("unrequested sensitive property escaped the fixed read schema")
	}
}

func TestObjectReadRefusesEmptyDuplicateWrongAndMalformedRows(t *testing.T) {
	for _, mode := range []string{"empty", "multiple", "wrong-id", "missing-required", "unexpected-done"} {
		t.Run(mode, func(t *testing.T) {
			f := mockTLS(t, func(p *mockPeer) error {
				if err := loginOK(p); err != nil {
					return err
				}
				request, err := p.read()
				if err != nil {
					return err
				}
				row := Record{".id": "*1", "name": "peer"}
				if mode == "wrong-id" {
					row[".id"] = "*2"
				}
				if mode == "missing-required" {
					delete(row, "name")
				}
				if mode != "empty" {
					if err = p.response("!re", requestTag(request), row); err != nil {
						return err
					}
				}
				if mode == "multiple" {
					if err = p.response("!re", requestTag(request), row); err != nil {
						return err
					}
				}
				if mode == "unexpected-done" {
					return p.response("!done", requestTag(request), Record{"ret": "untrusted"})
				}
				return p.response("!done", requestTag(request), nil)
			})
			row, err := dialFixture(t, f).ReadObject(context.Background(), BGPConnection, "*1")
			if err == nil || row != nil {
				t.Fatal("ambiguous or malformed object became state evidence")
			}
		})
	}
	for _, query := range []struct {
		resource Resource
		id       string
	}{{RoutingTable, "*1"}, {SystemLicense, "*1"}, {FilterRule, "1"}, {FilterRule, "*1,*2"}, {FilterRule, "*1\n"}} {
		var c *Client
		if _, err := c.ReadObject(context.Background(), query.resource, query.id); err == nil {
			t.Fatal("unapproved selection reached the transport")
		}
	}
}

func TestIdentityReadCannotMasqueradeAsCompleteRoutingInventory(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if err := loginOK(p); err != nil {
			return err
		}
		for _, resource := range []Resource{SystemResource, SystemLicense, RouterBoard} {
			request, err := p.read()
			if err != nil || request[0] != dictionaries[resource].path+"/print" {
				return errors.New("identity read accessed a non-identity menu")
			}
			row := Record{"version": "7.24.6"}
			if resource == SystemLicense {
				row = Record{"system-id": "fixture-device"}
			}
			if resource != RouterBoard {
				if err = p.response("!re", requestTag(request), row); err != nil {
					return err
				}
			}
			if err = p.response("!done", requestTag(request), nil); err != nil {
				return err
			}
		}
		return nil
	})
	identity, err := dialFixture(t, f).ReadIdentity(context.Background())
	if err != nil || len(identity.Resources) != 1 || len(identity.Licenses) != 1 || len(identity.RouterBoards) != 0 {
		t.Fatal("actual identity evidence was not preserved", err)
	}
	for _, resource := range []Resource{RoutingTable, BGPInstance, BGPConnection, FilterRule, SafeMode} {
		if _, claimed := identity.Capabilities[resource]; claimed {
			t.Fatal("partial read claimed a complete routing menu")
		}
	}
}

func TestDirectRuleFailureNeedsSeparateCompletedAbsenceQuery(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if err := loginOK(p); err != nil {
			return err
		}
		request, err := p.read()
		if err != nil || len(request) != 4 || request[2] != "=from=*A" {
			return errors.New("existing rule did not use fixed direct selection")
		}
		if err = p.response("!trap", requestTag(request), Record{"category": "0", "message": "fixture missing item or command"}); err != nil {
			return err
		}
		if err = p.response("!done", requestTag(request), nil); err != nil {
			return err
		}
		request, err = p.read()
		if err != nil || len(request) != 4 || request[2] != "?.id=*A" {
			return errors.New("absence bypassed the independent completed query")
		}
		return p.response("!done", requestTag(request), nil)
	})
	c := dialFixture(t, f)
	if row, err := c.ReadObject(context.Background(), FilterRule, "*A"); err == nil || row != nil {
		t.Fatal("direct selection failure became successful evidence")
	}
	result, err := c.ReadObjectPresence(context.Background(), FilterRule, "*A")
	if err != nil || result.Present || result.Record != nil {
		t.Fatal("separate successful absence query was lost", err)
	}
}
