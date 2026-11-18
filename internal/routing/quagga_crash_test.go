package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Exercise the real ixp-agent supervisor and stock bgpd in one isolated PID
// and network namespace. No test code sends a signal to bgpd after the crash.
func TestQuaggaNativeSupervisorSIGKILLRestoresConfirmedWirePolicy(t *testing.T) {
	quaggaWireGuard(t)
	agent, native := os.Getenv("QUAGGA_CRASH_AGENT_BIN"), os.Getenv("QUAGGA_BGPD_BIN")
	if agent != "/scratch/bin/ixp-agent" || native != "/native/bgpd" {
		t.Fatal("supervisor crash fixture requires its fixed sandbox executables")
	}
	const stateDir = "/scratch/state"
	if err := os.Chmod(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	base, candidate := quaggaLifecycleConfigs(t, false)
	seed, err := RenderQuagga(Router{ASN: 64500, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", LocalIPv6: "2001:db8::112"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(t.TempDir(), "reviewed-seed.conf")
	if err := os.WriteFile(seedPath, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	const binaryChecksum = "9f758d79ce302c188b42d252ebd3b51044f803767db55c048574e8d4b60eaf67"
	opts := QuaggaOptions{StateDir: stateDir, Binary: native, BinaryChecksum: binaryChecksum, BGPPort: 1179, ExclusiveInstance: true}
	host, err := newQuaggaHost(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.binary.Close() })
	kernel := quaggaWireKernel(t)
	socket := filepath.Join(stateDir, "agent.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	start := func(bootstrap bool) func(bool) {
		t.Helper()
		args := []string{"serve", "--platform", "quagga", "--socket", socket, "--state-dir", stateDir,
			"--validator", native, "--binary-checksum", binaryChecksum, "--bgp-port", "1179", "--exclusive-instance-acknowledged"}
		if bootstrap {
			args = append(args, "--bootstrap-file", seedPath, "--bootstrap-checksum", Checksum(seed))
		}
		cmd := exec.CommandContext(ctx, agent, args...)
		var diagnostic strings.Builder
		cmd.Stderr = &diagnostic
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		joined := false
		stop := func(crash bool) {
			t.Helper()
			if joined {
				return
			}
			joined = true
			sig := syscall.SIGTERM
			if crash {
				sig = syscall.SIGKILL
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Error("could not signal the exact supervisor process", err)
			}
			err := <-done
			if !crash && err == nil {
				return
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				if ws, ok := exit.Sys().(syscall.WaitStatus); crash && ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL {
					return
				}
			}
			t.Fatal("unexpected supervisor exit", err, diagnostic.String())
		}
		t.Cleanup(func() { stop(false) })
		until := time.Now().Add(20 * time.Second)
		for time.Now().Before(until) {
			ready, release := context.WithTimeout(ctx, time.Second)
			_, err := RequestQuagga(ready, socket, "status", nil)
			release()
			if err == nil {
				return stop
			}
			select {
			case err := <-done:
				joined = true
				t.Fatal("native supervisor exited during startup", err, diagnostic.String())
			case <-ctx.Done():
				t.Fatal("native supervisor context ended", ctx.Err())
			case <-time.After(40 * time.Millisecond):
			}
		}
		t.Fatal("native supervisor did not become ready")
		return nil
	}

	stage := func(config, old string) QuaggaTransaction {
		t.Helper()
		request := QuaggaStageRequest{Config: config, Checksum: Checksum(config), BaseChecksum: Checksum(old), TimeoutSeconds: 90}
		got, err := RequestQuagga(ctx, socket, "stage", request)
		if err != nil || got.Status != "pending" || got.Checksum != request.Checksum || got.ActiveChecksum != request.BaseChecksum {
			t.Fatal("native stage did not preserve the previously approved policy", got, err)
		}
		return got
	}
	observe := func() (quaggaProcessReceipt, string) {
		t.Helper()
		raw, err := quaggaPrivateRead(filepath.Join(stateDir, "process.json"), 4096)
		var receipt quaggaProcessReceipt
		if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.PID < 2 || receipt.BootID != host.bootID {
			t.Fatal("native child identity receipt unavailable", err)
		}
		generation, state, err := quaggaGeneration(receipt.PID)
		if err != nil || state == "Z" || generation != receipt.Generation {
			t.Fatal("native child identity does not match its live generation", err)
		}
		executable, exeErr := os.Stat(fmt.Sprintf("/proc/%d/exe", receipt.PID))
		pinned, pinErr := host.binary.Stat()
		listeners, listenErr := host.listeners()
		if exeErr != nil || pinErr != nil || listenErr != nil || !os.SameFile(executable, pinned) || !quaggaOwnedListeners(receipt.PID, listeners) {
			t.Fatal("recorded child does not own both BGP listeners under the real executable")
		}
		fingerprint, err := host.fingerprint(ctx, &quaggaChild{receipt: receipt})
		if err != nil {
			t.Fatal("live configuration proof unavailable", err)
		}
		return receipt, fingerprint
	}

	stop := start(true)
	first := stage(base, seed)
	peers := quaggaWatchdogConnect(t, base)
	peers.policy(7, 0, 64511)
	confirmed, err := RequestQuagga(ctx, socket, "confirm", QuaggaDecision{ID: first.ID, Checksum: first.Checksum})
	if err != nil || confirmed.Status != "confirmed" || confirmed.ActiveChecksum != Checksum(base) {
		t.Fatal("base A was not explicitly confirmed", confirmed, err)
	}
	baseProcess, baseRuntime := observe()
	fresh := stage(candidate, base)
	quaggaWatchdogCease(t, peers)
	peers = quaggaWatchdogConnect(t, candidate)
	peers.policy(0, 7, 64512)
	candidateProcess, candidateRuntime := observe()
	if candidateProcess == baseProcess || candidateRuntime == baseRuntime || !time.Now().Before(fresh.Deadline) {
		t.Fatal("the confirmed and pending native instances were not distinct")
	}
	journalPath := filepath.Join(stateDir, "transaction.json")
	pendingRaw, err := quaggaPrivateRead(journalPath, 4096)
	var pending quaggaJournal
	if err != nil || json.Unmarshal(pendingRaw, &pending) != nil || pending.State.Status != "pending" || pending.State.ID != fresh.ID || pending.State.ActiveChecksum != Checksum(base) || pending.State.Checksum != Checksum(candidate) {
		t.Fatal("unconfirmed candidate was not durably bound to the old confirmed base", err)
	}
	baseSlot := filepath.Join(stateDir, fmt.Sprintf("policy-%d.conf", pending.ActiveSlot))
	baseBytes, err := quaggaPrivateRead(baseSlot, maxConfigSize)
	if err != nil || string(baseBytes) != base {
		t.Fatal("the exact confirmed A policy was not preserved", err)
	}

	stop(true)
	for deadline := time.Now().Add(5 * time.Second); ; {
		generation, processState, statErr := quaggaGeneration(candidateProcess.PID)
		listeners, listenErr := host.listeners()
		exited := os.IsNotExist(statErr) || (statErr == nil && (processState == "Z" || generation != candidateProcess.Generation))
		if exited && listenErr == nil && len(listeners) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SIGKILL left the candidate generation or its BGP listeners alive", processState, statErr, listeners, listenErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, peer := range peers.peers {
		select {
		case <-peer.done:
			if routes, _, _ := peer.state(); len(routes) != 0 {
				t.Fatal("crashed session retained routes in the receiver")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("crashed native daemon left a live BGP session")
		}
	}
	oldVTY, err := quaggaSocketIdentity(host.socket)
	if err != nil || oldVTY.Ino != candidateProcess.Inode || oldVTY.Dev != candidateProcess.Device {
		t.Fatal("candidate's stale VTY name no longer matches its exact receipt", err)
	}

	foreign, err := net.Listen("tcp4", "127.0.0.1:1179")
	if err != nil {
		t.Fatal("fixture could not reserve the foreign BGP listener", err)
	}
	foreignListeners, err := host.listeners()
	if err != nil || len(foreignListeners) != 1 {
		foreign.Close()
		t.Fatal("foreign listener not observable", err)
	}
	rejected, rejectErr := OpenQuaggaWatchdog(ctx, opts)
	if rejected != nil {
		_ = rejected.Close()
	}
	stillForeign, err := host.listeners()
	foreign.Close()
	if rejectErr == nil || !strings.Contains(rejectErr.Error(), "already occupied") || err != nil || !reflect.DeepEqual(stillForeign, foreignListeners) {
		t.Fatal("recovery interfered with an unrelated listener", rejectErr, err)
	}
	stillPending, err := quaggaPrivateRead(journalPath, 4096)
	if err != nil || !bytes.Equal(stillPending, pendingRaw) {
		t.Fatal("rejected recovery rewrote durable intent", err)
	}

	stop = start(false)
	recovered, err := RequestQuagga(ctx, socket, "status", nil)
	if err != nil || recovered.Status != "recovered" || recovered.ActiveChecksum != Checksum(base) || recovered.ID != fresh.ID {
		t.Fatal("the new supervisor did not recover exact confirmed A", recovered, err)
	}
	if _, err := RequestQuagga(ctx, socket, "confirm", QuaggaDecision{ID: fresh.ID, Checksum: fresh.Checksum}); err == nil {
		t.Fatal("old pre-crash confirmation was accepted")
	}
	recoveredProcess, recoveredRuntime := observe()
	if recoveredProcess == candidateProcess || recoveredProcess == baseProcess || recoveredRuntime != baseRuntime {
		t.Fatal("fresh child identity or exact running A did not match recovery")
	}
	restoredBytes, err := quaggaPrivateRead(baseSlot, maxConfigSize)
	if err != nil || !bytes.Equal(restoredBytes, baseBytes) {
		t.Fatal("recovery changed the confirmed A policy bytes", err)
	}
	peers = quaggaWatchdogConnect(t, base)
	peers.policy(7, 0, 64511)
	stop(false)
	quaggaWatchdogCease(t, peers)
	if !reflect.DeepEqual(quaggaWireKernel(t), kernel) {
		t.Fatal("Quagga crash recovery changed the kernel routing table")
	}
	t.Log("quagga_supervisor_sigkill=true pending_candidate_wire_verified=true parent_death_no_orphan=true both_bgp_listeners_released=true foreign_listener_preserved=true exact_confirmed_reopen=true old_confirm_refused=true restored_dual_stack_wire=true no_kernel_change=true")
}
