package terminal

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

func ownedRule() api.Mutation {
	return api.Mutation{Resource: api.FilterRule, Action: api.Set, ID: "*A", Attributes: []api.Attribute{{Name: "rule", Value: "if (bgp-as-path 64501$) { accept; }"}}}
}
func TestOwnedMutationQuotesDollarAndRejectsCommandSurface(t *testing.T) {
	m := ownedRule()
	raw, err := encodeOwned(m)
	if err != nil {
		t.Fatal(err)
	}
	escaped := string([]byte{92, 36})
	if !strings.Contains(string(raw), "64501"+escaped) || !strings.HasPrefix(string(raw), "/routing/filter/rule/set *A rule=") {
		t.Fatal("literal dollar or single native ID lost")
	}
	value := string([]byte{34, 59, 32, 36, 91, 92}) + "unsafe"
	got := ownedQuote(value)
	if got[0] != 34 || got[len(got)-1] != 34 || !strings.Contains(got, string([]byte{92, 34})) || !strings.Contains(got, string([]byte{92, 36})) || !strings.Contains(got, string([]byte{92, 92})) {
		t.Fatal("CLI string escapes incomplete")
	}
	for name, bad := range map[string]api.Mutation{
		"add":           {Resource: api.FilterRule, Action: api.Add, Attributes: []api.Attribute{{Name: "chain", Value: "test"}, {Name: "rule", Value: "accept;"}}},
		"remove":        {Resource: api.BGPConnection, Action: api.Remove, ID: "*A"},
		"table":         {Resource: api.RoutingTable, Action: api.Set, ID: "*A", Attributes: []api.Attribute{{Name: "fib", Value: "yes"}}},
		"display-index": {Resource: api.FilterRule, Action: api.Set, ID: "1", Attributes: []api.Attribute{{Name: "rule", Value: "reject;"}}},
		"selector":      {Resource: api.BGPConnection, Action: api.Enable, ID: "[find]"},
		"unknown-field": {Resource: api.BGPConnection, Action: api.Set, ID: "*A", Attributes: []api.Attribute{{Name: "on-event", Value: "anything"}}},
		"control":       {Resource: api.FilterRule, Action: api.Set, ID: "*A", Attributes: []api.Attribute{{Name: "rule", Value: "x" + string(rune(27)) + "y"}}},
		"newline":       {Resource: api.FilterRule, Action: api.Set, ID: "*A", Attributes: []api.Attribute{{Name: "rule", Value: "x" + string(rune(10)) + "y"}}},
		"prepend":       {Resource: api.BGPConnection, Action: api.Set, ID: "*A", Attributes: []api.Attribute{{Name: "output.default-prepend", Value: "1"}}},
		"unset-secret":  {Resource: api.BGPConnection, Action: api.Unset, ID: "*A", Property: "tcp-md5-key"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := encodeOwned(bad); err == nil {
				t.Fatal("arbitrary command surface accepted")
			}
		})
	}
}
func TestOwnedMutationRequiresCurrentOwnerAndIndependentCompletion(t *testing.T) {
	c, _ := newFixture(t, nil)
	if _, err := c.ExecuteOwned(context.Background(), ownedRule()); err == nil {
		t.Fatal("unowned command sent")
	}
	if _, err := c.EnterSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := c.ExecuteOwned(context.Background(), ownedRule())
	if err != nil || !result.Completed || result.Evidence.Owner != OwnerThisSession || result.Evidence.RollbackVerified || result.Evidence.NativeValidated {
		t.Fatal("owned completion evidence invalid", err)
	}
}
func TestOwnedMutationNeverReturnsEchoedSecretsOrUnknownSuccess(t *testing.T) {
	for _, mode := range []string{"echo-only", "end-only", "double-ok", "normal-prompt", "post-owner-unknown", "fail", "raw-error"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := newFixture(t, func(f *fixture) { f.ownedMode = mode })
			if _, err := c.EnterSafeMode(context.Background()); err != nil {
				t.Fatal(err)
			}
			m := api.Mutation{Resource: api.BGPConnection, Action: api.Set, ID: "*1", Attributes: []api.Attribute{{Name: "tcp-md5-key", Value: fixtureSecret, Sensitive: true}}}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			result, err := c.ExecuteOwned(ctx, m)
			if err == nil || result.Completed {
				t.Fatal("ambiguous terminal output accepted")
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(err.Error(), fixtureSecret) || strings.Contains(string(encoded), fixtureSecret) {
				t.Fatal("echo/error leaked secret")
			}
		})
	}
}
func TestOwnedMutationAndCancellationShareOneOrderedWriter(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	c, f := newFixture(t, func(f *fixture) { f.ownedEntered = entered; f.ownedRelease = release })
	if _, err := c.EnterSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() { _, err := c.ExecuteOwned(context.Background(), ownedRule()); writeDone <- err }()
	<-entered
	cancelDone := make(chan error, 1)
	go func() { _, err := c.CancelSafeMode(context.Background()); cancelDone <- err }()
	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	controls := append([]byte(nil), f.controls...)
	f.mu.Unlock()
	if len(controls) != 1 || controls[0] != 0x18 {
		t.Fatal("cancel overtook an in-flight owned command")
	}
	once.Do(func() { close(release) })
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	controls = append([]byte(nil), f.controls...)
	f.mu.Unlock()
	if len(controls) != 2 || controls[1] != 0x04 {
		t.Fatal("ordered cancellation was duplicated or replaced")
	}
}

