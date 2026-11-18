package routing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ApplyOptions are supplied by the host administrator, never by HTTP requests.
type ApplyOptions struct {
	BirdMajor   int // zero preserves BIRD 2 compatibility; 3 requires BIRD 3
	BirdBinary  string
	BirdcBinary string
	Socket      string
	Target      string
	Checksum    string
}

var safePath = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)

// Apply is opt-in on the routing host. It requires an existing regular config,
// pins the reviewed bytes, validates with the explicitly selected BIRD major, serializes writers,
// preserves permissions/ownership, and restores the previous config on failure.
// The web service intentionally never calls Apply.
func Apply(ctx context.Context, config string, opts ApplyOptions) error {
	major, err := birdMajor(opts.BirdMajor)
	if err != nil {
		return err
	}
	if err := rejectRedactedArtifact(config); err != nil {
		return err
	}
	if len(opts.Checksum) != 64 || Checksum(config) != opts.Checksum {
		return errors.New("configuration checksum differs from the reviewed artifact")
	}
	if !safePath.MatchString(opts.Target) || filepath.Clean(opts.Target) != opts.Target {
		return errors.New("target must be an absolute clean path with only letters, digits, /, _, - and .")
	}
	parent := filepath.Dir(opts.Target)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return errors.New("target parent must exist and may not contain symlinks")
	}
	unlock, err := deploymentLock(opts.Target)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Lstat(opts.Target + ".moedove-pending"); !os.IsNotExist(err) {
		return errors.New("interrupted deployment detected; run ixp-agent recover before applying another revision")
	}
	info, err := os.Lstat(opts.Target)
	if err != nil {
		return fmt.Errorf("existing bootstrap configuration is required: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("target must be a regular file, not a symlink")
	}
	old, err := os.ReadFile(opts.Target)
	if err != nil {
		return err
	}
	if len(old) > maxConfigSize {
		return errors.New("existing configuration exceeds 16 MiB")
	}
	if strings.HasPrefix(string(old), bird1Header) {
		return errors.New("managed BIRD1 targets require their dedicated family-bound apply/recover path")
	}
	if err = privateAuthenticationTarget(config+"\n"+string(old), info.Mode()); err != nil {
		return err
	}
	if err = validateBIRDMajor(ctx, opts.BirdBinary, config, major); err != nil {
		return err
	}
	if major == 3 {
		if err = validateBIRDMajor(ctx, opts.BirdBinary, string(old), 3); err != nil {
			return fmt.Errorf("BIRD 3 rollback snapshot parse failed: %w", err)
		}
		if err = requireBIRD3Daemon(ctx, opts); err != nil {
			return err
		}
	}
	backup := opts.Target + ".moedove-previous"
	if info, err := os.Lstat(backup); err == nil && !info.Mode().IsRegular() {
		return errors.New("backup path is not a regular file")
	}
	if err = atomicConfig(backup, old, info); err != nil {
		return fmt.Errorf("write rollback copy: %w", err)
	}
	if err = writePendingMajor(opts.Target, Checksum(string(old)), major); err != nil {
		return err
	}
	rollback := func(cause error) error {
		// Rollback is independent of an expired/cancelled caller context.
		rctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		diskErr := atomicConfig(opts.Target, old, info)
		daemonErr := control(rctx, opts, "configure", `"`+opts.Target+`"`)
		if daemonErr == nil {
			daemonErr = waitConfigured(rctx, opts)
		}
		if diskErr != nil || daemonErr != nil {
			return fmt.Errorf("%w; ROLLBACK INCOMPLETE (disk: %v; daemon: %v); previous bytes remain in %s", cause, diskErr, daemonErr, backup)
		}
		if err := clearPending(opts.Target); err != nil {
			return fmt.Errorf("%w; configuration restored but recovery journal cleanup failed: %v", cause, err)
		}
		return fmt.Errorf("%w; previous configuration restored", cause)
	}
	if err = atomicConfig(opts.Target, []byte(config), info); err != nil {
		return rollback(fmt.Errorf("atomic install failed: %w", err))
	}
	if err = control(ctx, opts, "configure", `"`+opts.Target+`"`, "timeout", "60"); err != nil {
		return rollback(err)
	}
	if err = waitConfigured(ctx, opts); err != nil {
		return rollback(err)
	}
	if err = control(ctx, opts, "configure", "confirm"); err != nil {
		return rollback(err)
	}
	return clearPending(opts.Target)
}

func atomicConfig(path string, data []byte, info os.FileInfo) error {
	if err := privateAuthenticationTarget(string(data), info.Mode()); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".moedove-candidate-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if err = f.Chown(int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
	}
	if err = f.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func control(ctx context.Context, opts ApplyOptions, command ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	binary := opts.BirdcBinary
	if binary == "" {
		binary = "birdc"
	}
	args := []string{"-v"}
	if opts.Socket != "" {
		args = append(args, "-s", opts.Socket)
	}
	args = append(args, command...)
	output, err := run(ctx, binary, args...)
	if err != nil {
		return nativeFailure("BIRD control command failed", err)
	}
	return checkControlReply(output, command)
}

