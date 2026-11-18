//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/as112ops"
)

func notificationSocket(t *testing.T, abstract bool) (*net.UnixConn, string) {
	t.Helper()
	dir, e := os.MkdirTemp("", "ixwd-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "notify")
	if abstract {
		path = "@ixwd-" + filepath.Base(dir) + "-" + strconv.Itoa(os.Getpid())
	}
	connection, e := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := connection.SyscallConn()
	if e != nil {
		t.Fatal(e)
	}
	var optionErr error
	if e = raw.Control(func(fd uintptr) {
		optionErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1)
	}); e != nil || optionErr != nil {
		t.Fatal(e, optionErr)
	}
	t.Cleanup(func() { connection.Close() })
	return connection, path
}
func receiveNotification(connection *net.UnixConn, wait time.Duration) (string, int, error) {
	if e := connection.SetReadDeadline(time.Now().Add(wait)); e != nil {
		return "", 0, e
	}
	data, oob := make([]byte, 128), make([]byte, 128)
	n, on, flags, _, e := connection.ReadMsgUnix(data, oob)
	if e != nil {
		return "", 0, e
	}
	if flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 {
		return "", 0, fmt.Errorf("notification truncated")
	}
	messages, e := syscall.ParseSocketControlMessage(oob[:on])
	if e != nil {
		return "", 0, e
	}
	pid := 0
	for _, message := range messages {
		credentials, e := syscall.ParseUnixCredentials(&message)
		if e == nil {
			pid = int(credentials.Pid)
		}
	}
	if pid == 0 {
		return "", 0, fmt.Errorf("notification lacks kernel sender credentials")
	}
	payload := string(data[:n])
	if payload != "READY=1" && payload != "WATCHDOG=1" && payload != "READY=1\nWATCHDOG=1" && payload != "STOPPING=1" {
		return "", 0, fmt.Errorf("unexpected notification payload")
	}
	return payload, pid, nil
}
func configureNotify(t *testing.T, socket string) {
	t.Helper()
	t.Setenv("NOTIFY_SOCKET", socket)
	t.Setenv("WATCHDOG_USEC", "3000000")
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()))
}

func TestWatchdogEnvironmentAndProtocol(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		bad  bool
	}{
		{"foreground", map[string]string{}, false}, {"disabled", map[string]string{"WATCHDOG_USEC": "0"}, false},
		{"wrong-pid", map[string]string{"WATCHDOG_PID": "1"}, true}, {"bad-pid", map[string]string{"WATCHDOG_PID": "secret-invalid"}, true},
		{"no-socket", map[string]string{"WATCHDOG_USEC": "3000000"}, true}, {"overflow", map[string]string{"WATCHDOG_USEC": "18446744073709551615"}, true},
		{"bad-time", map[string]string{"WATCHDOG_USEC": "-2"}, true}, {"relative", map[string]string{"NOTIFY_SOCKET": "secret-relative"}, true},
		{"empty-abstract", map[string]string{"NOTIFY_SOCKET": "@"}, true}, {"newline", map[string]string{"NOTIFY_SOCKET": "/secret\nSTATUS=leak"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, e := newSystemdNotifier(func(k string) string { return tc.env[k] }, 112)
			if (e != nil) != tc.bad {
				t.Fatal("unexpected config acceptance", e)
			}
			if e != nil && strings.Contains(e.Error(), "secret") {
				t.Fatal("error disclosed environment")
			}
			if e == nil && n.progress() != nil {
				t.Fatal("plain foreground contacted service manager")
			}
		})
	}
	for _, abstract := range []bool{false, true} {
		t.Run(fmt.Sprintf("abstract=%v", abstract), func(t *testing.T) {
			conn, path := notificationSocket(t, abstract)
			config := map[string]string{"NOTIFY_SOCKET": path, "WATCHDOG_USEC": "180000000", "WATCHDOG_PID": strconv.Itoa(os.Getpid())}
			n, e := newSystemdNotifier(func(k string) string { return config[k] }, os.Getpid())
			if e != nil {
				t.Fatal(e)
			}
			for _, want := range []string{"READY=1\nWATCHDOG=1", "WATCHDOG=1", "STOPPING=1"} {
				if want == "STOPPING=1" {
					e = n.stopped()
				} else {
					e = n.progress()
				}
				if e != nil {
					t.Fatal(e)
				}
				payload, pid, e := receiveNotification(conn, time.Second)
				if e != nil || pid != os.Getpid() || payload != want {
					t.Fatal(payload, pid, e)
				}
			}
		})
	}
}
func TestWatchdogLoopRenewsAfterSafeHealthWithdrawal(t *testing.T) {
	plan, file := cliPlan(t)
	installCLI(t, plan, file)
	conn, path := notificationSocket(t, false)
	configureNotify(t, path)
	model := newCLIRuntime()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"run", "--plan", file, "--recover-ack"}, &bytes.Buffer{}, func() as112ops.Runtime { return model })
	}()
	payload, pid, e := receiveNotification(conn, 3*time.Second)
	if e != nil || payload != "READY=1\nWATCHDOG=1" || pid != os.Getpid() {
		t.Fatal(payload, pid, e)
	}
	first, e := as112ops.ReadStatus(plan.Root)
	if e != nil || !first.Installed || first.DNS == nil || first.BGP == nil || !first.LastHealthy {
		t.Fatal("readiness preceded safe first tick", first, e)
	}
	awaitCLIStatus(t, plan.Root, func(s as112ops.Status) bool { return s.Phase == "active" })
	model.mu.Lock()
	model.healthy = false
	model.mu.Unlock()
	awaitCLIStatus(t, plan.Root, func(s as112ops.Status) bool { return s.Phase == "withdrawn" && s.BGP == nil && !s.LastHealthy })
	for i := 0; i < 2; i++ {
		payload, _, e = receiveNotification(conn, 3*time.Second)
		if e != nil || payload != "WATCHDOG=1" {
			t.Fatal("responsive withdrawal stopped renewal", payload, e)
		}
	}
	cancel()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if model.active == 0 || len(model.stops) < 2 {
		t.Fatal("health withdrawal not exercised")
	}
}

