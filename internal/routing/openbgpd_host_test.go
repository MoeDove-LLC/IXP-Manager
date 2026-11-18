package routing

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenBGPDReloadAcknowledgmentAndBarrier(t *testing.T) {
	outputs := []string{"reload request sent.\nrequest processed\n", "reload request sent.\nprevious reload still running\n", "reload request sent.\nrequest processed\n", "request processed\n"}
	calls := 0
	invoke := func(_ context.Context, args ...string) (string, error) {
		if len(args) == 1 && args[0] == "-V" {
			return "OpenBGPD 8.8", nil
		}
		if args[0] != "-s" || args[1] != OpenBGPDControlSocket {
			t.Fatal("unpinned control endpoint")
		}
		if calls == 3 && strings.Join(args[2:], " ") != "neighbor 192.0.2.2 clear" {
			t.Fatal("unexpected reset argv")
		}
		v := outputs[calls]
		calls++
		return v, nil
	}
	if err := openBGPDReload(context.Background(), func() error { return nil }, invoke, []string{"192.0.2.2"}); err != nil || calls != 4 {
		t.Fatal("reload barrier", err, calls)
	}
	for _, bad := range []string{"reload request sent.\n", "reload request sent.\nconfig file has errors, reload failed\n", "reload request sent.\nrequest processed\nsecret diagnostic"} {
		invoke := func(_ context.Context, args ...string) (string, error) {
			if args[0] == "-V" {
				return "OpenBGPD 8.8", nil
			}
			return bad, nil
		}
		if err := openBGPDReload(context.Background(), func() error { return nil }, invoke, nil); err == nil || strings.Contains(err.Error(), "secret diagnostic") {
			t.Fatal("zero exit without exact native acknowledgment accepted or diagnostic leaked")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	invoke = func(_ context.Context, args ...string) (string, error) {
		if args[0] == "-V" {
			return "OpenBGPD 8.8", nil
		}
		return "reload request sent.\nprevious reload still running", nil
	}
	if err := openBGPDReload(ctx, func() error { return nil }, invoke, nil); err == nil {
		t.Fatal("busy reload escaped deadline")
	}
	if err := openBGPDReload(context.Background(), func() error { return errors.New("identity changed") }, invoke, nil); err == nil {
		t.Fatal("identity failure ignored")
	}
}
func TestOpenBGPDUnixPeerIdentity(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("positive root parent credential test runs in isolated user namespace")
	}
	path := filepath.Join(t.TempDir(), "control.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	os.Chmod(path, 0600)
	if err := openBGPDUnixPeer(path, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := openBGPDUnixPeer(path, os.Getpid()+1); err == nil {
		t.Fatal("wrong parent PID accepted")
	}
	os.Chmod(path, 0666)
	if err := openBGPDUnixPeer(path, os.Getpid()); err == nil {
		t.Fatal("public control socket accepted")
	}
}
func TestOpenBGPDProcessRejectsWrongBinaryOrTarget(t *testing.T) {
	opts := OpenBGPDOptions{DaemonPID: os.Getpid(), Validator: "/not/the/daemon", Target: "/tmp/bgpd.conf"}
	if _, err := openBGPDProcess(opts, ""); err == nil {
		t.Fatal("wrong binary accepted")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	opts.Validator = exe
	if _, err := openBGPDProcess(opts, ""); err == nil {
		t.Fatal("process without explicit managed -f target accepted")
	}
}

func TestOpenBGPDParentPIDBoundaries(t *testing.T) {
	for _, pid := range []int{0, 1, 2, 12345} {
		if err := validateOpenBGPDHostOptions(OpenBGPDOptions{ExclusiveInstance: true, DaemonPID: pid}); err != nil {
			t.Fatalf("valid parent/discovery PID %d rejected: %v", pid, err)
		}
	}
	for _, opts := range []OpenBGPDOptions{{ExclusiveInstance: true, DaemonPID: -1}, {DaemonPID: 1}} {
		if err := validateOpenBGPDHostOptions(opts); err == nil {
			t.Fatal("negative PID or absent exclusive-instance acknowledgment accepted")
		}
	}
	if _, err := openBGPDProcess(OpenBGPDOptions{DaemonPID: 1, Validator: "/not/the/dedicated/bgpd", Target: "/tmp/bgpd.conf"}, ""); err == nil {
		t.Fatal("PID 1 bypassed executable/target identity verification")
	}
}
func TestOpenBGPDPrivateSocketTransactions(t *testing.T) {
	h := newOpenBGPDTestHost(t)
	socket := filepath.Join(filepath.Dir(h.opts.Target), "run", "agent.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveOpenBGPDWatchdog(ctx, h.opts, socket, func(ctx context.Context, opts OpenBGPDOptions) (*OpenBGPDWatchdog, error) {
			return openOpenBGPDWatchdog(ctx, opts, h.host())
		})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	var state OpenBGPDTransaction
	var err error
	for time.Now().Before(deadline) {
		state, err = RequestOpenBGPD(context.Background(), socket, "status", nil)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("agent socket not private")
	}
	state, err = RequestOpenBGPD(context.Background(), socket, "stage", h.request())
	if err != nil || state.Status != "pending" {
		t.Fatal(state, err)
	}
	state, err = RequestOpenBGPD(context.Background(), socket, "cancel", OpenBGPDDecision{state.ID, state.Checksum})
	if err != nil || state.Status != "cancelled" {
		t.Fatal(state, err)
	}
	if _, err = RequestOpenBGPD(context.Background(), socket, "arbitrary", nil); err == nil {
		t.Fatal("arbitrary command accepted")
	}
	assertOpenBGPDFile(t, h.live, h.old)
}