var errReconfiguring = errors.New("BIRD reconfiguration is still in progress")

// Numeric responses are defined by BIRD 2 sysdep/unix/main.c. A zero
// process exit is insufficient: e.g. 0006 means the daemon ignored the change.
func checkControlReply(output string, command []string) error {
	expected := 3
	status := len(command) > 1 && command[0] == "configure" && command[1] == "status"
	confirm := len(command) > 1 && command[0] == "configure" && command[1] == "confirm"
	if confirm {
		expected = 18
	}
	matched := false
	pending := false
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 5 || (line[4] != ' ' && line[4] != '-') {
			continue
		}
		code, err := strconv.Atoi(line[:4])
		if err != nil {
			continue
		}
		if code >= 8000 || code == 5 || code == 6 || code == 17 || code == 19 {
			return fmt.Errorf("BIRD rejected control command (code %04d); raw diagnostics withheld", code)
		}
		if code == expected {
			matched = true
		}
		if code == 4 && !confirm {
			if status {
				pending = true
			} else {
				matched = true
			}
		}
	}
	if pending {
		return errReconfiguring
	}
	if !matched {
		return fmt.Errorf("BIRD control response lacks expected success code %04d", expected)
	}
	return nil
}

func waitConfigured(ctx context.Context, opts ApplyOptions) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		err := control(ctx, opts, "configure", "status")
		if !errors.Is(err, errReconfiguring) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}

}

// Recover restores the previous file after an interrupted Apply. The journal
// and snapshot are persisted before installation. A crash can never authorize
// overwriting the last recovery snapshot with an unconfirmed revision.
func Recover(ctx context.Context, opts ApplyOptions) error {
	major, err := birdMajor(opts.BirdMajor)
	if err != nil {
		return err
	}
	if !safePath.MatchString(opts.Target) || filepath.Clean(opts.Target) != opts.Target {
		return errors.New("invalid target path")
	}
	parent := filepath.Dir(opts.Target)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return errors.New("target parent must exist and may not contain symlinks")
	}
	unlock, err := deploymentLock(opts.Target)
	if err != nil {
		return err
	}
	defer unlock()
	marker := opts.Target + ".moedove-pending"
	info, err := os.Lstat(marker)
	if os.IsNotExist(err) {
		return errors.New("no interrupted deployment to recover")
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("recovery journal is not a regular file")
	}
	expected, err := os.ReadFile(marker)
	if err != nil {
		return err
	}
	journalMajor := 2
	if strings.HasPrefix(string(expected), "bird3:") {
		journalMajor = 3
		expected = []byte(strings.TrimPrefix(string(expected), "bird3:"))
	}
	if major != journalMajor {
		return errors.New("recovery journal targets a different BIRD major version")
	}
	backup := opts.Target + ".moedove-previous"
	info, err = os.Lstat(backup)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfigSize {
		return errors.New("recovery snapshot is missing or invalid")
	}
	previous, err := os.ReadFile(backup)
	if err != nil {
		return err
	}
	if len(expected) != 64 || Checksum(string(previous)) != string(expected) {
		return errors.New("recovery snapshot checksum mismatch; manual inspection required")
	}
	if err = privateAuthenticationTarget(string(previous), info.Mode()); err != nil {
		return err
	}
	if err = validateBIRDMajor(ctx, opts.BirdBinary, string(previous), major); err != nil {
		return fmt.Errorf("recovery snapshot parse failed: %w", err)
	}
	if major == 3 {
		if err = requireBIRD3Daemon(ctx, opts); err != nil {
			return err
		}
	}
	if err = atomicConfig(opts.Target, previous, info); err != nil {
		return err
	}
	if err = control(ctx, opts, "configure", `"`+opts.Target+`"`); err != nil {
		return err
	}
	if err = waitConfigured(ctx, opts); err != nil {
		return err
	}
	return clearPending(opts.Target)
}

func deploymentLock(target string) (func(), error) {
	path := target + ".moedove-lock"
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("open deployment lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("deployment lock must be a regular file")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("cannot acquire exclusive deployment lock: %w", err)
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = file.Close() }, nil
}

func writePending(target, checksum string) error {
	return writePendingMajor(target, checksum, 2)
}

func writePendingMajor(target, checksum string, major int) error {
	if major == 3 {
		checksum = "bird3:" + checksum
	}
	f, err := os.OpenFile(target+".moedove-pending", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(checksum); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}
func clearPending(target string) error {
	if err := os.Remove(target + ".moedove-pending"); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
