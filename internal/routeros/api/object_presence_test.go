package api

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestObjectPresenceRequiresCompletedExactIDRead(t *testing.T) {
	for _, resource := range []Resource{RoutingTable, BGPInstance, BGPConnection, FilterRule} {
		for _, present := range []bool{false, true} {
			t.Run(string(resource)+map[bool]string{true: "-present", false: "-absent"}[present], func(t *testing.T) {
				f := mockTLS(t, func(p *mockPeer) error {
					if err := loginOK(p); err != nil {
						return err
					}
					request, err := p.read()
					if err != nil {
						return err
					}
					want := []string{dictionaries[resource].path + "/print", "=.proplist=" + strings.Join(dictionaries[resource].properties, ","), "?.id=*A", ".tag=" + requestTag(request)}
					if !reflect.DeepEqual(request, want) {
						return errors.New("unapproved presence query")
					}
					if present {
						row := Record{".id": "*a", "name": "target", "chain": "target", "rule": "reject;", "tcp-md5-key": "must-not-escape"}
						if err = p.response("!re", requestTag(request), row); err != nil {
							return err
						}
					}
					return p.response("!done", requestTag(request), nil)
				})
				got, err := dialFixture(t, f).ReadObjectPresence(context.Background(), resource, "*A")
				if err != nil || got.Present != present {
					t.Fatal("presence not supported by complete reply", err)
				}
				if !present && got.Record != nil {
					t.Fatal("absence fabricated a record")
				}
				if present && (got.Record[".id"] != "*a" || got.Record["tcp-md5-key"] != "") {
					t.Fatal("wrong identity or secret leaked")
				}
				if _, assumed := got.Record["disabled"]; assumed {
					t.Fatal("presence read invented omitted properties")
				}
			})
		}
	}
}

func TestObjectPresenceNeverTreatsUnknownOrMalformedReplyAsAbsence(t *testing.T) {
	for _, mode := range []string{"eof", "trap", "done-attributes", "wrong-id", "multiple", "missing-required"} {
		t.Run(mode, func(t *testing.T) {
			f := mockTLS(t, func(p *mockPeer) error {
				if err := loginOK(p); err != nil {
					return err
				}
				request, err := p.read()
				if err != nil {
					return err
				}
				tag := requestTag(request)
				switch mode {
				case "eof":
					return nil
				case "trap":
					if err = p.response("!trap", tag, Record{"category": "2", "message": "fixture permission denied"}); err != nil {
						return err
					}
				case "done-attributes":
					return p.response("!done", tag, Record{"ret": "untrusted"})
				default:
					row := Record{".id": "*1", "chain": "target", "rule": "reject;"}
					if mode == "wrong-id" {
						row[".id"] = "*2"
					}
					if mode == "missing-required" {
						delete(row, "rule")
					}
					if err = p.response("!re", tag, row); err != nil {
						return err
					}
					if mode == "multiple" {
						if err = p.response("!re", tag, row); err != nil {
							return err
						}
					}
				}
				return p.response("!done", tag, nil)
			})
			got, err := dialFixture(t, f).ReadObjectPresence(context.Background(), FilterRule, "*1")
			if err == nil || got.Record != nil || got.Present {
				t.Fatal("unknown outcome became successful presence evidence")
			}
		})
	}
	for _, query := range []struct {
		resource Resource
		id       string
	}{{SystemLicense, "*1"}, {SafeMode, "*1"}, {FilterRule, "0"}, {FilterRule, "*1,*2"}, {FilterRule, "*1\n"}} {
		var client *Client
		if _, err := client.ReadObjectPresence(context.Background(), query.resource, query.id); err == nil {
			t.Fatal("unsafe query reached transport")
		}
	}
}

func TestObjectPresenceEmptyMarkerWithoutCompletionRemainsUnknown(t *testing.T) {
	received, release := make(chan struct{}), make(chan struct{})
	f := mockTLS(t, func(p *mockPeer) error {
		if err := loginOK(p); err != nil {
			return err
		}
		request, err := p.read()
		if err != nil {
			return err
		}
		if err = p.response("!empty", requestTag(request), nil); err != nil {
			return err
		}
		close(received)
		<-release
		return nil
	})
	client := dialFixture(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		presence ObjectPresence
		err      error
	}
	done := make(chan result, 1)
	go func() { value, err := client.ReadObjectPresence(ctx, FilterRule, "*1"); done <- result{value, err} }()
	<-received
	cancel()
	got := <-done
	close(release)
	if got.err == nil || got.presence.Present || got.presence.Record != nil {
		t.Fatal("!empty or cancellation was promoted to completed absence")
	}
}
