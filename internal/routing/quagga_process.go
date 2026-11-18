package routing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const quaggaNativeSize = 64 << 20

type quaggaHost struct {
	opts   QuaggaOptions
	bootID string
	socket string
	zebra  string
	binary *os.File
	dir    os.FileInfo
}

type quaggaProcessReceipt struct {
	BootID     string `json:"bootId"`
	PID        int    `json:"pid"`
	Generation string `json:"generation"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
}

type quaggaChild struct {
	command     *exec.Cmd
	done        chan struct{}
	pidFile     *os.File
	receipt     quaggaProcessReceipt
	fingerprint string
}

func quaggaPrivateRead(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > limit || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, errors.New("Quagga state requires owned single-link regular files with mode 0600")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || !os.SameFile(info, after) || int64(len(raw)) != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || after.Mode() != info.Mode() {
		return nil, errors.New("Quagga private file changed or exceeded its limit")
	}
	return raw, nil
}

func quaggaRead(path string, size int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil || int64(len(raw)) > size {
		return nil, errors.New("bounded Quagga process inspection failed")
	}
	return raw, nil
}

func quaggaConfined(raw []byte) bool {
	fields := map[string]string{}
	for _, row := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(row, ":")
		if ok {
			fields[key] = strings.TrimSpace(value)
		}
	}
	uid := strconv.Itoa(os.Geteuid())
	if len(strings.Fields(fields["Uid"])) != 4 || fields["NoNewPrivs"] != "1" {
		return false
	}
	for _, value := range strings.Fields(fields["Uid"]) {
		if value != uid {
			return false
		}
	}
	for _, key := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		value, err := strconv.ParseUint(fields[key], 16, 64)
		if err != nil || value != 0 {
			return false
		}
	}
	return true
}

func newQuaggaHost(opts QuaggaOptions) (*quaggaHost, error) {
	status, err := quaggaRead("/proc/self/status", 64<<10)
	if err != nil || !quaggaConfined(status) {
		return nil, errors.New("Quagga supervisor requires a single UID, no capabilities and NoNewPrivs")
	}
	boot, err := quaggaRead("/proc/sys/kernel/random/boot_id", 128)
	if err != nil || strings.TrimSpace(string(boot)) == "" {
		return nil, errors.New("Linux boot identity unavailable")
	}
	dir, err := os.Lstat(opts.StateDir)
	if err != nil || !dir.IsDir() || dir.Mode().Perm() != 0700 {
		return nil, errors.New("private Quagga state directory is missing")
	}
	resolved, err := filepath.EvalSymlinks(opts.Binary)
	if err != nil || resolved != opts.Binary {
		return nil, errors.New("Quagga executable must have an explicit non-symlink path")
	}
	file, err := os.OpenFile(opts.Binary, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	h := &quaggaHost{opts: opts, bootID: strings.TrimSpace(string(boot)), binary: file, dir: dir, socket: filepath.Join(opts.StateDir, "bgpd.vty"), zebra: filepath.Join(opts.StateDir, "no-zebra.sock")}
	if err = h.verifyBinary(); err != nil {
		file.Close()
		return nil, err
	}
	// Stock 1.2.4 uses BGP_VTYSH_PATH and unlinks that path at startup. Require
	// its only compiled VTY socket name to belong to our private directory.
	binary, err := elf.NewFile(file)
	if err != nil {
		file.Close()
		return nil, errors.New("Quagga executable must be a stock-compatible ELF binary")
	}
	data := binary.Section(".rodata")
	if data == nil || data.Size > quaggaNativeSize {
		file.Close()
		return nil, errors.New("Quagga executable has no bounded VTY path metadata")
	}
	raw, err := data.Data()
	if err != nil || !quaggaSocketPath(raw, h.socket) {
		file.Close()
		return nil, errors.New("Quagga must be built for exactly this private bgpd.vty path")
	}
	return h, nil
}

func quaggaSocketPath(raw []byte, expected string) bool {
	found := false
	for len(raw) > 0 {
		value, rest, terminated := bytes.Cut(raw, []byte{0})
		if terminated && bytes.HasSuffix(value, []byte("/bgpd.vty")) {
			if string(value) != expected {
				return false
			}
			found = true
		}
		raw = rest
	}
	return found
}

func (h *quaggaHost) verifyBinary() error {
	if err := h.verifyDir(); err != nil {
		return err
	}
	info, err := h.binary.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || info.Size() < 1 || info.Size() > quaggaNativeSize {
		return errors.New("Quagga executable permissions or size invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	named, err := os.Lstat(h.opts.Binary)
	if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || err != nil || !os.SameFile(info, named) {
		return errors.New("Quagga executable identity changed")
	}
	hash := sha256.New()
	if count, err := io.Copy(hash, io.NewSectionReader(h.binary, 0, info.Size())); err != nil || count != info.Size() || hex.EncodeToString(hash.Sum(nil)) != h.opts.BinaryChecksum {
		return errors.New("Quagga executable checksum changed")
	}
	return nil
}

func (h *quaggaHost) verifyDir() error {
	info, err := os.Lstat(h.opts.StateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !os.SameFile(info, h.dir) {
		return errors.New("Quagga state directory identity or permissions changed")
	}
	return nil
}

func (h *quaggaHost) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", args...)
	cmd.Args[0] = h.opts.Binary
	cmd.ExtraFiles = []*os.File{h.binary}
	return cmd
}

func (h *quaggaHost) validate(ctx context.Context, config string) error {
	if err := h.verifyBinary(); err != nil {
		return err
	}
	return validateQuaggaConfig(ctx, config, func(ctx context.Context, dir string, args ...string) (string, error) {
		// The exclusively owned instance already has zero capabilities and
		// NoNewPrivs. Its probe binds only 127.0.0.1:65535, exits before creating
		// VTY/PID files, and never opens the live BGP port or the Zebra socket.
		return runQuaggaCommand(ctx, h.command(ctx, args...), dir, false)
	})
}

func (h *quaggaHost) unlinkedFile(config []byte) (*os.File, error) {
	file, err := os.CreateTemp(h.opts.StateDir, ".process-*")
	if err != nil {
		return nil, err
	}
	if err = os.Remove(file.Name()); err == nil && config != nil {
		_, err = file.Write(config)
	}
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func quaggaGeneration(pid int) (id, status string, err error) {
	raw, err := quaggaRead(fmt.Sprintf("/proc/%d/stat", pid), 64<<10)
	if err != nil {
		return "", "", err
	}
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return "", "", errors.New("invalid Quagga process generation")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return "", "", errors.New("missing Quagga process generation")
	}
	if _, err = strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", "", errors.New("invalid Quagga process start time")
	}
	return fields[19], fields[0], nil
}

func (h *quaggaHost) identity(child *quaggaChild) error {
	pid := child.command.Process.Pid
	start, state, err := quaggaGeneration(pid)
	if err != nil || start != child.receipt.Generation || state == "Z" {
		return errors.New("Quagga child generation changed or exited")
	}
	base := fmt.Sprintf("/proc/%d/", pid)
	info, err := os.Stat(base + "exe")
	pinned, pinErr := h.binary.Stat()
	if err != nil || pinErr != nil || !os.SameFile(info, pinned) {
		return errors.New("Quagga child executable identity changed")
	}
	argv, err := quaggaRead(base+"cmdline", 8192)
	status, statErr := quaggaRead(base+"status", 64<<10)
	if err != nil || statErr != nil || !quaggaConfined(status) || !slices.Equal(strings.Split(strings.TrimSuffix(string(argv), "\x00"), "\x00"), child.command.Args) {
		return errors.New("Quagga child UID, confinement or arguments changed")
	}
	return nil
}

func (h *quaggaHost) listeners() (map[string]bool, error) {
	listeners := map[string]bool{}
	for _, family := range []string{"tcp", "tcp6"} {
		raw, err := quaggaRead("/proc/net/"+family, 4<<20)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			if len(f) < 10 || f[3] != "0A" {
				continue
			}
			_, port, _ := strings.Cut(f[1], ":")
			if n, err := strconv.ParseUint(port, 16, 16); err == nil && int(n) == h.opts.BGPPort {
				listeners[f[9]] = family == "tcp6"
			}
		}
	}
	return listeners, nil
}

func quaggaOwnedListeners(pid int, listeners map[string]bool) bool {
	if len(listeners) != 2 {
		return false
	}
	base := fmt.Sprintf("/proc/%d/fd", pid)
	fds, err := os.ReadDir(base)
	if err != nil || len(fds) > 65536 {
		return false
	}
	owned := [2]bool{}
	for _, fd := range fds {
		link, _ := os.Readlink(filepath.Join(base, fd.Name()))
		if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
			if v6, found := listeners[link[8:len(link)-1]]; found {
				if v6 {
					owned[1] = true
				} else {
					owned[0] = true
				}
			}
		}
	}
	return owned[0] && owned[1]
}

func quaggaSocketIdentity(path string) (*syscall.Stat_t, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0007 != 0 || stat.Nlink != 1 {
		return nil, errors.New("Quagga VTY must be an owned private Unix socket")
	}
	return stat, nil
}

func (h *quaggaHost) dial(ctx context.Context, pid int) (*net.UnixConn, *syscall.Stat_t, error) {
	stat, err := quaggaSocketIdentity(h.socket)
	if err != nil {
		return nil, nil, err
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", h.socket)
	if err != nil {
		return nil, nil, err
	}
	unix := conn.(*net.UnixConn)
	fd, err := unix.SyscallConn()
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	var credentials *syscall.Ucred
	var inspect error
	err = fd.Control(func(raw uintptr) {
		credentials, inspect = syscall.GetsockoptUcred(int(raw), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	after, statErr := quaggaSocketIdentity(h.socket)
	if err != nil || inspect != nil || statErr != nil || after.Ino != stat.Ino || after.Dev != stat.Dev || credentials == nil || int(credentials.Pid) != pid || credentials.Uid != uint32(os.Geteuid()) {
		conn.Close()
		return nil, nil, errors.New("Quagga VTY does not belong to the owned foreground child")
	}
	return unix, stat, nil
}

func (h *quaggaHost) fingerprint(ctx context.Context, child *quaggaChild) (string, error) {
	conn, stat, err := h.dial(ctx, child.receipt.PID)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if stat.Ino != child.receipt.Inode || stat.Dev != child.receipt.Device {
		return "", errors.New("Quagga VTY inode changed")
	}
	if err = quaggaSetVTYDeadline(ctx, conn); err != nil {
		return "", err
	}
	// A new native VTY-shell starts in VIEW_NODE. This fixed command changes
	// only the local VTY command context, allowing the read-only config query.
	if _, err = io.WriteString(conn, "enable\x00"); err != nil {
		return "", errors.New("Quagga private VTY session unavailable")
	}
	ack, err := quaggaVTYReply(conn, 4)
	if err != nil || len(ack) != 0 {
		return "", errors.New("Quagga private VTY session did not accept its required command context")
	}
	if _, err = io.WriteString(conn, "show running-config\x00"); err != nil {
		return "", errors.New("Quagga read-only VTY command unavailable")
	}
	data, err := quaggaVTYReply(conn, maxConfigSize)
	defer clear(data)
	if err != nil {
		return "", err
	}
	if !bytes.Contains(data, []byte(" view MOEDOVE_QUAGGA\n")) {
		return "", errors.New("Quagga VTY did not return the dedicated route-server view")
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func quaggaSetVTYDeadline(ctx context.Context, conn net.Conn) error {
	deadline := time.Now().Add(time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	return conn.SetDeadline(deadline)
}

func (h *quaggaHost) peerReady(ctx context.Context, child *quaggaChild, address string) bool {
	conn, stat, err := h.dial(ctx, child.receipt.PID)
	if err != nil {
		return false
	}
	defer conn.Close()
	if stat.Ino != child.receipt.Inode || stat.Dev != child.receipt.Device || quaggaSetVTYDeadline(ctx, conn) != nil {
		return false
	}
	// address came from the exact parsed managed Quagga profile.
	if _, err = io.WriteString(conn, "show ip bgp view MOEDOVE_QUAGGA neighbors "+address+"\x00"); err != nil {
		return false
	}
	text, err := quaggaVTYReply(conn, 64<<10)
	return err == nil && (bytes.Contains(text, []byte("BGP state = Active")) || bytes.Contains(text, []byte("BGP state = Connect")) || bytes.Contains(text, []byte("BGP state = Established")))
}

func quaggaVTYReply(conn net.Conn, limit int) ([]byte, error) {
	data := make([]byte, 0, 8192)
	var buffer [4096]byte
	for len(data) < limit {
		n, err := conn.Read(buffer[:])
		data = append(data, buffer[:n]...)
		if end := bytes.Index(data, []byte{0, 0, 0}); end >= 0 && len(data) >= end+4 {
			if end+4 <= limit && end+4 == len(data) && data[end+3] == 0 {
				return data[:end], nil
			}
			break
		}
		if err != nil {
			break
		}
	}
	clear(data)
	return nil, errors.New("Quagga VTY did not acknowledge its bounded command")
}

func (h *quaggaHost) verify(ctx context.Context, child *quaggaChild) error {
	if err := h.verifyBinary(); err != nil {
		return err
	}
	if child == nil || child.fingerprint == "" || h.identity(child) != nil {
		return errors.New("owned Quagga daemon is not ready")
	}
	listeners, err := h.listeners()
	if err != nil || !quaggaOwnedListeners(child.receipt.PID, listeners) {
		return errors.New("Quagga no longer owns the dedicated dual-family listeners")
	}
	fingerprint, err := h.fingerprint(ctx, child)
	if err != nil || fingerprint != child.fingerprint {
		return errors.New("Quagga running configuration drift; operator inspection required")
	}
	return h.identity(child)
}

func (h *quaggaHost) clearStaleSocket() error {
	if err := h.verifyDir(); err != nil {
		return err
	}
	stat, err := quaggaSocketIdentity(h.socket)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := quaggaPrivateRead(filepath.Join(h.opts.StateDir, "process.json"), 4096)
	var receipt quaggaProcessReceipt
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.BootID == "" || receipt.PID < 1 || receipt.Generation == "" || stat.Dev != receipt.Device || stat.Ino != receipt.Inode {
		return errors.New("unrecognized Quagga VTY socket; will not remove it")
	}
	if receipt.BootID == h.bootID {
		generation, status, err := quaggaGeneration(receipt.PID)
		if (err != nil && !os.IsNotExist(err)) || (err == nil && generation == receipt.Generation && status != "Z") {
			return errors.New("previous Quagga daemon may still be alive; will not take it over")
		}
	}
	conn, err := net.DialTimeout("unix", h.socket, 100*time.Millisecond)
	if err == nil {
		conn.Close()
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return errors.New("previous Quagga VTY closure could not be proven")
	}
	latest, err := quaggaSocketIdentity(h.socket)
	if err != nil || latest.Ino != stat.Ino || latest.Dev != stat.Dev {
		return errors.New("Quagga VTY path changed during stale socket inspection")
	}
	return os.Remove(h.socket)
}

func (h *quaggaHost) start(ctx context.Context, config []byte) (*quaggaChild, error) {
	model, err := parseManagedQuagga(string(config))
	if err != nil {
		return nil, err
	}
	// A successful configuration query can precede the initial peer FSM.
	// Require one actual started peer per configured family before returning.
	var peers [2]string
	for _, peer := range model.Peers {
		index := 0
		if strings.Contains(peer.Address, ":") {
			index = 1
		}
		if peers[index] == "" {
			peers[index] = peer.Address
		}
	}
	if err := h.verifyBinary(); err != nil {
		return nil, err
	}
	listeners, err := h.listeners()
	if err != nil || len(listeners) != 0 {
		return nil, errors.New("Quagga BGP port already occupied; will not adopt another daemon")
	}
	if _, err = os.Lstat(h.zebra); !os.IsNotExist(err) {
		return nil, errors.New("Quagga requires its private Zebra path to remain absent")
	}
	if err = h.clearStaleSocket(); err != nil {
		return nil, err
	}
	sealed, err := h.unlinkedFile(config)
	if err != nil {
		return nil, err
	}
	defer sealed.Close()
	pidFile, err := h.unlinkedFile(nil)
	if err != nil {
		return nil, err
	}
	cmd := h.command(context.Background(), "--skip_runas", "--no_kernel", "--bgp_port", strconv.Itoa(h.opts.BGPPort), "--vty_port", "0", "--socket", h.zebra, "--pid_file", "/proc/self/fd/5", "--config_file", "/proc/self/fd/4")
	cmd.Dir = h.opts.StateDir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + h.opts.StateDir, "TMPDIR=" + h.opts.StateDir, "LANG=C", "LC_ALL=C"}
	cmd.ExtraFiles = append(cmd.ExtraFiles, sealed, pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	child := &quaggaChild{command: cmd, done: make(chan struct{}), pidFile: pidFile}
	started := make(chan error, 1)
	go func() {
		// Linux binds Pdeathsig to the thread that created the child. Keep this
		// thread alive through Wait so that a supervisor crash kills its child.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(child.done)
		err := cmd.Start()
		started <- err
		if err == nil {
			_ = cmd.Wait()
		}
	}()
	if err := <-started; err != nil {
		pidFile.Close()
		return nil, errors.New("cannot start pinned Quagga daemon")
	}
	pid := cmd.Process.Pid
	generation, _, err := quaggaGeneration(pid)
	if err != nil {
		_ = cmd.Process.Kill()
		<-child.done
		pidFile.Close()
		return nil, errors.New("could not bind new Quagga process generation")
	}
	child.receipt = quaggaProcessReceipt{BootID: h.bootID, PID: pid, Generation: generation}
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	failure := errors.New("private Quagga VTY has not bound to its child")
	for ready.Err() == nil {
		if err := h.identity(child); err != nil {
			failure = err
			break
		}
		if child.receipt.Inode == 0 {
			if connection, stat, err := h.dial(ready, pid); err == nil {
				connection.Close()
				child.receipt.Device, child.receipt.Inode = stat.Dev, stat.Ino
				raw, _ := json.Marshal(child.receipt)
				if err = writePrivate(filepath.Join(h.opts.StateDir, "process.json"), raw); err != nil {
					return child, errors.New("cannot durably bind the Quagga child socket")
				}
			}
		}
		if child.receipt.Inode != 0 {
			listeners, err := h.listeners()
			var pidRaw [32]byte
			n, _ := child.pidFile.ReadAt(pidRaw[:], 0)
			switch {
			case err != nil || !quaggaOwnedListeners(pid, listeners):
				failure = errors.New("Quagga did not exclusively bind both BGP listeners")
			case strings.TrimSpace(string(pidRaw[:n])) != strconv.Itoa(pid):
				failure = errors.New("Quagga PID writer did not bind to the owned child")
			default:
				if fingerprint, err := h.fingerprint(ready, child); err == nil {
					if (peers[0] != "" && !h.peerReady(ready, child, peers[0])) || (peers[1] != "" && !h.peerReady(ready, child, peers[1])) {
						failure = errors.New("Quagga initial peer state not yet initialized")
						break
					}
					child.fingerprint = fingerprint
					return child, h.verify(ready, child)
				} else {
					failure = err
				}
			}
		}
		select {
		case <-child.done:
			return child, errors.New("Quagga exited before readiness")
		case <-ready.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
	return child, fmt.Errorf("Quagga readiness unverified: %w", failure)
}

func (h *quaggaHost) stop(child *quaggaChild) error {
	if child == nil {
		return nil
	}
	select {
	case <-child.done:
	default:
		if err := h.identity(child); err != nil && !errors.Is(child.command.Process.Signal(syscall.Signal(0)), os.ErrProcessDone) {
			return err
		}
		if err := child.command.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return errors.New("cannot stop owned Quagga daemon")
		}
		select {
		case <-child.done:
		case <-time.After(3 * time.Second):
			if err := h.identity(child); err != nil {
				return err
			}
			if err := child.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return errors.New("owned Quagga daemon shutdown unverified")
			}
			select {
			case <-child.done:
			case <-time.After(3 * time.Second):
				return errors.New("owned Quagga daemon exit unverified")
			}
		}
	}
	child.pidFile.Close()
	return h.clearStaleSocket()
}
