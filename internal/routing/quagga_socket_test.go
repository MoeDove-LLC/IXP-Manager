package routing

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type quaggaSocketFixture struct {
	mu     sync.Mutex
	state  QuaggaTransaction
	stages int
	closed bool
}

func (w *quaggaSocketFixture) Status() QuaggaTransaction {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

func (w *quaggaSocketFixture) Stage(ctx context.Context, request QuaggaStageRequest) (QuaggaTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return w.state, err
	}
	if request.BaseChecksum != w.state.ActiveChecksum || Checksum(request.Config) != request.Checksum || request.TimeoutSeconds != 90 {
		return w.state, errors.New("invalid Quagga stage fixture")
	}
	w.stages++
	w.state = QuaggaTransaction{Version: 1, ID: strings.Repeat("c", 48), Status: "pending", ActiveChecksum: request.BaseChecksum, BaseChecksum: request.BaseChecksum, Checksum: request.Checksum, Deadline: time.Now().Add(90 * time.Second)}
	return w.state, nil
}

func (w *quaggaSocketFixture) Confirm(_ context.Context, decision QuaggaDecision) (QuaggaTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Status != "pending" || decision.ID != w.state.ID || decision.Checksum != w.state.Checksum {
		return w.state, errors.New("invalid Quagga decision fixture")
	}
	w.state.Status, w.state.ActiveChecksum = "confirmed", decision.Checksum
	return w.state, nil
}

func (w *quaggaSocketFixture) Cancel(decision QuaggaDecision) (QuaggaTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Status != "pending" || decision.ID != w.state.ID || decision.Checksum != w.state.Checksum {
		return w.state, errors.New("invalid Quagga decision fixture")
	}
	w.state.Status = "cancelled"
	return w.state, nil
}

func (*quaggaSocketFixture) Tick() error { return nil }

func (w *quaggaSocketFixture) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func shortQuaggaSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ixp-q-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestQuaggaSocketTypedRequestsAndExactDecisions(t *testing.T) {
	socket := filepath.Join(shortQuaggaSocketDir(t), "agent.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err = errors.Join(os.Chmod(socket, 0600), stale.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err := quaggaControlSocketInfo(socket); err != nil {
		t.Fatal("no private stale socket to exercise reopening", err)
	}
	old := Checksum("old")
	w := &quaggaSocketFixture{state: QuaggaTransaction{Version: 1, Status: "idle", ActiveChecksum: old}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveQuaggaSocket(ctx, socket, func() (quaggaSocketWatchdog, error) { return w, nil }) }()
	stopped := false
	defer func() {
		if !stopped {
			cancel()
			<-done
		}
	}()
	for {
		status, err := RequestQuagga(ctx, socket, "status", nil)
		if err == nil {
			if status.Status != "idle" || status.ActiveChecksum != old {
				t.Fatal("unexpected initial status", status)
			}
			break
		}
		select {
		case serveErr := <-done:
			stopped = true
			t.Fatal("private socket exited before startup", serveErr)
		case <-ctx.Done():
			t.Fatal("private socket did not start", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := RequestQuagga(ctx, socket, "status", QuaggaDecision{}); err == nil {
		t.Fatal("status accepted a decision payload")
	}
	if _, err := RequestQuagga(ctx, socket, "stage", QuaggaDecision{}); err == nil {
		t.Fatal("stage accepted a different request type")
	}
	for _, action := range []string{"cancel", "confirm"} {
		candidate := "candidate for " + action + "\nsecond line\n"
		sum := Checksum(candidate)
		staged, err := RequestQuagga(ctx, socket, "stage", QuaggaStageRequest{Config: candidate, Checksum: sum, BaseChecksum: old, TimeoutSeconds: 90})
		if err != nil || staged.Status != "pending" || staged.ActiveChecksum != old || staged.Checksum != sum {
			t.Fatal("stage did not preserve the exact signed request and committed hash", staged, err)
		}
		if state, err := RequestQuagga(ctx, socket, action, QuaggaDecision{ID: staged.ID, Checksum: old}); err == nil || state.Status != "pending" {
			t.Fatal("wrong decision changed pending state", state, err)
		}
		result, err := RequestQuagga(ctx, socket, action, QuaggaDecision{ID: staged.ID, Checksum: sum})
		if err != nil || (action == "confirm" && (result.Status != "confirmed" || result.ActiveChecksum != sum)) || (action == "cancel" && (result.Status != "cancelled" || result.ActiveChecksum != old)) {
			t.Fatal("decision did not act on the requested transaction", result, err)
		}
	}
	cancel()
	err = <-done
	stopped = true
	if err != nil || !w.closed {
		t.Fatal("watchdog shutdown failed", err)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatal("watchdog left a live socket", err)
	}
}

func TestQuaggaSocketRejectsAmbiguousAndNonlocalCommands(t *testing.T) {
	base := Checksum("old")
	config := "fixture"
	sum := Checksum(config)
	w := &quaggaSocketFixture{state: QuaggaTransaction{Version: 1, Status: "idle", ActiveChecksum: base}}
	handler := quaggaWatchdogHandler(context.Background(), w)
	body := `{"config":"` + config + `","checksum":"` + sum + `","baseChecksum":"` + base + `","timeoutSeconds":90}`
	for _, tc := range []struct{ url, body string }{
		{"/stage", body + `{}`},
		{"/stage", strings.Replace(body, `"config":`, `"config":"wrong","config":`, 1)},
		{"/stage", strings.Replace(body, `"checksum":`, `"Checksum":`, 1)},
		{"/stage", strings.Replace(body, `"`, `"target":"another", "`, 1)},
		{"/stage", strings.Replace(body, `:90`, `:null`, 1)},
		{"/stage", strings.Replace(body, `:90`, `:9e1`, 1)},
		{"/stage?target=another", body},
		{"/stage/", body},
		{"/cancel", strings.Repeat(" ", 2048) + `{"id":"a","checksum":"b"}`},
		{"/cancel", `{"id":"a"}`},
	} {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, tc.url, strings.NewReader(tc.body)))
		if res.Code < 400 || w.stages != 0 {
			t.Fatalf("ambiguous or unsupported socket input changed the watchdog at %q: %d", tc.url, res.Code)
		}
	}
	clientCtx, disconnect := context.WithCancel(context.Background())
	disconnect()
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/stage", strings.NewReader(body)).WithContext(clientCtx))
	if res.Code != http.StatusOK || w.stages != 1 {
		t.Fatal("decoded transaction used the cancelled HTTP client context")
	}
}

func TestQuaggaSocketConflictsPreventDaemonStartup(t *testing.T) {
	dir := shortQuaggaSocketDir(t)
	socket := filepath.Join(dir, "agent.sock")
	open := func() (quaggaSocketWatchdog, error) {
		t.Fatal("socket conflict allowed daemon startup")
		return nil, nil
	}
	if err := os.WriteFile(socket, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := serveQuaggaSocket(context.Background(), socket, open); err == nil {
		t.Fatal("regular file socket conflict accepted")
	}
	if raw, err := os.ReadFile(socket); err != nil || string(raw) != "keep" {
		t.Fatal("regular file socket conflict was changed", err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	if err := serveQuaggaSocket(context.Background(), socket, open); err == nil {
		t.Fatal("another listener allowed daemon startup")
	}
	info, err := quaggaControlSocketInfo(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("another listener was affected", err)
	}
}
