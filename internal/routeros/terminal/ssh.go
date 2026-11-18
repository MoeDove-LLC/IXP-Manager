package terminal

import (
	"context"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type SSHOptions struct {
	Host       string
	User       string
	Port       int
	KnownHosts string
	Identity   string
}

var hostname = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,252}$`)
var username = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,63}$`)

func credentialFile(path string, private bool) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, " \t\r\n\x00%$~\"'\\") {
		return false
	}
	// Reject symlinked path components; ssh must open exactly the configured
	// local credential files, not an ambient path alias or expansion token.
	for at := path; at != "/"; at = filepath.Dir(at) {
		st, err := os.Lstat(at)
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if at == path {
			if !st.Mode().IsRegular() || st.Size() < 1 || st.Size() > 4<<20 || st.Mode().Perm()&022 != 0 {
				return false
			}
			if private && (st.Mode().Perm()&077 != 0 || st.Size() > 64<<10) {
				return false
			}
		} else if !st.IsDir() {
			return false
		}
	}
	return true
}

// SSHArgs always requests an interactive remote PTY and never supplies a remote
// shell command. RouterOS login suffix disables color/detection and fixes width.
func SSHArgs(o SSHOptions) ([]string, error) {
	ip, ipErr := netip.ParseAddr(o.Host)
	if o.Host == "" || strings.ContainsAny(o.Host, "\x00\r\n \t%") || (ipErr != nil && !hostname.MatchString(o.Host)) || (ipErr == nil && (ip.Zone() != "" || ip.Is4In6() || ip.IsUnspecified() || ip.IsMulticast())) || !username.MatchString(o.User) || o.Port < 1 || o.Port > 65535 || !credentialFile(o.KnownHosts, false) || !credentialFile(o.Identity, true) {
		return nil, failure(InvalidInput)
	}
	args := []string{"-F", "/dev/null", "-tt", "-e", "none"}
	for _, option := range []string{
		"BatchMode=yes", "RequestTTY=force", "StrictHostKeyChecking=yes", "UserKnownHostsFile=" + o.KnownHosts, "GlobalKnownHostsFile=/dev/null", "UpdateHostKeys=no",
		"IdentitiesOnly=yes", "IdentityAgent=none", "AddKeysToAgent=no", "ForwardAgent=no", "ClearAllForwardings=yes", "ExitOnForwardFailure=yes",
		"PermitLocalCommand=no", "ProxyCommand=none", "ProxyJump=none", "CanonicalizeHostname=no", "ControlMaster=no", "ControlPath=none", "ControlPersist=no",
		"PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "PreferredAuthentications=publickey", "PubkeyAuthentication=yes", "NumberOfPasswordPrompts=0",
		"ConnectTimeout=10", "ConnectionAttempts=1", "ServerAliveInterval=5", "ServerAliveCountMax=2", "LogLevel=ERROR",
	} {
		args = append(args, "-o", option)
	}
	args = append(args, "-i", o.Identity, "-p", strconv.Itoa(o.Port), "-l", o.User+"+ct200w60h", "--", o.Host)
	return args, nil
}

type sshSession struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	once sync.Once
}

type stderrLimit struct {
	size int
	stop func()
}

func (w *stderrLimit) Write(p []byte) (int, error) {
	if len(p) > 4096-w.size {
		if w.stop != nil {
			w.stop()
		}
		return 0, failure(BoundsExceeded)
	}
	w.size += len(p)
	return len(p), nil
}

func (s *sshSession) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *sshSession) Write(p []byte) (int, error) { return s.in.Write(p) }
func (s *sshSession) Close() error {
	s.once.Do(func() {
		_ = s.in.Close()
		_ = s.out.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		_ = s.cmd.Wait()
	})
	return nil
}

func sshCommand(ctx context.Context, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LANG=C", "LC_ALL=C", "TERM=dumb", "SSH_ASKPASS_REQUIRE=never"}
	cmd.Dir = "/"
	cmd.Stderr = &stderrLimit{stop: func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}}
	return cmd
}

// DialSSH creates one persistent interactive channel. Context cancellation
// closes the channel; it never retries/reconnects a Safe Mode session.
func DialSSH(ctx context.Context, o SSHOptions, l Limits) (*Client, error) {
	args, err := SSHArgs(o)
	if err != nil {
		return nil, err
	}
	if _, err = checkedLimits(l); err != nil {
		return nil, err
	}
	cmd := sshCommand(ctx, args)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, failure(TransportFailure)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, failure(TransportFailure)
	}
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return nil, failure(TransportFailure)
	}
	transport := &sshSession{cmd: cmd, in: in, out: out}
	c, err := Open(transport, l)
	if err != nil {
		_ = transport.Close()
		return nil, err
	}
	return c, nil
}
