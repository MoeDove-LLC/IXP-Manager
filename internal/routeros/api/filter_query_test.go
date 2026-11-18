package api

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestFilterBooleanQueryUsesOnlyFixedPredicateAndNativeIDs(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		q, e := p.read()
		if e != nil {
			return e
		}
		want := []string{"/routing/filter/rule/print", "=.proplist=.id,disabled", "?disabled=false", "?-disabled", "?#|", ".tag=" + requestTag(q)}
		if !reflect.DeepEqual(q, want) {
			return errors.New("unexpected query")
		}
		for _, row := range []Record{{".id": "*b"}, {".id": "*A", "disabled": "false"}} {
			if e = p.response("!re", requestTag(q), row); e != nil {
				return e
			}
		}
		return p.response("!done", requestTag(q), nil)
	})
	c := dialFixture(t, f)
	ids, e := c.ReadFilterRuleDisabled(context.Background(), false)
	if e != nil || len(ids) != 2 || ids[0] != "*A" || ids[1] != "*B" {
		t.Fatal("fixed query evidence lost", e)
	}
}

func TestFilterBooleanQueryTrueUsesOnlyExplicitEquality(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if err := loginOK(p); err != nil {
			return err
		}
		request, err := p.read()
		if err != nil {
			return err
		}
		tag := requestTag(request)
		if !reflect.DeepEqual(request, []string{"/routing/filter/rule/print", "=.proplist=.id,disabled", "?disabled=true", ".tag=" + tag}) {
			return errors.New("true query changed its fixed predicate")
		}
		if err := p.response("!re", tag, Record{".id": "*c", "disabled": "true"}); err != nil {
			return err
		}
		return p.response("!done", tag, nil)
	})
	ids, err := dialFixture(t, f).ReadFilterRuleDisabled(context.Background(), true)
	if err != nil || !reflect.DeepEqual(ids, []string{"*C"}) {
		t.Fatal("true selection was not completed exact-ID evidence")
	}
}

func TestFilterBooleanQueryEmptyIsKnownOnlyWithSuccessfulCompletion(t *testing.T) {
	for _, mode := range []string{"empty-complete", "trap", "eof", "empty-without-done", "done-attributes", "wrong-tag"} {
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
				case "empty-complete":
					return p.response("!done", tag, nil)
				case "trap":
					return p.response("!trap", tag, Record{"message": "private-fixture-error"})
				case "eof":
					return nil
				case "empty-without-done":
					return p.response("!empty", tag, nil)
				case "done-attributes":
					return p.response("!done", tag, Record{"ret": "unexpected"})
				default:
					return p.response("!done", "unrelated-tag", nil)
				}
			})
			ids, err := dialFixture(t, f).ReadFilterRuleDisabled(context.Background(), false)
			if mode == "empty-complete" {
				if err != nil || ids == nil || len(ids) != 0 {
					t.Fatal("completed empty predicate lost its known result")
				}
			} else if err == nil || ids != nil {
				t.Fatal("unknown query outcome became a completed empty selection")
			}
		})
	}
}

func TestFilterBooleanQueryDeadlineReturnsNoPartialSelection(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if err := loginOK(p); err != nil {
			return err
		}
		request, err := p.read()
		if err != nil {
			return err
		}
		if err := p.response("!re", requestTag(request), Record{".id": "*A", "disabled": "false"}); err != nil {
			return err
		}
		var b [1]byte
		_, _ = p.conn.Read(b[:])
		return nil
	})
	client := dialFixture(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ids, err := client.ReadFilterRuleDisabled(ctx, false)
	var failure *Error
	if !errors.As(err, &failure) || ids != nil || !client.closed.Load() {
		t.Fatal("incomplete timed-out selection escaped as evidence")
	}
}
func TestFilterBooleanQueryRejectsDuplicatesAndContradictions(t *testing.T) {
	for _, rows := range [][]Record{{{".id": "0"}}, {{".id": "*A", "disabled": "false"}}, {{".id": "*A", "disabled": ""}}, {{".id": "*A"}, {".id": "*a"}}} {
		f := mockTLS(t, func(p *mockPeer) error {
			if e := loginOK(p); e != nil {
				return e
			}
			q, e := p.read()
			if e != nil {
				return e
			}
			for _, row := range rows {
				if e = p.response("!re", requestTag(q), row); e != nil {
					return e
				}
			}
			return p.response("!done", requestTag(q), nil)
		})
		c := dialFixture(t, f)
		ids, e := c.ReadFilterRuleDisabled(context.Background(), true)
		if e == nil || ids != nil || !c.closed.Load() {
			t.Fatal("invalid query returned evidence")
		}
	}
}
