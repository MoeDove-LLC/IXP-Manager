package netconf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type SSHOptions struct {
	Host       string `json:"host"`
	User       string `json:"user"`
	Port       int    `json:"port"`
	KnownHosts string `json:"knownHosts"`
	Identity   string `json:"identity"`
}

var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)
var userPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)

func SSHArgs(o SSHOptions) ([]string, error) {
	if net.ParseIP(o.Host) == nil && !hostPattern.MatchString(o.Host) {
		return nil, errors.New("invalid SSH hostname or IP")
	}
	if !userPattern.MatchString(o.User) || o.Port < 1 || o.Port > 65535 {
		return nil, errors.New("invalid SSH user or port")
	}
	for _, path := range []string{o.KnownHosts, o.Identity} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, " \t\n\r\x00%$\"\\") {
			return nil, errors.New("SSH identity and known_hosts must be absolute paths")
		}
		st, e := os.Lstat(path)
		if e != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			return nil, errors.New("SSH identity and pinned known_hosts must be nonempty regular files")
		}
	}
	return []string{"-F", "/dev/null", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + o.KnownHosts, "-o", "GlobalKnownHostsFile=/dev/null", "-o", "IdentitiesOnly=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "ProxyCommand=none", "-o", "ProxyJump=none", "-o", "CanonicalizeHostname=no", "-o", "ConnectTimeout=15", "-i", o.Identity, "-p", strconv.Itoa(o.Port), "-l", o.User, "-s", "--", o.Host, "netconf"}, nil
}

type sshTransport struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func (t *sshTransport) Read(p []byte) (int, error)  { return t.out.Read(p) }
func (t *sshTransport) Write(p []byte) (int, error) { return t.in.Write(p) }
func (t *sshTransport) Close() error {
	_ = t.in.Close()
	_ = t.out.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	return nil
}
func DialSSH(ctx context.Context, o SSHOptions) (*Client, error) {
	args, err := SSHArgs(o)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Remote subsystem stderr is unstructured and may echo configuration secrets.
	// Never forward it to a terminal, journal or the caller's logs.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("SSH transport: %w", err)
	}
	t := &sshTransport{cmd: cmd, in: in, out: out}
	c, err := Open(t)
	if err != nil {
		_ = t.Close()
		return nil, err
	}
	return c, nil
}
