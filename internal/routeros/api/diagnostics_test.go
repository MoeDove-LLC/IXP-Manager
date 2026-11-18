package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLargeFixedFilterPrintRemainsBounded(t *testing.T) {
	const count = 2004
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		q, e := p.read()
		if e != nil {
			return e
		}
		for i := 1; i <= count; i++ {
			row := Record{".id": fmt.Sprintf("*%X", i), "chain": "controlled_generation_in", "rule": strings.Repeat(" ", 128) + "reject;", "comment": strings.Repeat("x", 128), "inactive": "false"}
			if e = p.response("!re", requestTag(q), row); e != nil {
				return e
			}
		}
		return p.response("!done", requestTag(q), nil)
	})
	c := dialFixture(t, f)
	started := time.Now()
	rows, e := c.Read(context.Background(), FilterRule)
	if e != nil || len(rows) != count {
		t.Fatal("large fixed projection failed", e)
	}
	if _, exists := rows[0]["disabled"]; exists {
		t.Fatal("missing flags became defaults")
	}
	t.Logf("fixed_filter_records=%d local_tls_read_milliseconds=%d", count, time.Since(started).Milliseconds())
}

func TestReadDetailedUsesFixedProjectionAndPreservesMissingFields(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if err := loginOK(p); err != nil {
			return err
		}
		words, err := p.read()
		if err != nil {
			return err
		}
		if words[0] != "/routing/filter/rule/print" || !has(words, "=detail=") || !has(words, "=.proplist="+strings.Join(dictionaries[FilterRule].properties, ",")) {
			return errors.New("detail request changed projection")
		}
		if err = p.response("!re", requestTag(words), Record{".id": "*1", "chain": "owned", "rule": "reject;", "inactive": "false", "private": "discard"}); err != nil {
			return err
		}
		return p.response("!done", requestTag(words), nil)
	})
	client := dialFixture(t, f)
	rows, err := client.ReadDetailed(context.Background(), FilterRule)
	if err != nil || len(rows) != 1 || rows[0]["inactive"] != "false" {
		t.Fatal("detail evidence unavailable", err)
	}
	if _, exists := rows[0]["disabled"]; exists {
		t.Fatal("missing flag was guessed")
	}
	if _, exists := rows[0]["private"]; exists {
		t.Fatal("unknown detail escaped projection")
	}
}

func TestReadFailureReportsResourceAndBoundWithoutValues(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if err := loginOK(p); err != nil {
			return err
		}
		words, err := p.read()
		if err != nil {
			return err
		}
		for _, id := range []string{"*1", "*2"} {
			if err = p.response("!re", requestTag(words), Record{".id": id, "name": "private-name-never-in-error"}); err != nil {
				return err
			}
		}
		return p.response("!done", requestTag(words), nil)
	})
	f.config.Limits.MaxRecords = 1
	client := dialFixture(t, f)
	_, err := client.Read(context.Background(), RoutingTable)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeLimit || failure.Resource != RoutingTable || failure.LimitKind != "records" || failure.ReplyRecords != 1 || failure.ReplyBytes <= 0 || failure.ReplySentences != 2 || failure.Deadline || failure.DeadlineKind != "none" {
		t.Fatalf("fixed read diagnostics missing: %+v", failure)
	}
	if strings.Contains(err.Error(), "private-name") {
		t.Fatal("response value leaked")
	}
}
func TestReadDeadlineKindDistinguishesCallerAndCommand(t *testing.T) {
	for _, kind := range []string{"context", "command"} {
		t.Run(kind, func(t *testing.T) {
			f := mockTLS(t, func(p *mockPeer) error {
				if err := loginOK(p); err != nil {
					return err
				}
				if _, err := p.read(); err != nil {
					return err
				}
				var b [1]byte
				_, _ = p.conn.Read(b[:])
				return nil
			})
			client := dialFixture(t, f)
			ctx := context.Background()
			cancel := func() {}
			if kind == "context" {
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
			} else {
				client.timeout = 30 * time.Millisecond
			}
			defer cancel()
			_, err := client.Read(ctx, FilterRule)
			var failure *Error
			if !errors.As(err, &failure) || failure.Resource != FilterRule || !failure.Deadline || failure.DeadlineKind != kind || failure.ReplyRecords != 0 {
				t.Fatalf("deadline classification missing: %+v", failure)
			}
		})
	}
}
func TestSensitiveGetterDoesNotExposeReplySizeDiagnostics(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if err := loginOK(p); err != nil {
			return err
		}
		words, err := p.read()
		if err != nil {
			return err
		}
		return p.response("!done", requestTag(words), Record{"ret": strings.Repeat("private-key", 20)})
	})
	client := dialFixture(t, f)
	client.limits.MaxWordBytes = 64
	_, err := client.ReadCredential(context.Background(), "*1")
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeLimit {
		t.Fatal("fixture did not reach bounded sensitive reply")
	}
	if failure.ReplyBytes != 0 || failure.ReplyRecords != 0 || failure.ReplySentences != 0 || failure.LimitKind != "" || failure.DeadlineKind != "" {
		t.Fatal("sensitive getter length exposed")
	}
}
