package terminal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sshFixture(t *testing.T) SSHOptions {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "identity")
	hosts := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(key, []byte("private-test-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hosts, []byte("192.0.2.1 ssh-ed25519 synthetic-test-public-key"), 0644); err != nil {
		t.Fatal(err)
	}
	return SSHOptions{Host: "192.0.2.1", User: "test_admin", Port: 22, Identity: key, KnownHosts: hosts}
}
func TestSSHUsesFixedInteractiveChannelAndNoAmbientAuthority(t *testing.T) {
	o := sshFixture(t)
	args, err := SSHArgs(o)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(args, "\n")
	for _, required := range []string{"-tt", "-F\n/dev/null", "StrictHostKeyChecking=yes", "IdentityAgent=none", "IdentitiesOnly=yes", "ProxyCommand=none", "ProxyJump=none", "ControlPath=none", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "ForwardAgent=no", "UserKnownHostsFile=" + o.KnownHosts, "GlobalKnownHostsFile=/dev/null", "test_admin+ct200w60h"} {
		if !strings.Contains(text, required) {
			t.Fatal("missing SSH restriction", required)
		}
	}
	if len(args) < 2 || args[len(args)-2] != "--" || args[len(args)-1] != o.Host {
		t.Fatal("unexpected remote shell command")
	}
	t.Setenv("SSH_AUTH_SOCK", "/secret-agent")
	t.Setenv("SSH_ASKPASS", "/unsafe-helper")
	t.Setenv("LD_PRELOAD", "/unsafe-library")
	cmd := sshCommand(context.Background(), args)
	if cmd.Path != "/usr/bin/ssh" || cmd.Dir != "/" {
		t.Fatal("ambient executable/workdir selected")
	}
	for _, value := range cmd.Env {
		if strings.Contains(value, "secret-agent") || strings.Contains(value, "unsafe-") || strings.HasPrefix(value, "LD_") {
			t.Fatal("inherited SSH environment")
		}
	}
}
func TestSSHRejectsPrivateFileAndTargetInjection(t *testing.T) {
	for name, change := range map[string]func(*SSHOptions){"option host": func(o *SSHOptions) { o.Host = "-oProxyCommand=evil" }, "login suffix": func(o *SSHOptions) { o.User = "admin+e" }, "newline": func(o *SSHOptions) { o.Host = "host\ncommand" }, "key expansion": func(o *SSHOptions) { o.Identity = "/tmp/%h-key" }, "relative path": func(o *SSHOptions) { o.KnownHosts = "known_hosts" }, "port": func(o *SSHOptions) { o.Port = 0 }, "IPv6 zone": func(o *SSHOptions) { o.Host = "fe80::1%eth0" }} {
		t.Run(name, func(t *testing.T) {
			o := sshFixture(t)
			change(&o)
			if _, err := SSHArgs(o); codeOf(err) != InvalidInput {
				t.Fatal("unsafe SSH option accepted", err)
			}
		})
	}
	o := sshFixture(t)
	if err := os.Chmod(o.Identity, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := SSHArgs(o); err == nil {
		t.Fatal("world-readable identity accepted")
	}
	o = sshFixture(t)
	link := filepath.Join(filepath.Dir(o.Identity), "link")
	if err := os.Symlink(o.Identity, link); err != nil {
		t.Fatal(err)
	}
	o.Identity = link
	if _, err := SSHArgs(o); err == nil {
		t.Fatal("symlink credential accepted")
	}
}

func TestSSHErrorStreamIsBoundedAndNeverReturned(t *testing.T) {
	stopped := false
	w := &stderrLimit{stop: func() { stopped = true }}
	if n, err := w.Write([]byte(fixtureSecret)); err != nil || n != len(fixtureSecret) {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(strings.Repeat("x", 4096))); codeOf(err) != BoundsExceeded || !stopped || strings.Contains(err.Error(), fixtureSecret) {
		t.Fatal("SSH diagnostics were unbounded or exposed")
	}
}