type stalledHealthRuntime struct {
	*cliRuntime
	entered chan struct{}
	release chan struct{}
}

func (r *stalledHealthRuntime) Health(ctx context.Context, p as112ops.Plan) (as112.HealthResult, error) {
	close(r.entered)
	<-r.release
	return r.cliRuntime.Health(ctx, p)
}
func TestWatchdogDoesNotRenewBlockedControllerAndNotifyFailureCloses(t *testing.T) {
	t.Run("blocked-tick", func(t *testing.T) {
		plan, file := cliPlan(t)
		installCLI(t, plan, file)
		conn, path := notificationSocket(t, false)
		configureNotify(t, path)
		model := &stalledHealthRuntime{newCLIRuntime(), make(chan struct{}), make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- run(ctx, []string{"run", "--plan", file}, &bytes.Buffer{}, func() as112ops.Runtime { return model })
		}()
		select {
		case <-model.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("tick not entered")
		}
		if payload, _, e := receiveNotification(conn, 300*time.Millisecond); e == nil {
			t.Fatal("blocked first tick sent readiness/renewal", payload)
		}
		cancel()
		close(model.release)
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	})
	t.Run("notify-failed", func(t *testing.T) {
		plan, file := cliPlan(t)
		installCLI(t, plan, file)
		conn, path := notificationSocket(t, false)
		configureNotify(t, path)
		model := newCLIRuntime()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- run(ctx, []string{"run", "--plan", file, "--recover-ack"}, &bytes.Buffer{}, func() as112ops.Runtime { return model })
		}()
		if _, _, e := receiveNotification(conn, 2*time.Second); e != nil {
			t.Fatal(e)
		}
		conn.Close()
		if e := <-done; e == nil {
			t.Fatal("notification failure was hidden")
		}
		state, e := as112ops.ReadStatus(plan.Root)
		if e != nil || state.BGP != nil || state.DNS != nil || state.Phase != "withdrawn" {
			t.Fatal("notify failure retained owned processes", state, e)
		}
	})
	t.Run("bad-pid-before-runtime", func(t *testing.T) {
		_, file := cliPlan(t)
		t.Setenv("WATCHDOG_PID", "1")
		if e := run(context.Background(), []string{"run", "--plan", file}, &bytes.Buffer{}, func() as112ops.Runtime { t.Fatal("bad notification identity reached runtime"); return nil }); e == nil {
			t.Fatal("incorrect watchdog identity accepted")
		}
	})
}
func TestWatchdogReviewedUnitContract(t *testing.T) {
	p, _ := cliPlan(t)
	p.Root = "/var/lib/as112-node"
	p.Tools.Router.Path = "/usr/sbin/bird"
	p.Tools.PDNS.Path = "/usr/sbin/pdns_server"
	p.Tools.NamedCheckzone.Path = "/usr/bin/named-checkzone"
	text, e := unitContent(p, "/var/lib/as112-plan.json", as112ops.BinaryPin{Path: "/usr/bin/true", SHA256: strings.Repeat("a", 64)}, false, os.Geteuid())
	if e != nil {
		t.Fatal(e)
	}
	for _, expected := range []string{"Type=notify", "NotifyAccess=main", "WatchdogSec=180", "WatchdogSignal=SIGKILL", "TimeoutStartSec=180", "KillMode=control-group", "SendSIGKILL=yes", "FinalKillSignal=SIGKILL", "TimeoutStopSec=30", "LimitCORE=0"} {
		if !strings.Contains(text, expected+"\n") {
			t.Fatalf("unit lacks %s", expected)
		}
	}
	if directory := os.Getenv("AS112_WATCHDOG_UNIT_OUTPUT"); directory != "" {
		if e = os.WriteFile(filepath.Join(directory, "as112-watchdog.service"), []byte(text), 0600); e != nil {
			t.Fatal(e)
		}
	}
}
