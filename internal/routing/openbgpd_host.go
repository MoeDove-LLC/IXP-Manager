package routing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func openBGPDPrivateRead(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > limit {
		return nil, errors.New("OpenBGPD state/configuration requires a bounded private regular file (0600)")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("OpenBGPD file read failed or exceeded size limit")
	}
	return raw, nil
}
func newOpenBGPDHost(opts OpenBGPDOptions) (openBGPDHost, error) {
	if err := validateOpenBGPDHostOptions(opts); err != nil {
		return openBGPDHost{}, err
	}
	if err := secureDir(filepath.Dir(OpenBGPDControlSocket)); err != nil {
		return openBGPDHost{}, err
	}
	if opts.DaemonPID == 0 {
		cred, err := openBGPDUnixCredentials(OpenBGPDControlSocket)
		if err != nil {
			return openBGPDHost{}, err
		}
		opts.DaemonPID = int(cred.Pid)
	}
	start, err := openBGPDProcess(opts, "")
	if err != nil {
		return openBGPDHost{}, err
	}
	verify := func() error {
		if _, err := openBGPDProcess(opts, start); err != nil {
			return err
		}
		return openBGPDControlPeer(opts.DaemonPID)
	}
	if err = verify(); err != nil {
		return openBGPDHost{}, err
	}
	validate := func(ctx context.Context, config string) error { return ValidateOpenBGPD(ctx, opts.Validator, config) }
	reload := func(ctx context.Context, reset []string) error {
		return openBGPDReload(ctx, verify, func(ctx context.Context, args ...string) (string, error) { return run(ctx, opts.BGPCTL, args...) }, reset)
	}
	return openBGPDHost{verify, validate, reload}, nil
}

func validateOpenBGPDHostOptions(opts OpenBGPDOptions) error {
	// PID 1 is a legitimate dedicated daemon in a container. Its executable,
	// explicit target, process generation and root socket ownership are still
	// verified exactly like every other parent; zero requests socket discovery.
	if !opts.ExclusiveInstance || opts.DaemonPID < 0 {
		return errors.New("exclusive-instance and a valid optional parent --daemon-pid are required")
	}
	return nil
}

// Process identity establishes that the pinned administrative socket belongs to
// the parent loading this exact managed file. It cannot detect another root
// administrator issuing dynamic runtime commands; exclusive ownership is required.
func openBGPDProcess(opts OpenBGPDOptions, wantStart string) (string, error) {
	base := fmt.Sprintf("/proc/%d", opts.DaemonPID)
	executable, err := os.Readlink(base + "/exe")
	if err != nil {
		return "", errors.New("dedicated OpenBGPD parent process unavailable")
	}
	expected, err := filepath.EvalSymlinks(opts.Validator)
	if err != nil || expected != executable {
		return "", errors.New("daemon executable does not match the configured native checker")
	}
	cmd, err := os.ReadFile(base + "/cmdline")
	if err != nil || len(cmd) > 8192 {
		return "", errors.New("cannot verify OpenBGPD process arguments")
	}
	args := strings.Split(strings.TrimSuffix(string(cmd), "\x00"), "\x00")
	seen := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-d":
		case "-f":
			if seen || i+1 >= len(args) || args[i+1] != opts.Target {
				return "", errors.New("daemon is not using the exact managed target")
			}
			seen = true
			i++
		default:
			return "", errors.New("daemon must use only optional -d and explicit -f target arguments")
		}
	}
	if !seen {
		return "", errors.New("daemon requires explicit -f managed target")
	}
	stat, err := os.ReadFile(base + "/stat")
	if err != nil {
		return "", errors.New("cannot verify process generation")
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return "", errors.New("invalid process identity")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 {
		return "", errors.New("invalid process generation")
	}
	start := fields[19]
	if _, err = strconv.ParseUint(start, 10, 64); err != nil || wantStart != "" && start != wantStart {
		return "", errors.New("OpenBGPD parent process generation changed; restart watchdog recovery")
	}
	return start, nil
}
func openBGPDControlPeer(pid int) error { return openBGPDUnixPeer(OpenBGPDControlSocket, pid) }
func openBGPDUnixPeer(path string, pid int) error {
	cred, err := openBGPDUnixCredentials(path)
	if err != nil {
		return err
	}
	if int(cred.Pid) != pid {
		return errors.New("control socket is not owned by the pinned OpenBGPD root parent")
	}
	return nil
}
func openBGPDUnixCredentials(path string) (*syscall.Ucred, error) {
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm()&0007 != 0 {
		return nil, errors.New("private OpenBGPD control socket unavailable")
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return nil, errors.New("OpenBGPD control socket unavailable")
	}
	defer conn.Close()
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, errors.New("control endpoint is not a Unix socket")
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return nil, errors.New("cannot inspect control socket identity")
	}
	var cred *syscall.Ucred
	var inspect error
	err = raw.Control(func(fd uintptr) {
		cred, inspect = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || inspect != nil || cred == nil || cred.Pid <= 0 || cred.Uid != 0 {
		return nil, errors.New("control socket is not owned by an OpenBGPD root parent")
	}
	return cred, nil
}

func openBGPDReload(ctx context.Context, verify func() error, invoke func(context.Context, ...string) (string, error), reset []string) error {
	if err := verify(); err != nil {
		return err
	}
	version, err := invoke(ctx, "-V")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(version), "OpenBGPD ") {
		return errors.New("control executable must be OpenBGPD bgpctl")
	}
	// The initial success acknowledges parsing and distributing a reload.
	// A second accepted identical reload proves the preceding reload finished:
	// OpenBGPD otherwise replies 'previous reload still running'. This is a
	// barrier for TCP-MD5 resets, not a running-config or route-health query.
	for accepted := 0; accepted < 2; {
		if err = verify(); err != nil {
			return err
		}
		out, e := invoke(ctx, "-s", OpenBGPDControlSocket, "reload")
		if e != nil {
			return openBGPDToolError("OpenBGPD reload", e)
		}
		switch strings.TrimSpace(out) {
		case "reload request sent.\nrequest processed":
			accepted++
		case "reload request sent.\nprevious reload still running":
			select {
			case <-ctx.Done():
				return errors.New("OpenBGPD reload acknowledgment deadline exceeded")
			case <-time.After(50 * time.Millisecond):
			}
		default:
			return errors.New("OpenBGPD reload was not acknowledged; raw diagnostics withheld")
		}
	}
	for _, address := range reset {
		if err = verify(); err != nil {
			return err
		}
		out, e := invoke(ctx, "-s", OpenBGPDControlSocket, "neighbor", address, "clear")
		if e != nil {
			return openBGPDToolError("OpenBGPD authentication session reset", e)
		}
		if strings.TrimSpace(out) != "request processed" {
			return errors.New("OpenBGPD authentication reset was not acknowledged; raw diagnostics withheld")
		}
	}
	return nil
}
