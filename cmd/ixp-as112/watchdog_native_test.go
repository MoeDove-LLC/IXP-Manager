//go:build linux && amd64

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/as112ops"
	"github.com/moedove/ixp-manager/internal/routing"
)

// Test-only pidfd helpers use Linux amd64's installed UAPI syscall numbers. A
// pidfd is opened first, then the complete recorded generation and ELF checksum
// are checked. No signal is sent from an unverified journal PID.
func watchdogProcessStart(pid int) (string, error) {
	raw, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return "", e
	}
	close := bytes.LastIndexByte(raw, ')')
	if close < 0 {
		return "", errors.New("process stat unavailable")
	}
	fields := strings.Fields(string(raw[close+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return "", os.ErrNotExist
	}
	return fields[19], nil
}
func verifiedProcessFD(p as112ops.Process) (int, error) {
	fd, _, errno := syscall.Syscall(434, uintptr(p.PID), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	valid := false
	defer func() {
		if !valid {
			syscall.Close(int(fd))
		}
	}()
	start, e := watchdogProcessStart(p.PID)
	if e != nil || start != p.StartTime {
		return -1, errors.New("process generation differs from installed owned receipt")
	}
	file, e := os.Open(fmt.Sprintf("/proc/%d/exe", p.PID))
	if e != nil {
		return -1, e
	}
	hash := sha256.New()
	_, e = io.Copy(hash, file)
	file.Close()
	if e != nil || hex.EncodeToString(hash.Sum(nil)) != p.ExecutableSHA256 {
		return -1, errors.New("owned executable changed")
	}
	valid = true
	return int(fd), nil
}
func signalProcessFD(fd int, signal syscall.Signal) error {
	_, _, errno := syscall.Syscall6(424, uintptr(fd), uintptr(signal), 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
func requireWatchdogNative(t *testing.T) {
	t.Helper()
	if os.Getenv("AS112_WATCHDOG_NATIVE") != "1" {
		t.Skip("explicit isolated CLI watchdog fixture required")
	}
	raw, e := os.ReadFile("/proc/self/status")
	if e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
		expected := "0000000000000000"
		if field == "NoNewPrivs" {
			expected = "1"
		}
		found := false
		for _, line := range strings.Split(string(raw), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 && parts[0] == field+":" {
				found = parts[1] == expected
			}
		}
		if !found {
			t.Fatal("watchdog native fixture requires zero capabilities and no_new_privs")
		}
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		t.Fatal(e)
	}
	for _, iface := range interfaces {
		if iface.Name != "lo" {
			t.Fatal("unexpected externally reachable interface")
		}
	}
}
func watchdogNativePlan(t *testing.T) (as112ops.Plan, string) {
	t.Helper()
	tools := as112ops.Tools{}
	var e error
	for path, pin := range map[string]*as112ops.BinaryPin{"/native/common/usr/sbin/bird": &tools.Router, "/native/common/usr/sbin/pdns_server": &tools.PDNS, "/native/common/usr/bin/named-checkzone": &tools.NamedCheckzone} {
		*pin, e = as112ops.PinBinary(path)
		if e != nil {
			t.Fatal(e)
		}
	}
	router := routing.Router{ID: "watchdog_as112", Role: routing.RoleAS112, Platform: "bird2", ASN: 112, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", LocalIPv6: "2001:db8::112", AS112Service: "both", AS112IPv4: true, AS112IPv6: true}
	active, e := routing.RenderFor("bird2", router, nil)
	if e != nil {
		t.Fatal(e)
	}
	bundle, e := as112.RenderNode(router.ID, "2026-11-18T00:00:00Z", routing.AS112Service(router), as112.Identity{ServerName: "watchdog.example.test.", ResponsibleMailbox: "hostmaster.example.test.", Facility: "isolated watchdog fixture", Location: "private namespace", UniqueIP: router.LocalIPv4}, []string{router.LocalIPv4, router.LocalIPv6})
	if e != nil {
		t.Fatal(e)
	}
	dns, e := as112.Encode(bundle)
	if e != nil {
		t.Fatal(e)
	}
	host, e := as112ops.CaptureTarget()
	if e != nil {
		t.Fatal(e)
	}
	dir, e := os.MkdirTemp("", "wd-native-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p, e := as112ops.BuildPlan(as112ops.PlanInput{Root: filepath.Join(dir, "instance"), Platform: "bird2", ActiveConfig: active, DNSBundle: dns, Tools: tools, Host: host, HealthIntervalSeconds: 1})
	if e != nil {
		t.Fatal(e)
	}
	controller, e := as112ops.Open(p, nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = controller.Install(context.Background(), as112ops.Approval{PlanChecksum: p.Checksum, ActiveChecksum: p.ActiveChecksum, StandbyChecksum: p.StandbyChecksum, DNSChecksum: p.DNSChecksum, ExclusiveInstanceAcknowledged: true})
	closeErr := controller.Close()
	if e != nil || closeErr != nil {
		t.Fatal("native watchdog fixture installation", e, closeErr)
	}
	raw, e := as112ops.EncodePlan(p)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(dir, "plan.json")
	if e = writeNewPrivate(file, raw); e != nil {
		t.Fatal(e)
	}
	return p, file
}

type externalWatchdogResult struct {
	heartbeats int
	ignored    int
	expired    bool
	err        error
}

// This independent test watcher owns exactly one exec.Cmd process handle. It
// models NotifyAccess=main via kernel credentials and never signals journal PIDs.
// It is deliberately not a replacement service manager shipped with the CLI.
func externalWatchdog(connection *net.UnixConn, owner *os.Process, result chan<- externalWatchdogResult) {
	state := externalWatchdogResult{}
	deadline := time.Now().Add(12 * time.Second)
	ready := false
	for {
		payload, pid, e := receiveNotification(connection, time.Until(deadline))
		if e != nil {
			if timeout, ok := e.(net.Error); ok && timeout.Timeout() && ready {
				state.expired = true
				state.err = owner.Kill()
			} else {
				state.err = e
			}
			result <- state
			return
		}
		if pid != owner.Pid {
			state.ignored++
			continue
		}
		if payload == "STOPPING=1" {
			result <- state
			return
		}
		if payload == "READY=1\nWATCHDOG=1" {
			ready = true
		}
		if ready && (payload == "WATCHDOG=1" || payload == "READY=1\nWATCHDOG=1") {
			state.heartbeats++
			deadline = time.Now().Add(5 * time.Second)
		}
	}
}
func waitNativeStatus(t *testing.T, root string, check func(as112ops.Status) bool) as112ops.Status {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last as112ops.Status
	for time.Now().Before(deadline) {
		state, e := as112ops.ReadStatus(root)
		if e == nil {
			last = state
			if check(state) {
				return state
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("real CLI watchdog fixture did not reach state: phase=%s reason=%s", last.Phase, last.Reason)
	return last
}
func requireRecordedProcessDead(t *testing.T, p as112ops.Process) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		start, e := watchdogProcessStart(p.PID)
		if os.IsNotExist(e) || e == nil && start != p.StartTime {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("recorded owned process remained alive after external owner death")
}
func TestWatchdogRealCLIStopExpiresOwnedServices(t *testing.T) {
	requireWatchdogNative(t)
	p, file := watchdogNativePlan(t)
	connection, path := notificationSocket(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Getenv("AS112_WATCHDOG_CLI"), "run", "--plan", file, "--recover-ack")
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "NOTIFY_SOCKET=" + path, "WATCHDOG_USEC=5000000"}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if e := command.Start(); e != nil {
		t.Fatal(e)
	}
	finished := false
	defer func() {
		if !finished {
			command.Process.Kill()
			command.Wait()
		}
	}()
	result := make(chan externalWatchdogResult, 1)
	go externalWatchdog(connection, command.Process, result)
	active := waitNativeStatus(t, p.Root, func(s as112ops.Status) bool { return s.Phase == "active" && s.BGP != nil && s.DNS != nil })
	time.Sleep(2200 * time.Millisecond)
	select {
	case state := <-result:
		t.Fatal("responsive owner did not renew", state)
	default:
	}
	// The exact child generation has no notify variables in its cleared native
	// environment, and cannot send accepted notifications on behalf of its owner.
	env, e := os.ReadFile(fmt.Sprintf("/proc/%d/environ", active.DNS.PID))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(env, []byte("NOTIFY_SOCKET=")) || bytes.Contains(env, []byte("WATCHDOG_")) {
		t.Fatal("native child inherited manager notification authority")
	}
	dnsFD, e := verifiedProcessFD(*active.DNS)
	if e != nil {
		t.Fatal(e)
	}
	defer syscall.Close(dnsFD)
	defer signalProcessFD(dnsFD, syscall.SIGCONT)
	if e = signalProcessFD(dnsFD, syscall.SIGSTOP); e != nil {
		t.Fatal(e)
	}
	waitNativeStatus(t, p.Root, func(s as112ops.Status) bool { return s.Phase == "withdrawn" && s.BGP == nil && !s.LastHealthy })
	requireRecordedProcessDead(t, *active.BGP)
	select {
	case state := <-result:
		t.Fatal("normal DNS withdrawal was mistaken for owner hang", state)
	default:
	}
	if e = signalProcessFD(dnsFD, syscall.SIGCONT); e != nil {
		t.Fatal(e)
	}
	recovered := waitNativeStatus(t, p.Root, func(s as112ops.Status) bool {
		return s.Phase == "active" && s.BGP != nil && s.BGP.PID != active.BGP.PID && s.LastHealthy
	})
	if e = command.Process.Signal(syscall.SIGSTOP); e != nil {
		t.Fatal(e)
	}
	time.Sleep(300 * time.Millisecond)
	if start, e := watchdogProcessStart(recovered.BGP.PID); e != nil || start != recovered.BGP.StartTime {
		t.Fatal("BGP stopped before external lease expiry", e)
	}
	if e = sendSystemdNotification(path, "WATCHDOG=1"); e != nil {
		t.Fatal(e)
	} // unrelated sender must not extend this lease
	var report externalWatchdogResult
	select {
	case report = <-result:
	case <-time.After(7 * time.Second):
		t.Fatal("external watchdog failed to expire stopped owner")
	}
	if !report.expired || report.err != nil || report.heartbeats < 4 || report.ignored < 1 {
		t.Fatal("watchdog lease or credential check failed", report)
	}
	if e = command.Wait(); e == nil {
		t.Fatal("stopped owner did not receive external termination")
	}
	finished = true
	requireRecordedProcessDead(t, *recovered.BGP)
	requireRecordedProcessDead(t, *recovered.DNS)
	if bytes.Contains(output.Bytes(), []byte("activeConfig")) {
		t.Fatal("CLI emitted private plan")
	}
	t.Logf("real CLI: %d authenticated renewals; %d foreign notification rejected; real DNS failure withdrew then recovered; SIGSTOP lease expired and owned PDNS/BIRD exited", report.heartbeats, report.ignored)
}
