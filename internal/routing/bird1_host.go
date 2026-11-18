package routing

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type bird1DaemonPin struct {
	PID              int    `json:"pid"`
	StartTime        string `json:"startTime"`
	Executable       string `json:"executable"`
	ExecutableSHA256 string `json:"executableSha256"`
	SocketDevice     uint64 `json:"socketDevice"`
	SocketInode      uint64 `json:"socketInode"`
}
type bird1Reply struct {
	lines []string
	code  int
}
type bird1Host struct {
	opts   BIRD1ApplyOptions
	pin    bird1DaemonPin
	invoke func(context.Context, string) (bird1Reply, error)
}

func bird1PrivateDir(path string) error {
	if !safePath.MatchString(path) || filepath.Clean(path) != path {
		return errors.New("BIRD1 directory must be an explicit clean absolute path")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, e := os.Lstat(current)
		if e != nil {
			return e
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || st.Uid != 0 && st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return errors.New("BIRD1 private directory ancestry is unsafe")
		}
		if current == path && (info.Mode().Perm() != 0700 || st.Uid != uint32(os.Geteuid())) {
			return errors.New("BIRD1 target/socket directory must be owned by this account with mode 0700")
		}
		if current == "/" {
			break
		}
	}
	return nil
}
func bird1PrivateRead(path string, limit int64) ([]byte, os.FileInfo, error) {
	before, e := os.Lstat(path)
	if e != nil {
		return nil, nil, e
	}
	if !before.Mode().IsRegular() {
		return nil, nil, errors.New("BIRD1 private input is not a regular file")
	}
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, nil, e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(before, info) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || info.Size() > limit {
		return nil, nil, errors.New("BIRD1 state/config must be an owned, single-link 0600 regular file")
	}
	raw, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(raw)) > limit {
		return nil, nil, errors.New("BIRD1 private file read failed")
	}
	return raw, info, nil
}
func bird1ExecutableHash(path string, proc bool) (string, error) {
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if !proc {
		flags |= syscall.O_NOFOLLOW
	}
	f, e := os.OpenFile(path, flags, 0)
	if e != nil {
		return "", errors.New("BIRD1 executable is unavailable")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || st.Size() > 128<<20 {
		return "", errors.New("BIRD1 executable is not a bounded trusted regular file")
	}
	hash := sha256.New()
	if _, e = io.Copy(hash, io.LimitReader(f, (128<<20)+1)); e != nil {
		return "", errors.New("BIRD1 executable digest unavailable")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func newBird1Host(opts BIRD1ApplyOptions) (*bird1Host, error) {
	executable, e := filepath.EvalSymlinks(opts.BirdBinary)
	if e != nil || executable != opts.BirdBinary {
		return nil, errors.New("BIRD1 checker must be an explicit canonical native executable path")
	}
	hash, e := bird1ExecutableHash(executable, false)
	if e != nil {
		return nil, e
	}
	host := &bird1Host{opts: opts, pin: bird1DaemonPin{Executable: executable, ExecutableSHA256: hash}}
	host.invoke = host.roundTrip
	return host, nil
}
func (h *bird1Host) verifyProcess(pid int) error {
	base := fmt.Sprintf("/proc/%d", pid)
	executable, e := os.Readlink(base + "/exe")
	if e != nil || executable != h.pin.Executable {
		return errors.New("BIRD1 control peer executable differs from the native checker")
	}
	hash, e := bird1ExecutableHash(base+"/exe", true)
	if e != nil || hash != h.pin.ExecutableSHA256 {
		return errors.New("BIRD1 daemon executable bytes changed")
	}
	current, e := bird1ExecutableHash(h.pin.Executable, false)
	if e != nil || current != h.pin.ExecutableSHA256 {
		return errors.New("BIRD1 native checker changed during the transaction")
	}
	raw, e := os.ReadFile(base + "/stat")
	if e != nil {
		return errors.New("BIRD1 process generation unavailable")
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return errors.New("BIRD1 process identity unavailable")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return errors.New("BIRD1 control peer is not live")
	}
	start := fields[19]
	if _, e = strconv.ParseUint(start, 10, 64); e != nil || h.pin.PID != 0 && (h.pin.PID != pid || h.pin.StartTime != start) {
		return errors.New("BIRD1 daemon generation changed; explicit recovery required")
	}
	own, e := os.Stat("/proc/self/ns/net")
	if e != nil {
		return e
	}
	remote, e := os.Stat(base + "/ns/net")
	if e != nil || !os.SameFile(own, remote) {
		return errors.New("BIRD1 daemon is in a different network namespace")
	}
	raw, e = os.ReadFile(base + "/cmdline")
	if e != nil || len(raw) > 8192 {
		return errors.New("BIRD1 daemon arguments unavailable")
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		option := args[i]
		if option == "-f" {
			if seen[option] {
				return errors.New("duplicate BIRD1 process option")
			}
			seen[option] = true
			continue
		}
		if option != "-c" && option != "-s" && option != "-P" || seen[option] || i+1 >= len(args) {
			return errors.New("BIRD1 daemon must use only optional -f, explicit -c/-s and a private optional -P")
		}
		seen[option] = true
		i++
		value := args[i]
		if option == "-c" && value != h.opts.Target || option == "-s" && value != h.opts.Socket || option == "-P" && (!safePath.MatchString(value) || filepath.Clean(value) != value || bird1PrivateDir(filepath.Dir(value)) != nil) {
			return errors.New("BIRD1 daemon does not name the exact dedicated target/socket")
		}
	}
	if !seen["-c"] || !seen["-s"] {
		return errors.New("BIRD1 daemon requires explicit dedicated -c target and -s socket")
	}
	if h.pin.PID == 0 {
		h.pin.PID = pid
		h.pin.StartTime = start
	}
	return nil
}
func (h *bird1Host) roundTrip(ctx context.Context, command string) (bird1Reply, error) {
	var out bird1Reply
	if e := bird1PrivateDir(filepath.Dir(h.opts.Socket)); e != nil {
		return out, e
	}
	info, e := os.Lstat(h.opts.Socket)
	if e != nil {
		return out, errors.New("BIRD1 private control socket unavailable")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0007 != 0 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return out, errors.New("BIRD1 socket is not a private owned endpoint")
	}
	if h.pin.SocketInode != 0 && (h.pin.SocketDevice != uint64(st.Dev) || h.pin.SocketInode != st.Ino) {
		return out, errors.New("BIRD1 control socket identity changed")
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, e := (&net.Dialer{Timeout: time.Second}).DialContext(call, "unix", h.opts.Socket)
	if e != nil {
		return out, errors.New("BIRD1 private control connection failed")
	}
	defer connection.Close()
	stop := context.AfterFunc(call, func() { connection.Close() })
	defer stop()
	deadline, _ := call.Deadline()
	if e = connection.SetDeadline(deadline); e != nil {
		return out, e
	}
	unix, ok := connection.(*net.UnixConn)
	if !ok {
		return out, errors.New("BIRD1 control is not Unix transport")
	}
	raw, e := unix.SyscallConn()
	if e != nil {
		return out, e
	}
	var credentials *syscall.Ucred
	var inspect error
	e = raw.Control(func(fd uintptr) {
		credentials, inspect = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if e != nil || inspect != nil || credentials == nil || credentials.Pid <= 0 || credentials.Uid != uint32(os.Geteuid()) {
		return out, errors.New("BIRD1 control peer account cannot be verified")
	}
	if e = h.verifyProcess(int(credentials.Pid)); e != nil {
		return out, e
	}
	if h.pin.SocketInode == 0 {
		h.pin.SocketDevice = uint64(st.Dev)
		h.pin.SocketInode = st.Ino
	}
	reader := bufio.NewReader(io.LimitReader(connection, 65537))
	greeting, e := reader.ReadString('\n')
	if e != nil || !strings.HasPrefix(greeting, "0001 BIRD 1.") || !strings.HasSuffix(greeting, " ready.\n") {
		return out, errors.New("BIRD1 control greeting does not identify the expected major")
	}
	if _, e = io.WriteString(connection, command+"\n"); e != nil {
		return out, errors.New("BIRD1 command result unknown")
	}
	size := len(greeting)
	for {
		line, e := reader.ReadString('\n')
		size += len(line)
		if e != nil || size > 65536 {
			return out, errors.New("BIRD1 control reply incomplete or exceeded bound")
		}
		line = strings.TrimSuffix(line, "\n")
		out.lines = append(out.lines, line)
		if strings.HasPrefix(line, " ") {
			continue
		}
		if len(line) < 5 || (line[4] != ' ' && line[4] != '-') {
			return out, errors.New("BIRD1 control framing invalid")
		}
		code, e := strconv.Atoi(line[:4])
		if e != nil {
			return out, errors.New("BIRD1 control response code invalid")
		}
		if code >= 8000 {
			return out, fmt.Errorf("BIRD1 rejected control request (code %04d); raw diagnostics withheld", code)
		}
		if line[4] == ' ' && code < 1000 {
			out.code = code
			break
		}
	}
	if e = h.verifyProcess(int(credentials.Pid)); e != nil {
		return out, e
	}
	return out, nil
}
func (h *bird1Host) ready(ctx context.Context) error {
	reply, e := h.invoke(ctx, "show status")
	if e != nil {
		return e
	}
	version := false
	for _, line := range reply.lines {
		if strings.HasPrefix(line, "1000-BIRD 1.") {
			version = true
		}
	}
	if !version || reply.code != 13 {
		return errors.New("BIRD1 readiness response is not recognized")
	}
	last := reply.lines[len(reply.lines)-1]
	if last == "0013 Reconfiguration in progress" {
		return errReconfiguring
	}
	if last != "0013 Daemon is up and running" {
		return errors.New("BIRD1 daemon is not ready")
	}
	return nil
}
func (h *bird1Host) readback(ctx context.Context) (string, error) {
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		e := h.ready(call)
		if errors.Is(e, errReconfiguring) {
			select {
			case <-call.Done():
				return "", call.Err()
			case <-time.After(50 * time.Millisecond):
				continue
			}
		}
		if e != nil {
			return "", e
		}
		reply, e := h.invoke(call, "eval md_managed_checksum")
		if e != nil {
			return "", e
		}
		if reply.code != 23 || len(reply.lines) != 1 || !bird1HashOK(strings.TrimPrefix(reply.lines[0], "0023 ")) {
			return "", errors.New("BIRD1 running configuration lacks a reviewed v2 fingerprint")
		}
		fingerprint := strings.TrimPrefix(reply.lines[0], "0023 ")
		if e = h.ready(call); errors.Is(e, errReconfiguring) {
			continue
		} else if e != nil {
			return "", e
		}
		again, e := h.invoke(call, "eval md_managed_checksum")
		if e != nil {
			return "", e
		}
		if again.code != 23 || len(again.lines) != 1 || !bird1HashOK(strings.TrimPrefix(again.lines[0], "0023 ")) {
			return "", errors.New("BIRD1 runtime readback is not a canonical fingerprint")
		}
		if strings.TrimPrefix(again.lines[0], "0023 ") != fingerprint {
			continue
		}
		return fingerprint, nil
	}
}
func (h *bird1Host) check(ctx context.Context, path string) error {
	reply, e := h.invoke(ctx, "configure check \""+path+"\"")
	if e != nil {
		return e
	}
	if reply.code != 20 {
		return errors.New("BIRD1 native check lacks explicit success")
	}
	return nil
}
func (h *bird1Host) configure(ctx context.Context, timed bool) error {
	path := h.opts.Target
	if timed {
		path += ".moedove-next"
	}
	command := "configure \"" + path + "\""
	if timed {
		command += " timeout " + strconv.Itoa(h.opts.TimeoutSeconds)
	}
	reply, e := h.invoke(ctx, command)
	if e != nil {
		return e
	}
	if reply.code != 3 && reply.code != 4 {
		return errors.New("BIRD1 reconfiguration was queued, ignored or not accepted")
	}
	if timed {
		found := false
		want := "0022-Undo scheduled in " + strconv.Itoa(h.opts.TimeoutSeconds) + " s"
		for _, line := range reply.lines {
			found = found || line == want
		}
		if !found {
			return errors.New("BIRD1 did not acknowledge the requested native undo deadline")
		}
	}
	return nil
}
func (h *bird1Host) confirm(ctx context.Context) error {
	reply, e := h.invoke(ctx, "configure confirm")
	if e != nil {
		return e
	}
	if reply.code != 18 {
		return errors.New("BIRD1 confirmation was not explicitly acknowledged")
	}
	return nil
}
