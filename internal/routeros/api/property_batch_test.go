package api

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func getterQueries() []PropertyRequest {
	return []PropertyRequest{{Resource: RoutingTable, ID: "*1", Property: "fib"}, {Resource: FilterRule, ID: "*2", Property: "disabled"}, {Resource: BGPConnection, ID: "*3", Property: "as"}}
}
func TestPropertyPipelineMatchesOutOfOrderTagsAndPreservesAbsence(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		requests := make([][]string, 3)
		for i := range requests {
			var e error
			requests[i], e = p.read()
			if e != nil {
				return e
			}
			if !strings.HasSuffix(requests[i][0], "/get") {
				return errors.New("not fixed getter")
			}
		}
		if e := p.response("!done", requestTag(requests[2]), Record{"ret": "64500"}); e != nil {
			return e
		}
		if e := p.response("!done", requestTag(requests[1]), Record{"ret": ""}); e != nil {
			return e
		}
		if e := p.response("!done", requestTag(requests[0]), nil); e != nil {
			return e
		}
		next, e := p.read()
		if e != nil {
			return e
		}
		return p.response("!done", requestTag(next), Record{"ret": "after-pipeline"})
	})
	c := dialFixture(t, f)
	values, e := c.ReadProperties(context.Background(), getterQueries())
	if e != nil || len(values) != 3 || values[0].Known || !values[1].Known || values[1].Value != "" || !values[2].Known || values[2].Value != "64500" {
		t.Fatal("tag/absence semantics lost", e)
	}
	value, e := c.ReadProperty(context.Background(), RoutingTable, "*1", "name")
	if e != nil || !value.Known || value.Value != "after-pipeline" {
		t.Fatal("serialized stream not reusable", e)
	}
}
func TestPropertyPipelineRejectsAmbiguousRepliesWithoutPartialResult(t *testing.T) {
	for _, mode := range []string{"unknown-tag", "duplicate-done", "trap", "data-row", "bad-ret", "missing-completion"} {
		t.Run(mode, func(t *testing.T) {
			f := mockTLS(t, func(p *mockPeer) error {
				if e := loginOK(p); e != nil {
					return e
				}
				requests := make([][]string, 3)
				for i := range requests {
					var e error
					requests[i], e = p.read()
					if e != nil {
						return e
					}
				}
				tag := requestTag(requests[0])
				switch mode {
				case "unknown-tag":
					return p.response("!done", "foreign-tag", nil)
				case "duplicate-done":
					if e := p.response("!done", tag, nil); e != nil {
						return e
					}
					return p.response("!done", tag, nil)
				case "trap":
					return p.response("!trap", tag, Record{"message": "never-return-this-private-value"})
				case "data-row":
					return p.response("!re", tag, Record{"ret": "unexpected"})
				case "bad-ret":
					return p.response("!done", tag, Record{"other": "private"})
				default:
					return p.response("!done", tag, nil)
				}
			})
			c := dialFixture(t, f)
			values, e := c.ReadProperties(context.Background(), getterQueries())
			if e == nil || values != nil || !c.closed.Load() || strings.Contains(e.Error(), "private") {
				t.Fatal("bad pipeline returned partial/sensitive state")
			}
		})
	}
}
func TestPropertyPipelineRejectsSensitiveAndUnboundedRequestsBeforeIO(t *testing.T) {
	for _, queries := range [][]PropertyRequest{
		nil,
		{{Resource: BGPConnection, ID: "*1", Property: "tcp-md5-key"}},
		{{Resource: Resource("login"), ID: "*1", Property: "password"}},
		{{Resource: FilterRule, ID: "0", Property: "rule"}},
		{{Resource: FilterRule, ID: "*1", Property: ".id"}},
		{{Resource: FilterRule, ID: "*1", Property: "rule"}, {Resource: FilterRule, ID: "*1", Property: "rule"}},
		make([]PropertyRequest, MaxPropertyBatch+1),
	} {
		if values, e := (&Client{}).ReadProperties(context.Background(), queries); e == nil || values != nil {
			t.Fatal("invalid/sensitive batch accepted")
		}
	}
}
func TestPropertyPipelineDeadlineClosesAndDoesNotReturnPartial(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		for range getterQueries() {
			if _, e := p.read(); e != nil {
				return e
			}
		}
		var b [1]byte
		_, _ = p.conn.Read(b[:])
		return nil
	})
	c := dialFixture(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	values, e := c.ReadProperties(ctx, getterQueries())
	var failure *Error
	if !errors.As(e, &failure) || values != nil || !failure.Deadline || failure.DeadlineKind != "context" || !c.closed.Load() {
		t.Fatal("bounded pipeline timeout mishandled", e)
	}
}

type batchSlowClose struct {
	net.Conn
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *batchSlowClose) Close() error {
	c.once.Do(func() { close(c.started); <-c.release; _ = c.Conn.Close() })
	return nil
}

type batchCancelAtCompletion struct {
	net.Conn
	cancel  context.CancelFunc
	closing <-chan struct{}
	once    sync.Once
}

func (c *batchCancelAtCompletion) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	if n == 1 && data[0] == 0 {
		c.once.Do(func() { c.cancel(); <-c.closing })
	}
	return n, err
}
func TestPropertyPipelineWaitsForStartedCloseCallbackBeforeReturning(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		request, e := p.read()
		if e != nil {
			return e
		}
		return p.response("!done", requestTag(request), nil)
	})
	client := dialFixture(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow := &batchSlowClose{Conn: client.raw, started: make(chan struct{}), release: make(chan struct{})}
	client.raw = slow
	client.conn = &batchCancelAtCompletion{Conn: client.conn, cancel: cancel, closing: slow.started}
	returned := make(chan struct{})
	go func() {
		_, _ = client.ReadProperties(ctx, []PropertyRequest{{Resource: RoutingTable, ID: "*1", Property: "fib"}})
		close(returned)
	}()
	select {
	case <-slow.started:
	case <-time.After(time.Second):
		close(slow.release)
		t.Fatal("callback not started")
	}
	select {
	case <-returned:
		close(slow.release)
		t.Fatal("batch returned while old cancellation callback could still close successor")
	case <-time.After(20 * time.Millisecond):
	}
	close(slow.release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("pipeline callback did not settle")
	}
}
