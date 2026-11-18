package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routing"
)

func bird3CLIExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBIRD3CLIApplyAndRecoveryPassExplicitMajor(t *testing.T) {
	dir := t.TempDir()
	old := "router id 192.0.2.1;\n"
	next := "router id 192.0.2.2;\n"
	target, candidate := filepath.Join(dir, "bird.conf"), filepath.Join(dir, "approved.conf")
	if err := os.WriteFile(target, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte(next), 0600); err != nil {
		t.Fatal(err)
	}
	validator := bird3CLIExecutable(t, dir, "bird3", "if [ \"$1\" = --version ]; then echo 'BIRD version 3.4.0'; exit 0; fi\nexit 0\n")
	control := bird3CLIExecutable(t, dir, "birdc3", `case "$*" in
 *"show status"*) echo '1000-BIRD 3.4.0'; echo '0013 Daemon is up and running';;
 *"configure confirm"*) echo '0018 Reconfiguration confirmed';;
 *"configure"*) echo '0003 Reconfigured';;
 *) exit 1;;
esac
`)
	if err := run([]string{"apply", "--platform", "bird3", "--validator", validator, "--birdc", control, "--target", target, "--file", candidate, "--checksum", routing.Checksum(next)}); err != nil {
		t.Fatal(err)
	}
	if bytes, err := os.ReadFile(target); err != nil || string(bytes) != next {
		t.Fatal("CLI did not install explicitly selected BIRD3 profile")
	}
	if err := os.WriteFile(target+".moedove-pending", []byte("bird3:"+routing.Checksum(old)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"recover", "--platform", "bird2", "--validator", validator, "--birdc", control, "--target", target}); err == nil || !strings.Contains(err.Error(), "different BIRD major") {
		t.Fatal("CLI default recovery lost journal major binding", err)
	}
	if err := run([]string{"recover", "--platform", "bird3", "--validator", validator, "--birdc", control, "--target", target}); err != nil {
		t.Fatal(err)
	}
	if bytes, err := os.ReadFile(target); err != nil || string(bytes) != old {
		t.Fatal("CLI BIRD3 recovery did not restore its own snapshot")
	}
}
