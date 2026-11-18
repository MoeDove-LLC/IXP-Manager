package routing

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFRRWatchdogChildProcess(t *testing.T) {
	raw := os.Getenv("MOEDOVE_TEST_FRR_WATCHDOG")
	if raw == "" {
		t.Skip("subprocess-only helper")
	}
	var opts FRROptions
	if err := json.Unmarshal([]byte(raw), &opts); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	if err := ServeFRRWatchdog(ctx, opts, os.Getenv("MOEDOVE_TEST_FRR_SOCKET")); err != nil {
		t.Fatal(err)
	}
}
func TestFRRWatchdogKilledProcessRecoversOnRestart(t *testing.T) {
	h := newFRRTestHost(t)
	socket := filepath.Join(filepath.Dir(h.opts.Target), "run", "process.sock")
	raw, _ := json.Marshal(h.opts)
	launch := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFRRWatchdogChildProcess$")
		cmd.Env = append(os.Environ(), "MOEDOVE_TEST_FRR_WATCHDOG="+string(raw), "MOEDOVE_TEST_FRR_SOCKET="+socket)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_, err := RequestFRR(ctx, socket, "status", nil)
			cancel()
			if err == nil {
				return cmd
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("watchdog subprocess did not become ready")
		return nil
	}
	first := launch()
	state, err := RequestFRR(context.Background(), socket, "stage", h.request())
	if err != nil || state.Status != "pending" {
		t.Fatal(err)
	}
	if err = first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	assertFile(t, h.live, h.new)
	// This explicit relaunch models the required external supervisor, not an
	// impossible promise that a dead process can execute its own rollback timer.
	second := launch()
	state, err = RequestFRR(context.Background(), socket, "status", nil)
	if err != nil || state.Status != "recovered" {
		t.Fatalf("restart recovery: %s %v", state.Status, err)
	}
	assertFile(t, h.live, h.old)
	assertFile(t, h.opts.Target, h.old)
	if err = second.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err = second.Wait(); err != nil {
		t.Fatal(err)
	}
}
