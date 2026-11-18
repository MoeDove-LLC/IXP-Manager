//go:build linux

package as112ops

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The child is this synthetic test executable, never a real DNS/BGP service.
func TestNativeOwnedChildHelper(t *testing.T) {
	at := -1
	for i, arg := range os.Args {
		if arg == "as112-owned-helper" {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	if at+1 >= len(os.Args) {
		os.Exit(42)
	}
	switch os.Args[at+1] {
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "parent":
		if at+2 >= len(os.Args) {
			os.Exit(43)
		}
		path, e := os.Executable()
		if e != nil {
			os.Exit(44)
		}
		pin, e := PinBinary(path)
		if e != nil {
			os.Exit(45)
		}
		r := NewNativeRuntime()
		p, e := r.start(context.Background(), Plan{}, pin, filepath.Dir(os.Args[at+2]), "bgp", "active", "-test.run=^TestNativeOwnedChildHelper$", "as112-owned-helper", "sleep")
		if e != nil {
			os.Exit(46)
		}
		raw, _ := json.Marshal(p)
		if os.WriteFile(os.Args[at+2], raw, 0600) != nil {
			os.Exit(47)
		}
		os.Exit(0)
	default:
		os.Exit(48)
	}
}
func TestNativeOwnedProcessPinsAndStop(t *testing.T) {
	path, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	pin, e := PinBinary(path)
	if e != nil {
		t.Fatal(e)
	}
	r := NewNativeRuntime()
	defer r.Close()
	p, e := r.start(context.Background(), Plan{}, pin, t.TempDir(), "bgp", "active", "-test.run=^TestNativeOwnedChildHelper$", "as112-owned-helper", "sleep")
	if e != nil {
		t.Fatal(e)
	}
	wrong := p
	wrong.StartTime += "wrong"
	if e = r.Stop(context.Background(), wrong); e == nil {
		t.Fatal("foreign process generation signaled")
	}
	if _, e = procStart(p.PID); e != nil {
		t.Fatal("wrong generation stop harmed owned process")
	}
	if e = r.Stop(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	if _, e = procStart(p.PID); !os.IsNotExist(e) {
		t.Fatal("owned process still alive", e)
	}
	changed := pin
	changed.SHA256 = strings.Repeat("0", 64)
	if _, e = r.start(context.Background(), Plan{}, changed, t.TempDir(), "bgp", "active"); e == nil {
		t.Fatal("changed native executable accepted")
	}
}
func TestNativeParentDeathTerminatesOwnedChild(t *testing.T) {
	path, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "receipt.json")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-test.run=^TestNativeOwnedChildHelper$", "as112-owned-helper", "parent", file)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("synthetic parent failed: %v %s", e, out)
	}
	raw, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	var p Process
	if e = json.Unmarshal(raw, &p); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, e = procStart(p.PID); os.IsNotExist(e) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("owned child survived creator process exit")
}
func TestNativePinRejectsScriptsAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-native")
	if e := os.WriteFile(file, []byte("#!/bin/sh\nexit 0\n"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := PinBinary(file); e == nil {
		t.Fatal("shell override allowed")
	}
	path, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "linked")
	if e = os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e = PinBinary(link); e == nil {
		t.Fatal("executable symlink allowed")
	}
}

func TestNativeReapedPIDDoesNotHideForeignProcess(t *testing.T) {
	path, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-test.run=^TestNativeOwnedChildHelper$", "as112-owned-helper", "sleep", filepath.Join(root, "claimed.conf"))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	r := NewNativeRuntime()
	done := make(chan struct{})
	close(done)
	r.children[cmd.Process.Pid] = &nativeChild{process: Process{PID: cmd.Process.Pid, StartTime: "old-generation"}, done: done}
	o, e := r.Observe(ctx, Plan{Root: root})
	if e != nil || len(o.Processes) != 1 || o.Processes[0].Kind != "foreign" || o.Processes[0].PID != cmd.Process.Pid {
		t.Fatal("reaped handle hid reused PID", o, e)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = procStart(cmd.Process.Pid); e != nil {
		t.Fatal("foreign process was killed", e)
	}
}
