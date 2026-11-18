package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestQuaggaCompiledSocketPathRequiresOneExactPrivatePath(t *testing.T) {
	const path = "/private/bgpd.vty"
	for _, sample := range []struct {
		raw  string
		want bool
	}{
		{"\x00" + path + "\x00", true},
		{path + "\x00" + path + "\x00", true},
		{"prefix" + path + "\x00", false},
		{path + "\x00/foreign/bgpd.vty\x00", false},
		{path, false},
		{"/foreign/bgpd.vty\x00", false},
	} {
		if quaggaSocketPath([]byte(sample.raw), path) != sample.want {
			t.Fatalf("unexpected path check for %q", sample.raw)
		}
	}
}

func TestQuaggaJournalKeepsLastConfirmedSlot(t *testing.T) {
	a, b := Checksum("a"), Checksum("b")
	j := quaggaJournal{Binding: "bound", Slots: [2]string{a, b}, State: QuaggaTransaction{Version: 1, ID: strings.Repeat("a", 48), Status: "pending", ActiveChecksum: a, BaseChecksum: a, Checksum: b, Deadline: time.Now()}}
	if !j.valid("bound") || j.valid("other") {
		t.Fatal("pending journal did not bind the committed active slot to the original instance")
	}
	j.ActiveSlot = 1
	j.State.ActiveChecksum = b
	if j.valid("bound") {
		t.Fatal("pending transaction admitted candidate as restart policy")
	}
	j.State.Status = "confirmed"
	if !j.valid("bound") {
		t.Fatal("confirmed transaction did not admit the exact candidate as restart policy")
	}
}

func quaggaWatchdogConnect(t *testing.T, config string) *quaggaLifecycle {
	t.Helper()
	l := &quaggaLifecycle{t: t, peers: map[string]*quaggaWirePeer{}}
	t.Cleanup(func() {
		for _, p := range l.peers {
			p.stop()
		}
	})
	model, err := parseManagedQuagga(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range model.Peers {
		deadline := time.Now().Add(5 * time.Second)
		for {
			state, err := quaggaWireVTY("show ip bgp view MOEDOVE_QUAGGA neighbors " + definition.Address)
			if err == nil && (strings.Contains(state, "BGP state = Active") || strings.Contains(state, "BGP state = Connect") || strings.Contains(state, "BGP state = Established")) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("native watchdog fixture peer did not leave Idle", definition.Address, err)
			}
			time.Sleep(30 * time.Millisecond)
		}
		peer, err := quaggaWireConnect(definition.Address, uint16(definition.ASN), strings.Contains(definition.Address, ":"))
		if err != nil {
			t.Fatal("native Quagga watchdog could not establish reviewed peer", definition.Address, err)
		}
		l.peers[definition.Address] = peer
	}
	return l
}

func quaggaWatchdogCease(t *testing.T, l *quaggaLifecycle) {
	t.Helper()
	for _, peer := range l.peers {
		select {
		case <-peer.done:
		case <-time.After(2 * time.Second):
			t.Fatal("superseded Quagga BGP session was not closed")
		}
		routes, notify, err := peer.state()
		if err != nil || len(routes) != 0 || len(notify) < 2 || notify[0] != 6 || notify[1] != 3 {
			t.Fatal("normal Quagga replacement did not send Cease and clear the peer RIB")
		}
		peer.stop()
	}
}

func quaggaKillFixtureCandidate(ctx context.Context, host *quaggaHost, baselinePID int) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		raw, err := quaggaPrivateRead(filepath.Join(host.opts.StateDir, "process.json"), 4096)
		var receipt quaggaProcessReceipt
		if err == nil && json.Unmarshal(raw, &receipt) == nil && receipt.PID != baselinePID && receipt.BootID == host.bootID && receipt.PID > 1 {
			process, err := os.FindProcess(receipt.PID)
			if err != nil {
				return err
			}
			defer process.Release()
			identity, err := os.Stat(fmt.Sprintf("/proc/%d/exe", receipt.PID))
			binary, pinErr := host.binary.Stat()
			generation, _, startErr := quaggaGeneration(receipt.PID)
			if err != nil || pinErr != nil || !os.SameFile(identity, binary) || startErr != nil || generation != receipt.Generation {
				return errors.New("fixture refused to signal an unverified candidate")
			}
			conn, stat, err := host.dial(ctx, receipt.PID)
			if err != nil {
				return err
			}
			conn.Close()
			if stat.Ino != receipt.Inode || stat.Dev != receipt.Device {
				return errors.New("fixture refused candidate with unknown socket identity")
			}
			return process.Kill()
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("fixture did not observe a candidate before readiness")
}

