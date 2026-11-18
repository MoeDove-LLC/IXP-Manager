//go:build linux

package as112ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
)

type nativeChild struct {
	process Process
	cmd     *exec.Cmd
	done    chan struct{}
}
type NativeRuntime struct {
	mu       sync.Mutex
	children map[int]*nativeChild
}

func NewNativeRuntime() *NativeRuntime { return &NativeRuntime{children: map[int]*nativeChild{}} }
func CaptureTarget() (TargetPin, error) {
	var p TargetPin
	raw, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return p, e
	}
	p.BootID = strings.TrimSpace(string(raw))
	s, e := os.Stat("/proc/self/ns/net")
	if e != nil {
		return p, e
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok {
		return p, errors.New("network namespace identity unavailable")
	}
	p.NetworkNamespace = fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	return p, nil
}

func openBinary(pin BinaryPin) (*os.File, error) {
	if !cleanAbs(pin.Path) {
		return nil, errors.New("invalid executable path")
	}
	if e := noSymlinks(pin.Path); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(pin.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	good := false
	defer func() {
		if !good {
			f.Close()
		}
	}()
	s, e := f.Stat()
	if e != nil {
		return nil, e
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || !s.Mode().IsRegular() || s.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || s.Mode().Perm()&0022 != 0 || s.Mode().Perm()&0111 == 0 || s.Size() > 128<<20 || st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("native executable permissions/owner/size invalid")
	}
	// A privileged exec can clear Linux's parent-death signal. The service may
	// receive narrowly declared ambient capabilities, but the pinned ELF itself
	// must never change credentials through set-id bits or file capabilities.
	capBytes, capErr := syscall.Getxattr(fmt.Sprintf("/proc/self/fd/%d", f.Fd()), "security.capability", nil)
	if capBytes > 0 || capErr != nil && !errors.Is(capErr, syscall.ENODATA) && !errors.Is(capErr, syscall.ENOTSUP) {
		return nil, errors.New("native executable file capabilities would invalidate parent-death ownership")
	}
	magic := make([]byte, 4)
	if _, e = io.ReadFull(f, magic); e != nil || !bytes.Equal(magic, []byte{127, 'E', 'L', 'F'}) {
		return nil, errors.New("only fixed native ELF executables are allowed")
	}
	if _, e = f.Seek(0, 0); e != nil {
		return nil, e
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return nil, e
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if pin.SHA256 != "" && sum != pin.SHA256 {
		return nil, errors.New("native executable SHA256 changed")
	}
	if _, e = f.Seek(0, 0); e != nil {
		return nil, e
	}
	good = true
	return f, nil
}
func PinBinary(path string) (BinaryPin, error) {
	p := BinaryPin{Path: path}
	f, e := openBinary(p)
	if e != nil {
		return p, e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return p, e
	}
	p.SHA256 = hex.EncodeToString(h.Sum(nil))
	return p, nil
}
func pinnedCommand(ctx context.Context, pin BinaryPin, dir string, args ...string) (*exec.Cmd, *os.File, error) {
	f, e := openBinary(pin)
	if e != nil {
		return nil, nil, e
	}
	var cmd *exec.Cmd
	if ctx == nil {
		cmd = exec.Command("/proc/self/fd/3", args...)
	} else {
		cmd = exec.CommandContext(ctx, "/proc/self/fd/3", args...)
	}
	cmd.Args[0] = pin.Path
	cmd.ExtraFiles = []*os.File{f}
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "LANG=C"}
	cmd.WaitDelay = 2 * time.Second
	return cmd, f, nil
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 64<<10 {
		keep := min(n, (64<<10)-b.Len())
		_, _ = b.Buffer.Write(p[:keep])
	}
	return n, nil
}
func nativeCheck(ctx context.Context, pin BinaryPin, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd, f, e := pinnedCommand(ctx, pin, dir, args...)
	if e != nil {
		return "", e
	}
	defer f.Close()
	out := &limitedOutput{}
	cmd.Stdout = out
	cmd.Stderr = out
	e = cmd.Run()
	return out.String(), e
}
func runDir(p Plan) (string, error) {
	dir := filepath.Join(p.Root, "run")
	if e := os.Mkdir(dir, 0700); e != nil && !os.IsExist(e) {
		return "", e
	}
	return dir, privateDir(dir)
}
func (r *NativeRuntime) Validate(ctx context.Context, p Plan, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	run, e := runDir(p)
	if e != nil {
		return e
	}
	b, e := as112.Decode(p.DNSBundle)
	if e != nil {
		return e
	}
	dns := filepath.Join(dir, "dns")
	for i, z := range b.Zones {
		if _, e = nativeCheck(ctx, p.Tools.NamedCheckzone, dns, "-i", "local", z, filepath.Join(dns, "zones", fmt.Sprintf("zone-%02d.zone", i))); e != nil {
			return errors.New("native zone check failed")
		}
	}
	if _, e = nativeCheck(ctx, p.Tools.PDNS, dns, "--config-dir="+dns, "--config=check"); e != nil {
		return errors.New("native DNS config check failed")
	}
	if p.Platform == "bird2" || p.Platform == "bird3" {
		out, e := nativeCheck(ctx, p.Tools.Router, run, "--version")
		major := "2"
		if p.Platform == "bird3" {
			major = "3"
		}
		if e != nil || !strings.Contains(out, "BIRD version "+major+".") {
			return errors.New("native BIRD major does not match plan")
		}
	}
	for _, mode := range []string{"active", "standby"} {
		file := filepath.Join(dir, mode+".conf")
		args := []string{"-p", "-c", file}
		if p.Platform == "frr" {
			args = []string{"--dryrun", "--skip_runas", "--bgp_port", "0", "--no_zebra", "--no_kernel", "--vty_socket", run, "--pid_file", filepath.Join(run, "check.pid"), "--config_file", file}
		}
		if _, e = nativeCheck(ctx, p.Tools.Router, run, args...); e != nil {
			return errors.New("native BGP config check failed")
		}
	}
	return nil
}
func procStart(pid int) (string, error) {
	raw, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return "", e
	}
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return "", errors.New("invalid process stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return "", errors.New("short process stat")
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return "", os.ErrNotExist
	}
	return fields[19], nil
}
func procHash(pid int) (string, error) {
	f, e := os.Open(fmt.Sprintf("/proc/%d/exe", pid))
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, io.LimitReader(f, (128<<20)+1)); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (r *NativeRuntime) Observe(ctx context.Context, p Plan) (Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := Observation{}
	entries, e := os.ReadDir("/proc")
	if e != nil || len(entries) > 65536 {
		return out, errors.New("process inventory unavailable")
	}
	for _, entry := range entries {
		if e = ctx.Err(); e != nil {
			return out, e
		}
		pid, e := strconv.Atoi(entry.Name())
		if e != nil {
			continue
		}
		if pid == os.Getpid() {
			continue
		}
		if ch := r.children[pid]; ch != nil {
			alive := true
			select {
			case <-ch.done:
				// A reaped child's numeric PID may now belong to a foreign
				// process. Drop the stale handle and inspect that process below.
				delete(r.children, pid)
				alive = false
			default:
			}
			if alive {
				start, e := procStart(pid)
				if os.IsNotExist(e) {
					continue
				}
				if e != nil || start != ch.process.StartTime {
					return out, errors.New("owned process generation changed")
				}
				sum, e := procHash(pid)
				if e != nil || sum != ch.process.ExecutableSHA256 {
					return out, errors.New("owned process executable changed")
				}
				out.Processes = append(out.Processes, ch.process)
				continue
			}
		}
		st, e := entry.Info()
		if e != nil {
			continue
		}
		rawStat, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return out, errors.New("process ownership unavailable")
		}
		if rawStat.Uid != uint32(os.Geteuid()) {
			// The private root and foreground children belong to this dedicated
			// UID. Privileged administrators must honor the declared exclusive
			// ownership boundary; this inventory does not police their processes.
			continue
		}
		raw, e := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return out, errors.New("same-user process inventory unreadable")
		}
		claim := false
		for _, arg := range strings.Split(string(raw), "\x00") {
			if strings.HasPrefix(arg, p.Root+"/") || strings.Contains(arg, "="+p.Root+"/") {
				claim = true
			}
		}
		if claim {
			start, e := procStart(pid)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return out, e
			}
			out.Processes = append(out.Processes, Process{PID: pid, StartTime: start, Kind: "foreign"})
		}
	}
	return out, nil
}
func (r *NativeRuntime) start(ctx context.Context, p Plan, pin BinaryPin, dir, kind, mode string, args ...string) (Process, error) {
	if e := ctx.Err(); e != nil {
		return Process{}, e
	}
	cmd, f, e := pinnedCommand(nil, pin, dir, args...)
	if e != nil {
		return Process{}, e
	}
	// Linux binds PDEATHSIG to the creating thread. Keep that exact OS thread
	// alive until Wait completes, rather than allowing Go to retire it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
	started := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		e := cmd.Start()
		_ = f.Close()
		started <- e
		if e == nil {
			_ = cmd.Wait()
		}
		close(done)
	}()
	if e = <-started; e != nil {
		return Process{}, errors.New("native process start failed")
	}
	start, e := procStart(cmd.Process.Pid)
	if e != nil {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return Process{}, errors.New("native child generation unavailable")
	}
	sum, e := procHash(cmd.Process.Pid)
	if e != nil || sum != pin.SHA256 {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return Process{}, errors.New("native child executable does not match pin")
	}
	process := Process{cmd.Process.Pid, start, sum, kind, mode}
	ch := &nativeChild{process, cmd, done}
	r.mu.Lock()
	r.children[process.PID] = ch
	r.mu.Unlock()
	select {
	case <-done:
		return Process{}, errors.New("native process exited during startup")
	case <-ctx.Done():
		_ = r.Stop(context.Background(), process)
		return Process{}, ctx.Err()
	case <-time.After(250 * time.Millisecond):
		return process, nil
	}
}
func (r *NativeRuntime) StartDNS(ctx context.Context, p Plan, dir string) (Process, error) {
	run, e := runDir(p)
	if e != nil {
		return Process{}, e
	}
	dns := filepath.Join(dir, "dns")
	return r.start(ctx, p, p.Tools.PDNS, dns, "dns", "authoritative", "--config-dir="+dns, "--daemon=no", "--guardian=no", "--disable-syslog=yes", "--write-pid=no", "--socket-dir="+run, "--loglevel=3")
}
func (r *NativeRuntime) StartBGP(ctx context.Context, p Plan, dir string, active bool) (Process, error) {
	run, e := runDir(p)
	if e != nil {
		return Process{}, e
	}
	mode := "standby"
	if active {
		mode = "active"
	}
	file := filepath.Join(dir, mode+".conf")
	args := []string{"-f", "-c", file, "-s", filepath.Join(run, "bird.ctl"), "-P", filepath.Join(run, "bird.pid")}
	if p.Platform == "frr" {
		args = []string{"--skip_runas", "--bgp_port", "179", "--no_zebra", "--no_kernel", "--vty_socket", run, "--pid_file", filepath.Join(run, "bgpd.pid"), "--config_file", file}
	}
	return r.start(ctx, p, p.Tools.Router, run, "bgp", mode, args...)
}
func (r *NativeRuntime) Stop(ctx context.Context, p Process) error {
	r.mu.Lock()
	ch := r.children[p.PID]
	r.mu.Unlock()
	if ch == nil || ch.process != p {
		return errors.New("refusing to signal a process not owned by this runtime generation")
	}
	select {
	case <-ch.done:
		return nil
	default:
	}
	start, e := procStart(p.PID)
	if os.IsNotExist(e) {
		select {
		case <-ch.done:
			return nil
		case <-time.After(time.Second):
			return errors.New("child exit unconfirmed")
		}
	}
	if e != nil || start != p.StartTime {
		return errors.New("process generation changed; no signal sent")
	}
	// os.Process uses pidfds on supported Linux. Its Wait state additionally
	// rejects signals after reaping; a numeric PID is never independently killed.
	if e = ch.cmd.Process.Signal(syscall.SIGTERM); e != nil && !errors.Is(e, os.ErrProcessDone) {
		return e
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-ch.done:
		return nil
	case <-ctx.Done():
	case <-timer.C:
	}
	if e = ch.cmd.Process.Kill(); e != nil && !errors.Is(e, os.ErrProcessDone) {
		return e
	}
	select {
	case <-ch.done:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("owned child exit unconfirmed")
	}
}
func (r *NativeRuntime) Health(ctx context.Context, p Plan) (as112.HealthResult, error) {
	b, e := as112.Decode(p.DNSBundle)
	if e != nil {
		return as112.HealthResult{}, e
	}
	return as112.Probe(ctx, b)
}
func (r *NativeRuntime) Close() error {
	r.mu.Lock()
	children := []Process{}
	for _, ch := range r.children {
		children = append(children, ch.process)
	}
	r.mu.Unlock()
	var first error
	for _, kind := range []string{"bgp", "dns"} {
		for _, p := range children {
			if p.Kind == kind {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				e := r.Stop(ctx, p)
				cancel()
				if first == nil {
					first = e
				}
			}
		}
	}
	return first
}
