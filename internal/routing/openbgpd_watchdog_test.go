package routing

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type openBGPDTestHost struct {
	opts           OpenBGPDOptions
	old, new, live string
	failReload     bool
	resets         [][]string
}

func newOpenBGPDTestHost(t *testing.T) *openBGPDTestHost {
	t.Helper()
	dir := t.TempDir()
	r, p := openBGPDTestInput()
	old, err := RenderOpenBGPD(r, p)
	if err != nil {
		t.Fatal(err)
	}
	p[0].Password = "rotatedSecret_+/="
	p[0].AuthenticationRevision = "abcdefghijklmnopqrstuv"
	next, err := RenderOpenBGPD(r, p)
	if err != nil {
		t.Fatal(err)
	}
	h := &openBGPDTestHost{opts: OpenBGPDOptions{Target: filepath.Join(dir, "bgpd.conf"), StateDir: filepath.Join(dir, "journal"), Validator: filepath.Join(dir, "bgpd"), BGPCTL: filepath.Join(dir, "bgpctl"), ExclusiveInstance: true, DaemonPID: 123}, old: old, new: next, live: filepath.Join(dir, "fake-live")}
	if err = os.WriteFile(h.opts.Target, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(h.live, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	return h
}
func (h *openBGPDTestHost) host() openBGPDHost {
	return openBGPDHost{verify: func() error { return nil }, validate: func(_ context.Context, config string) error { return ValidateManagedOpenBGPD(config) }, reload: func(_ context.Context, reset []string) error {
		h.resets = append(h.resets, append([]string(nil), reset...))
		if h.failReload {
			h.failReload = false
			return errors.New("fake reload rejection")
		}
		raw, err := os.ReadFile(h.opts.Target)
		if err != nil {
			return err
		}
		return os.WriteFile(h.live, raw, 0600)
	}}
}
func (h *openBGPDTestHost) request() OpenBGPDStageRequest {
	return OpenBGPDStageRequest{Config: h.new, Checksum: Checksum(h.new), BaseChecksum: Checksum(h.old), TimeoutSeconds: 120}
}
func (h *openBGPDTestHost) open(t *testing.T) *OpenBGPDWatchdog {
	t.Helper()
	w, err := openOpenBGPDWatchdog(context.Background(), h.opts, h.host())
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func assertOpenBGPDFile(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatal("unexpected file contents", path, err)
	}
}
func TestOpenBGPDWatchdogStageConfirmCancelAndDeadline(t *testing.T) {
	for _, decision := range []string{"confirm", "cancel", "expired", "close"} {
		t.Run(decision, func(t *testing.T) {
			h := newOpenBGPDTestHost(t)
			w := h.open(t)
			defer w.Close()
			state, err := w.Stage(context.Background(), h.request())
			if err != nil || state.Status != "pending" {
				t.Fatal(state, err)
			}
			assertOpenBGPDFile(t, h.live, h.new)
			if len(h.resets) != 1 || len(h.resets[0]) != 1 || h.resets[0][0] != "192.0.2.2" {
				t.Fatal("changed MD5 session not reset")
			}
			if _, err := w.Confirm(context.Background(), OpenBGPDDecision{ID: "wrong", Checksum: state.Checksum}); err == nil {
				t.Fatal("wrong transaction confirmed")
			}
			d := OpenBGPDDecision{ID: state.ID, Checksum: state.Checksum}
			switch decision {
			case "confirm":
				state, err = w.Confirm(context.Background(), d)
			case "cancel":
				state, err = w.Cancel(d)
			case "expired":
				w.now = func() time.Time { return state.Deadline.Add(time.Second) }
				err = w.Tick()
				state = w.Status()
			case "close":
				err = w.Close()
				state = w.Status()
			}
			if err != nil {
				t.Fatal(err)
			}
			want := h.old
			if decision == "confirm" {
				want = h.new
			}
			assertOpenBGPDFile(t, h.live, want)
			assertOpenBGPDFile(t, h.opts.Target, want)
			journal, _ := os.ReadFile(filepath.Join(h.opts.StateDir, "transaction.json"))
			if strings.Contains(string(journal), "Secret") || strings.Contains(string(journal), "tcp md5sig") {
				t.Fatal("secret in journal metadata")
			}
		})
	}
}
func TestOpenBGPDWatchdogFailuresAndExclusiveLock(t *testing.T) {
	h := newOpenBGPDTestHost(t)
	w := h.open(t)
	defer w.Close()
	if _, err := openOpenBGPDWatchdog(context.Background(), h.opts, h.host()); err == nil {
		t.Fatal("second target owner accepted")
	}
	bad := h.request()
	bad.BaseChecksum = strings.Repeat("0", 64)
	if _, err := w.Stage(context.Background(), bad); err == nil {
		t.Fatal("wrong base checksum accepted")
	}
	bad = h.request()
	bad.Config = strings.Replace(bad.Config, "fib-update no", "fib-update yes", 1)
	bad.Checksum = Checksum(bad.Config)
	if _, err := w.Stage(context.Background(), bad); err == nil {
		t.Fatal("arbitrary native source accepted")
	}
	h.failReload = true
	state, err := w.Stage(context.Background(), h.request())
	if err == nil || state.Status != "failed" {
		t.Fatal(state, err)
	}
	assertOpenBGPDFile(t, h.live, h.old)
	state, err = w.Stage(context.Background(), h.request())
	if err != nil {
		t.Fatal(err)
	}
	// Unrelated third-party disk writes must not be destroyed by rollback.
	if err = os.WriteFile(h.opts.Target, []byte("unrelated outside content"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Confirm(context.Background(), OpenBGPDDecision{state.ID, state.Checksum}); err == nil {
		t.Fatal("drift confirmed")
	}
	if _, err = w.Cancel(OpenBGPDDecision{state.ID, state.Checksum}); err == nil || w.Status().Status != "rollback-failed" {
		t.Fatal("outside drift overwritten")
	}
	assertOpenBGPDFile(t, h.opts.Target, "unrelated outside content")
	if err = os.WriteFile(h.opts.Target, []byte(h.new), 0600); err != nil {
		t.Fatal(err)
	}
	if err = w.Tick(); err != nil {
		t.Fatal(err)
	}
	assertOpenBGPDFile(t, h.live, h.old)
}
func TestOpenBGPDConcurrentStageSingleWinner(t *testing.T) {
	h := newOpenBGPDTestHost(t)
	w := h.open(t)
	defer w.Close()
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := w.Stage(context.Background(), h.request()); errs <- err }()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatal("stage not serialized")
	}
}
func TestOpenBGPDWatchdogCrashHelper(t *testing.T) {
	raw := os.Getenv("MOEDOVE_TEST_OPENBGPD_CRASH")
	if raw == "" {
		t.Skip("subprocess-only fixture")
	}
	var opts OpenBGPDOptions
	if err := json.Unmarshal([]byte(raw), &opts); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(opts.Target)
	if err != nil {
		t.Fatal(err)
	}
	next, err := os.ReadFile(filepath.Join(filepath.Dir(opts.Target), "fixture-candidate"))
	if err != nil {
		t.Fatal(err)
	}
	h := &openBGPDTestHost{opts: opts, old: string(old), new: string(next), live: filepath.Join(filepath.Dir(opts.Target), "fake-live")}
	w := h.open(t)
	if _, err = w.Stage(context.Background(), h.request()); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(opts.StateDir, "fixture-ready"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	select {}
}
func TestOpenBGPDKilledWatchdogRecoversBeforeReopening(t *testing.T) {
	h := newOpenBGPDTestHost(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(h.opts.Target), "fixture-candidate"), []byte(h.new), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(h.opts)
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenBGPDWatchdogCrashHelper$")
	cmd.Env = append(os.Environ(), "MOEDOVE_TEST_OPENBGPD_CRASH="+string(raw))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(h.opts.StateDir, "fixture-ready")); err == nil {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("fixture failed to stage")
	}
	assertOpenBGPDFile(t, h.live, h.new)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	w := h.open(t)
	defer w.Close()
	if w.Status().Status != "recovered" {
		t.Fatal(w.Status())
	}
	assertOpenBGPDFile(t, h.live, h.old)
	assertOpenBGPDFile(t, h.opts.Target, h.old)
}