func TestQuaggaNativeWatchdogStageConfirmAndRecovery(t *testing.T) {
	quaggaWireGuard(t)
	kernel := quaggaWireKernel(t)
	a, b := quaggaLifecycleConfigs(t, false)
	qa, qb := Checksum(a), Checksum(b)
	bootstrap := filepath.Join(t.TempDir(), "base.conf")
	if err := os.WriteFile(bootstrap, []byte(a), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod("/scratch/state", 0700); err != nil {
		t.Fatal(err)
	}
	opts := QuaggaOptions{StateDir: "/scratch/state", Binary: os.Getenv("QUAGGA_BGPD_BIN"), BinaryChecksum: "9f758d79ce302c188b42d252ebd3b51044f803767db55c048574e8d4b60eaf67", BootstrapFile: bootstrap, BootstrapChecksum: qa, BGPPort: 1179, ExclusiveInstance: true}
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()
	host, err := newQuaggaHost(opts)
	if err != nil {
		t.Fatal(err)
	}
	_, versionErr := runQuaggaCommand(ctx, host.command(ctx, "--version"), opts.StateDir, false)
	host.binary.Close()
	if versionErr != nil {
		t.Fatal("native fixture cannot launch version probe", versionErr)
	}
	// The fixed Quagga build unlinks its VTY on start. The supervisor must
	// refuse a foreign listener at that pathname before executing any child.
	socket := filepath.Join(opts.StateDir, "bgpd.vty")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(socket, 0600); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	before, _ := os.Lstat(socket)
	if rejected, openErr := OpenQuaggaWatchdog(ctx, opts); openErr == nil {
		rejected.Close()
		listener.Close()
		t.Fatal("foreign live VTY was adopted")
	}
	after, err := os.Lstat(socket)
	listener.Close()
	if err != nil || before == nil || !os.SameFile(before, after) {
		t.Fatal("foreign socket was altered")
	}
	w, err := OpenQuaggaWatchdog(ctx, opts)
	if err != nil {
		t.Fatal("open native Quagga base", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	peers := quaggaWatchdogConnect(t, a)
	peers.policy(-1, 0, 64511)
	baselinePID := w.child.receipt.PID
	if _, err = w.Stage(ctx, QuaggaStageRequest{Config: b, Checksum: qb, BaseChecksum: qb, TimeoutSeconds: 60}); err == nil || w.child.receipt.PID != baselinePID {
		t.Fatal("stale base interrupted Quagga")
	}
	stage := func(base, next, config string) QuaggaTransaction {
		t.Helper()
		state, err := w.Stage(ctx, QuaggaStageRequest{Config: config, Checksum: next, BaseChecksum: base, TimeoutSeconds: 60})
		if err != nil || state.Status != "pending" || state.ActiveChecksum != base || state.Checksum != next {
			t.Fatal("stage did not keep the reviewed base", state, err)
		}
		return state
	}
	staged := stage(qa, qb, b)
	quaggaWatchdogCease(t, peers)
	if w.child.receipt.PID == baselinePID {
		t.Fatal("Quagga did not replace the child")
	}
	peers = quaggaWatchdogConnect(t, b)
	peers.policy(0, 7, 64512)
	if _, err = w.Confirm(ctx, QuaggaDecision{ID: staged.ID, Checksum: qa}); err == nil {
		t.Fatal("incorrect confirmation was accepted")
	}
	cancelled, err := w.Cancel(QuaggaDecision{ID: staged.ID, Checksum: qb})
	if err != nil || cancelled.Status != "cancelled" || cancelled.ActiveChecksum != qa {
		t.Fatal("cancellation did not restore base", cancelled, err)
	}
	quaggaWatchdogCease(t, peers)
	peers = quaggaWatchdogConnect(t, a)
	peers.policy(7, 0, 64511)
	staged = stage(qa, qb, b)
	quaggaWatchdogCease(t, peers)
	confirmed, err := w.Confirm(ctx, QuaggaDecision{ID: staged.ID, Checksum: qb})
	if err != nil || confirmed.Status != "confirmed" || confirmed.ActiveChecksum != qb {
		t.Fatal("confirmation did not publish active slot", confirmed, err)
	}
	peers = quaggaWatchdogConnect(t, b)
	peers.policy(0, 7, 64512)
	killed := make(chan error, 1)
	killPID := w.child.receipt.PID
	go func() { killed <- quaggaKillFixtureCandidate(ctx, w.host, killPID) }()
	failed, stageErr := w.Stage(ctx, QuaggaStageRequest{Config: a, Checksum: qa, BaseChecksum: qb, TimeoutSeconds: 60})
	if err := <-killed; err != nil {
		t.Fatal(err)
	}
	if stageErr == nil || failed.Status != "failed" || failed.ActiveChecksum != qb || w.child.receipt.PID == killPID {
		t.Fatal("failed candidate was not replaced by last confirmed policy", failed, stageErr)
	}
	quaggaWatchdogCease(t, peers)
	peers = quaggaWatchdogConnect(t, b)
	peers.policy(0, 7, 64512)
	stage(qb, qa, a)
	quaggaWatchdogCease(t, peers)
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	opts.BootstrapFile, opts.BootstrapChecksum = "", ""
	w, err = OpenQuaggaWatchdog(ctx, opts)
	if err != nil {
		t.Fatal("restart did not restore the last confirmed configuration", err)
	}
	if w.Status().Status != "recovered" || w.Status().ActiveChecksum != qb {
		t.Fatal("restart did not restore confirmed policy")
	}
	peers = quaggaWatchdogConnect(t, b)
	peers.policy(0, 7, 64512)
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	quaggaWatchdogCease(t, peers)
	raw, err := quaggaPrivateRead(filepath.Join(opts.StateDir, "transaction.json"), 4096)
	var journal quaggaJournal
	if err != nil || json.Unmarshal(raw, &journal) != nil || journal.State.ActiveChecksum != qb {
		t.Fatal("Quagga active policy not durable")
	}
	if !reflect.DeepEqual(quaggaWireKernel(t), kernel) {
		t.Fatal("Quagga watchdog changed kernel routes")
	}
	t.Log("quagga_watchdog_native=true fixed_native_binary=true private_foreign_socket_preserved=true stale_base_no_interruption=true fresh_replace=true exact_cancel_restore=true failed_candidate_restores_confirmed_wire_policy=true confirmed_policy_restarts=true no_kernel_change=true")
}
