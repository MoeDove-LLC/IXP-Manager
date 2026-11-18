package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

func TestQuaggaWatchdogCLIRejectsRetargetingFlagsAndUnboundInputs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "candidate.conf")
	if err := os.WriteFile(file, []byte("candidate"), 0644); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "agent.sock")
	base := routing.Checksum("base")
	sum := routing.Checksum("candidate")
	stage := []string{"stage", "--platform", "quagga", "--socket", socket, "--file", file, "--checksum", sum, "--base-checksum", base}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{append(append([]string(nil), stage...), "--target", "/etc/quagga/bgpd.conf"), "not a Quagga stage option"},
		{append(append([]string(nil), stage...), "--timeout", "59"), "timeout 60..3600"},
		{stage, "mode 0600"},
		{[]string{"confirm", "--platform", "quagga", "--socket", socket, "--transaction", "from-other-platform", "--checksum", sum}, "exact --transaction"},
		{[]string{"status", "--platform", "quagga", "--socket", socket, "--validator", "/usr/bin/true"}, "not a Quagga status option"},
		{[]string{"serve", "--platform", "quagga", "--socket", socket, "--validator", "/usr/bin/true", "--binary-checksum", sum, "--bgp-port", "1179", "--exclusive-instance-acknowledged"}, "requires --state-dir"},
		{[]string{"check", "--platform", "bird2", "--bgp-port", "1179"}, "startup settings require serve"},
	} {
		if err := run(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("expected %q, got %v", tc.want, err)
		}
	}
}

// Requires the cap0 isolated fixture with the stock executable built for
// /scratch/state/bgpd.vty. The package-level lifecycle fixture exercises peers.
func TestQuaggaNativeAgentTransactions(t *testing.T) {
	if os.Getenv("QUAGGA_AGENT_LIVE_TEST") != "1" {
		t.Skip("requires the isolated native Quagga CLI fixture")
	}
	agent, binary := os.Getenv("QUAGGA_AGENT_BIN"), os.Getenv("QUAGGA_BGPD_BIN")
	if agent != "/scratch/bin/ixp-agent" || binary != "/native/bgpd" {
		t.Fatal("native Quagga fixture requires the pinned executable paths")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !strings.Contains(string(status), "CapEff:\t0000000000000000") || !strings.Contains(string(status), "CapBnd:\t0000000000000000") || !strings.Contains(string(status), "NoNewPrivs:\t1") {
		t.Fatal("native Quagga CLI fixture requires zero capabilities and no-new-privs")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" {
		t.Fatal("native Quagga CLI fixture requires an isolated loopback-only namespace")
	}
	const stateDir = "/scratch/state"
	if err := os.Chmod(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(stateDir, "agent.sock")
	configFile := func(id string) (string, string) {
		t.Helper()
		config, err := routing.RenderFor("quagga", routing.Router{ASN: 64500, RouterID: id, LocalIPv4: "192.0.2.1"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(stateDir, id+".conf")
		if err = os.WriteFile(file, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		return file, routing.Checksum(config)
	}
	oldFile, old := configFile("192.0.2.1")
	nextFile, next := configFile("192.0.2.2")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	args := func(mode string, flags ...string) []string {
		return append([]string{mode, "--platform", "quagga", "--socket", socket}, flags...)
	}
	runAgent := func(call context.Context, mode string, flags ...string) (routing.QuaggaTransaction, error) {
		t.Helper()
		cmd := exec.CommandContext(call, agent, args(mode, flags...)...)
		out, err := cmd.Output()
		var state routing.QuaggaTransaction
		if decodeErr := json.Unmarshal(out, &state); decodeErr != nil && err == nil {
			return state, decodeErr
		}
		return state, err
	}
	start := func(bootstrap bool) func() {
		t.Helper()
		flags := []string{"--state-dir", stateDir, "--validator", binary, "--binary-checksum", "9f758d79ce302c188b42d252ebd3b51044f803767db55c048574e8d4b60eaf67", "--bgp-port", "1179", "--exclusive-instance-acknowledged"}
		if bootstrap {
			flags = append(flags, "--bootstrap-file", oldFile, "--bootstrap-checksum", old)
		}
		var diagnostic strings.Builder
		cmd := exec.CommandContext(ctx, agent, args("serve", flags...)...)
		cmd.Stderr = &diagnostic
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			t.Helper()
			if stopped {
				return
			}
			stopped = true
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Error("native CLI signal failed", err)
			}
			if err := <-done; err != nil {
				t.Error("native CLI shutdown failed", err, diagnostic.String())
			}
		}
		t.Cleanup(stop)
		for {
			call, end := context.WithTimeout(ctx, 2*time.Second)
			_, err := runAgent(call, "status")
			end()
			if err == nil {
				return stop
			}
			select {
			case err := <-done:
				stopped = true
				t.Fatal("native CLI serve failed", err, diagnostic.String())
			case <-ctx.Done():
				t.Fatal("native CLI serve did not become ready", ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	stop := start(true)
	if state, err := runAgent(ctx, "status"); err != nil || state.Status != "idle" || state.ActiveChecksum != old {
		t.Fatal("native initial status mismatch", state, err)
	}
	staged, err := runAgent(ctx, "stage", "--file", nextFile, "--checksum", next, "--base-checksum", old, "--timeout", "90")
	if err != nil || staged.Status != "pending" || staged.ActiveChecksum != old || staged.Checksum != next {
		t.Fatal("native CLI failed to stage exactly the candidate", staged, err)
	}
	if state, err := runAgent(ctx, "confirm", "--transaction", staged.ID, "--checksum", old); err == nil || state.Status != "pending" {
		t.Fatal("wrong confirmation changed native Quagga", state, err)
	}
	if state, err := runAgent(ctx, "confirm", "--transaction", staged.ID, "--checksum", next); err != nil || state.Status != "confirmed" || state.ActiveChecksum != next {
		t.Fatal("native CLI did not confirm exact candidate", state, err)
	}
	staged, err = runAgent(ctx, "stage", "--file", oldFile, "--checksum", old, "--base-checksum", next, "--timeout", "90")
	if err != nil || staged.Status != "pending" || staged.ActiveChecksum != next {
		t.Fatal("native CLI failed to stage a different candidate", staged, err)
	}
	if state, err := runAgent(ctx, "cancel", "--transaction", staged.ID, "--checksum", old); err != nil || state.Status != "cancelled" || state.ActiveChecksum != next {
		t.Fatal("native CLI did not restore the confirmed version", state, err)
	}
	stop()
	stop = start(false)
	if state, err := runAgent(ctx, "status"); err != nil || state.ActiveChecksum != next || state.Status != "cancelled" {
		t.Fatal("native CLI did not reopen the confirmed version", state, err)
	}
	stop()
	t.Log("quagga_agent_native=true private_socket=true stage_confirm_cancel=true committed_reopen=true")
}