func TestOwnedCommandTimeoutNeverRetriesOrCreatesNewOwner(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	c, f := newFixture(t, func(f *fixture) { f.ownedEntered = entered; f.ownedRelease = release })
	if _, err := c.EnterSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err := c.ExecuteOwned(ctx, ownedRule())
	if err == nil || result.Completed || !c.Snapshot().Closed {
		t.Fatal("timeout did not close original owner stream")
	}
	if _, err = c.ExecuteOwned(context.Background(), ownedRule()); err == nil {
		t.Fatal("unknown mutation retried on closed owner")
	}
	once.Do(func() { close(release) })
	f.mu.Lock()
	commands := append([]string(nil), f.commands...)
	controls := append([]byte(nil), f.controls...)
	f.mu.Unlock()
	count := 0
	for _, command := range commands {
		if strings.Contains(command, ":do { /routing/") {
			count++
		}
	}
	if count != 1 || len(controls) != 1 || controls[0] != 0x18 {
		t.Fatal("timeout replayed command or reacquired owner")
	}
}

type ownedPartialWriter struct {
	net.Conn
	started bool
	closed  chan struct{}
	once    sync.Once
}

func (w *ownedPartialWriter) Write(data []byte) (int, error) {
	if w.started {
		<-w.closed
		return 0, io.ErrClosedPipe
	}
	if strings.Contains(string(data), ":do { /routing/") {
		w.started = true
		return w.Conn.Write(data[:20])
	}
	return w.Conn.Write(data)
}
func (w *ownedPartialWriter) Close() error {
	w.once.Do(func() { close(w.closed); _ = w.Conn.Close() })
	return nil
}
func TestOwnedPartialWriteCancellationKeepsBufferPrivate(t *testing.T) {
	local, peer := net.Pipe()
	writer := &ownedPartialWriter{Conn: local, closed: make(chan struct{})}
	c, err := Open(writer, limits())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{peer: peer, ownerMenu: true, takenMessage: "Taking Safe Mode session... Success!", historyCount: "0", done: make(chan struct{})}
	go f.serve()
	defer func() { c.Close(); peer.Close(); <-f.done }()
	if _, err = c.EnterSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err := c.ExecuteOwned(ctx, ownedRule())
	if err == nil || result.Completed || !c.Snapshot().Closed {
		t.Fatal("partial line was reported complete")
	}
}
