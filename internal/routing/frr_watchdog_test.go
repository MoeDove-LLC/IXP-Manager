package routing

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type frrTestHost struct {
	opts                                 FRROptions
	old, new, live, failOnce, failAlways string
}

func shellLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func newFRRTestHost(t *testing.T) frrTestHost {
	t.Helper()
	dir := t.TempDir()
	r, p := fixture()
	old := renderFRR(r, p)
	p[0].MaxPrefixes = 200
	next := renderFRR(r, p)
	target := filepath.Join(dir, "bgpd.conf")
	live := filepath.Join(dir, "live.conf")
	once := filepath.Join(dir, "fail-once")
	always := filepath.Join(dir, "fail-always")
	for _, path := range []string{target, live} {
		if err := os.WriteFile(path, []byte(old), 0640); err != nil {
			t.Fatal(err)
		}
	}
	validator := executable(t, dir, "bgpd", "if [ \"$1\" = --version ]; then echo 'bgpd version 10.3'; exit 0; fi\ncase \"$*\" in *--dryrun*--bgp_port*0*--no_zebra*--no_kernel*) exit 0;; *) exit 1;; esac\n")
	reload := executable(t, dir, "frr-reload.py", "live="+shellLiteral(live)+"\nonce="+shellLiteral(once)+"\nalways="+shellLiteral(always)+"\nmode=\"$1\"\nfor file do :; done\nif [ \"$mode\" = --test ]; then cmp -s \"$file\" \"$live\" || echo 'Lines To Add'; exit 0; fi\nif [ -f \"$always\" ]; then echo 'ERROR reload unavailable'; exit 1; fi\ncp \"$file\" \"$live\"\nif [ -f \"$once\" ]; then rm \"$once\"; echo 'ERROR partial reload'; exit 1; fi\n")
	vtysh := executable(t, dir, "vtysh", "exit 0\n")
	return frrTestHost{FRROptions{Target: target, StateDir: filepath.Join(dir, "state"), Validator: validator, Reload: reload, Vtysh: vtysh, VtySocket: dir}, old, next, live, once, always}
}
func (h frrTestHost) request() FRRStageRequest {
	return FRRStageRequest{Config: h.new, Checksum: Checksum(h.new), BaseChecksum: Checksum(h.old), TimeoutSeconds: 120}
}
func openTestWatchdog(t *testing.T, h frrTestHost) *FRRWatchdog {
	t.Helper()
	w, err := OpenFRRWatchdog(context.Background(), h.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}
func assertFile(t *testing.T, path, expected string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != expected {
		t.Fatalf("wrong file %s: err=%v", path, err)
	}
}

func TestFRRWatchdogStageConfirmAndScope(t *testing.T) {
	h := newFRRTestHost(t)
	w := openTestWatchdog(t, h)
	s, err := w.Stage(context.Background(), h.request())
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != "pending" || s.ID == "" {
		t.Fatal("stage failed to create pending transaction")
	}
	assertFile(t, h.opts.Target, h.new)
	assertFile(t, h.live, h.new)
	if _, err = w.Confirm(context.Background(), FRRDecision{ID: s.ID, Checksum: Checksum(h.old)}); err == nil {
		t.Fatal("wrong reviewed checksum confirmed")
	}
	if _, err = w.Stage(context.Background(), h.request()); err == nil {
		t.Fatal("overwrote a pending revision")
	}
	final, err := w.Confirm(context.Background(), FRRDecision{ID: s.ID, Checksum: s.Checksum})
	if err != nil || final.Status != "confirmed" {
		t.Fatalf("confirm: %v", err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	assertFile(t, h.live, h.new)
	journal, err := os.Stat(filepath.Join(h.opts.StateDir, "transaction.json"))
	if err != nil || journal.Mode().Perm() != 0600 {
		t.Fatal("journal is not private")
	}
	if _, err = OpenFRRWatchdog(context.Background(), FRROptions{Target: filepath.Join(filepath.Dir(h.opts.Target), "frr.conf"), StateDir: h.opts.StateDir}); err == nil {
		t.Fatal("integrated FRR target accepted")
	}
}
func TestFRRWatchdogRejectsScopeChecksumAndDrift(t *testing.T) {
	h := newFRRTestHost(t)
	w := openTestWatchdog(t, h)
	for _, mutate := range []func(*FRRStageRequest){func(r *FRRStageRequest) { r.Checksum = Checksum("other") }, func(r *FRRStageRequest) { r.BaseChecksum = Checksum("stale") }, func(r *FRRStageRequest) { r.Config += "router ospf\n"; r.Checksum = Checksum(r.Config) }, func(r *FRRStageRequest) { r.TimeoutSeconds = 0 }} {
		req := h.request()
		mutate(&req)
		if _, err := w.Stage(context.Background(), req); err == nil {
			t.Fatal("unsafe request accepted")
		}
		assertFile(t, h.live, h.old)
		assertFile(t, h.opts.Target, h.old)
	}
	if err := os.WriteFile(h.live, []byte(h.old+"unmanaged live router\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage(context.Background(), h.request()); err == nil {
		t.Fatal("live drift silently overwritten")
	}
	assertFile(t, h.opts.Target, h.old)
}
func TestFRRWatchdogFailureRollback(t *testing.T) {
	h := newFRRTestHost(t)
	w := openTestWatchdog(t, h)
	if err := os.WriteFile(h.failOnce, nil, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := w.Stage(context.Background(), h.request())
	if err == nil || s.Status != "failed" {
		t.Fatalf("expected failed reload with restored config, got %s %v", s.Status, err)
	}
	assertFile(t, h.live, h.old)
	assertFile(t, h.opts.Target, h.old)
}
func TestFRRWatchdogTimeoutAndCancel(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancel], func(t *testing.T) {
			h := newFRRTestHost(t)
			w := openTestWatchdog(t, h)
			s, err := w.Stage(context.Background(), h.request())
			if err != nil {
				t.Fatal(err)
			}
			if cancel {
				s, err = w.Cancel(FRRDecision{s.ID, s.Checksum})
				if err != nil || s.Status != "cancelled" {
					t.Fatal(err)
				}
			} else {
				w.now = func() time.Time { return s.Deadline.Add(time.Second) }
				if _, err = w.Confirm(context.Background(), FRRDecision{s.ID, s.Checksum}); err == nil {
					t.Fatal("late confirmation accepted")
				}
				if w.Status().Status != "expired" {
					t.Fatal("expired transaction not reverted")
				}
			}
			assertFile(t, h.live, h.old)
			assertFile(t, h.opts.Target, h.old)
		})
	}
}
func TestFRRWatchdogCrashRecovery(t *testing.T) {
	h := newFRRTestHost(t)
	w := openTestWatchdog(t, h)
	if _, err := w.Stage(context.Background(), h.request()); err != nil {
		t.Fatal(err)
	}
	// Simulate OS releasing the advisory lock on SIGKILL, without graceful Close.
	w.unlock()
	w.closed = true
	recovered := openTestWatchdog(t, h)
	if recovered.Status().Status != "recovered" {
		t.Fatal("startup did not reconcile journal")
	}
	assertFile(t, h.live, h.old)
	assertFile(t, h.opts.Target, h.old)
}
func TestFRRWatchdogRetryFailedRollback(t *testing.T) {
	h := newFRRTestHost(t)
	w := openTestWatchdog(t, h)
	s, err := w.Stage(context.Background(), h.request())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(h.failAlways, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err = w.Cancel(FRRDecision{s.ID, s.Checksum}); err == nil || s.Status != "rollback-failed" {
		t.Fatal("lost rollback failure journal")
	}
	if err = os.Remove(h.failAlways); err != nil {
		t.Fatal(err)
	}
	if err = w.Tick(); err != nil {
		t.Fatal(err)
	}
	assertFile(t, h.live, h.old)
}
func TestFRRWatchdogConcurrentStage(t *testing.T) {
	h := newFRRTestHost(t)
	w := openTestWatchdog(t, h)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() { defer group.Done(); _, err := w.Stage(context.Background(), h.request()); results <- err }()
	}
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent stages succeeded %d times", successes)
	}
}
func TestFRRWatchdogRecoveryTamper(t *testing.T) {
	h := newFRRTestHost(t)
	w := openTestWatchdog(t, h)
	if _, err := w.Stage(context.Background(), h.request()); err != nil {
		t.Fatal(err)
	}
	w.unlock()
	w.closed = true
	if err := os.WriteFile(filepath.Join(h.opts.StateDir, "previous.conf"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFRRWatchdog(context.Background(), h.opts); err == nil {
		t.Fatal("unverified recovery snapshot applied")
	}
	b, _ := os.ReadFile(filepath.Join(h.opts.StateDir, "transaction.json"))
	var tx FRRTransaction
	_ = json.Unmarshal(b, &tx)
	if tx.Status != "rollback-failed" {
		t.Fatal("recovery failure was not durable")
	}
}
func TestFRRBootstrapIsExplicitAndOffline(t *testing.T) {
	h := newFRRTestHost(t)
	unmanaged := "! existing operator config\n"
	if err := os.WriteFile(h.opts.Target, []byte(unmanaged), 0640); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapFRR(context.Background(), h.opts.Target, h.opts.Validator, h.new, Checksum(h.new), Checksum(unmanaged)); err != nil {
		t.Fatal(err)
	}
	assertFile(t, h.opts.Target, h.new)
	assertFile(t, h.opts.Target+".moedove-bootstrap-original", unmanaged)
	assertFile(t, h.live, h.old)
	if err := BootstrapFRR(context.Background(), h.opts.Target, h.opts.Validator, h.old, Checksum(h.old), Checksum(h.new)); err == nil {
		t.Fatal("bootstrap overwrote an already managed target")
	}
}

func TestFRRWatchdogStateDirectoryBinding(t *testing.T) {
	h := newFRRTestHost(t)
	w := openTestWatchdog(t, h)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	h.opts.StateDir = filepath.Join(filepath.Dir(h.opts.StateDir), "lost-journal")
	if _, err := OpenFRRWatchdog(context.Background(), h.opts); err == nil {
		t.Fatal("allowed relocating target away from its recovery journal")
	}
}

func TestNativeCommandCancellationKillsChildren(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "orphan-wrote")
	helper := executable(t, dir, "helper", "(sleep 0.5; echo survived > "+shellLiteral(marker)+") &\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := run(ctx, helper); err == nil {
		t.Fatal("cancelled helper reported success")
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("orphan helper child survived and performed a late write")
	}
}

// This exercises the actual FRR reload planner, using --input so it never
// connects to a running daemon or executes configuration commands.
func TestNativeFRRReloadPlanIntegration(t *testing.T) {
	helper := os.Getenv("FRR_RELOAD_BIN")
	vtysh := os.Getenv("VTYSH_BIN")
	if helper == "" || vtysh == "" {
		t.Skip("set FRR_RELOAD_BIN and VTYSH_BIN for the offline native reload planner")
	}
	dir := t.TempDir()
	r, p := fixture()
	old := renderFRR(r, p)
	base := filepath.Join(dir, "base.conf")
	candidate := filepath.Join(dir, "candidate.conf")
	if err := os.WriteFile(base, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--test", "--input", base, "--daemon", "bgpd", "--bindir", filepath.Dir(vtysh), "--confdir", dir, "--rundir", dir, "--vty_socket", dir, "--stdout", "--log-level", "error", candidate}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := run(ctx, helper, args...)
	if err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("same profile produced a delta: %v: %s", err, out)
	}
	p[0].MaxPrefixes++
	if err = os.WriteFile(candidate, []byte(renderFRR(r, p)), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = run(ctx, helper, args...)
	if err != nil || !strings.Contains(out, "Lines To Add") || !strings.Contains(out, "maximum-prefix") {
		t.Fatalf("changed profile produced no meaningful diff: %v: %s", err, out)
	}
}
