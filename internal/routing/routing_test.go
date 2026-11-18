package routing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture() (Router, []Peer) {
	return Router{ID: "r1", ASN: 64512, RouterID: "192.0.2.1", LocalIPv4: "192.0.2.1", LocalIPv6: "2001:db8::1"}, []Peer{{ID: "p1", Name: "Untrusted name; include /etc/shadow", ASN: 64513, Address: "192.0.2.2", Enabled: true, MaxPrefixes: 100, AllowedPrefixes: []string{"198.51.100.0/24"}}, {ID: "p2", ASN: 64514, Address: "2001:db8::2", Enabled: true, MaxPrefixes: 100, AllowedPrefixes: []string{"2001:db8:1::/48"}}}
}

func TestRenderConstrainedPolicy(t *testing.T) {
	r, p := fixture()
	config, err := Render(r, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"rs client;", "bgp_path.first != 64513", "bgp_path.len > 64", "import limit 100 action block", "198.51.100.0/24", "2001:db8:1::/48", "source != RTS_BGP"} {
		if !strings.Contains(config, required) {
			t.Errorf("missing %q", required)
		}
	}
	for _, forbidden := range []string{"include", "Untrusted name", "/24+", "protocol kernel"} {
		if strings.Contains(config, forbidden) {
			t.Errorf("unsafe output %q", forbidden)
		}
	}
	p[0], p[1] = p[1], p[0]
	again, err := Render(r, p)
	if err != nil || again != config {
		t.Fatal("ordering changed immutable configuration")
	}
}

func TestRenderRejectsUnsafeInputs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Router, []Peer)
	}{
		{"injection", func(r *Router, p []Peer) { p[0].Address = "192.0.2.2; include \"/tmp/pwn\";" }},
		{"default route", func(r *Router, p []Peer) { p[0].AllowedPrefixes = []string{"0.0.0.0/0"} }},
		{"host bits", func(r *Router, p []Peer) { p[0].AllowedPrefixes = []string{"198.51.100.1/24"} }},
		{"wrong family", func(r *Router, p []Peer) { p[0].AllowedPrefixes = []string{"2001:db8::/32"} }},
		{"empty allowlist", func(r *Router, p []Peer) { p[0].AllowedPrefixes = nil }},
		{"zero limit", func(r *Router, p []Peer) { p[0].MaxPrefixes = 0 }},
		{"invalid asn", func(r *Router, p []Peer) { p[0].ASN = 23456 }},
		{"same asn", func(r *Router, p []Peer) { p[0].ASN = r.ASN }},
		{"duplicate id", func(r *Router, p []Peer) { p[1].ID = p[0].ID }},
		{"duplicate address", func(r *Router, p []Peer) { p[1].Address = p[0].Address }},
		{"own address", func(r *Router, p []Peer) { p[0].Address = r.LocalIPv4 }},
		{"missing source", func(r *Router, p []Peer) { r.LocalIPv6 = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, p := fixture()
			tc.mutate(&r, p)
			if _, err := Render(r, p); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func executable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func fakeBird(t *testing.T, dir string) string {
	return executable(t, dir, "bird", "if [ \"$1\" = --version ]; then echo 'BIRD version 2.17.5'; exit 0; fi\nif [ \"$1\" = -p ] && [ \"$2\" = -c ]; then exit 0; fi\nexit 1\n")
}
func TestNativeValidationFailsClosed(t *testing.T) {
	ctx := context.Background()
	if err := Validate(ctx, filepath.Join(t.TempDir(), "missing"), "router id 192.0.2.1;"); err == nil {
		t.Fatal("missing binary accepted")
	}
	bin := executable(t, t.TempDir(), "bird", "echo 'BIRD version 3.1.0'\n")
	if err := Validate(ctx, bin, "router id 192.0.2.1;"); err == nil {
		t.Fatal("different BIRD major accepted")
	}
	bin = executable(t, t.TempDir(), "bird", "if [ \"$1\" = --version ]; then echo 'BIRD version 2.17.5'; exit 0; fi\necho 'syntax error'; exit 1\n")
	if err := Validate(ctx, bin, "bad"); err == nil {
		t.Fatal("parse rejection ignored")
	}
}

func TestApplyRollbackAndSymlinkProtection(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bird.conf")
	old := []byte("old config")
	if err := os.WriteFile(target, old, 0640); err != nil {
		t.Fatal(err)
	}
	bird := fakeBird(t, dir)
	birdc := executable(t, dir, "birdc", "case \"$*\" in *timeout*) echo '8002 Reconfiguration failed'; exit 0;; *) echo '0003 Reconfigured';; esac\n")
	options := ApplyOptions{BirdBinary: bird, BirdcBinary: birdc, Target: target, Checksum: Checksum("new config")}
	if err := Apply(context.Background(), "new config", options); err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("expected rollback: %v", err)
	}
	contents, _ := os.ReadFile(target)
	if string(contents) != string(old) {
		t.Fatal("rollback failed")
	}
	backup, _ := os.ReadFile(target + ".moedove-previous")
	if string(backup) != string(old) {
		t.Fatal("backup lost")
	}
	st, _ := os.Stat(target)
	if st.Mode().Perm() != 0640 {
		t.Fatal("permissions changed")
	}
	link := filepath.Join(dir, "link.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	options.Target = link
	if err := Apply(context.Background(), "new config", options); err == nil {
		t.Fatal("symlink target accepted")
	}
	options.Target = target
	options.Checksum = Checksum("different")
	if err := Apply(context.Background(), "new config", options); err == nil {
		t.Fatal("unreviewed bytes accepted")
	}
}

func TestApplySuccessAndExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bird.conf")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	options := ApplyOptions{BirdBinary: fakeBird(t, dir), BirdcBinary: executable(t, dir, "birdc", "case \"$*\" in *confirm*) echo '0018 Reconfiguration confirmed';; *) echo '0003 Reconfigured';; esac\n"), Target: target, Checksum: Checksum("new")}
	unlock, err := deploymentLock(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), "new", options); err == nil {
		t.Fatal("exclusive lock ignored")
	}
	unlock()
	if err := Apply(context.Background(), "new", options); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Fatal("configuration not installed")
	}
}

// Run with BIRD_BIN=/path/to/bird to exercise the real parser without a daemon.
func TestNativeBIRD2Integration(t *testing.T) {
	binary := os.Getenv("BIRD_BIN")
	if binary == "" {
		t.Skip("set BIRD_BIN to run native BIRD 2 parser")
	}
	r, p := fixture()
	config, err := Render(r, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = Validate(context.Background(), binary, config); err != nil {
		t.Fatal(err)
	}
	if err = Validate(context.Background(), binary, "not bird syntax"); err == nil {
		t.Fatal("native parser accepted invalid syntax")
	}
}

func TestRecoverInterruptedDeployment(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bird.conf")
	if err := os.WriteFile(target, []byte("unconfirmed"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".moedove-previous", []byte("previous"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := writePending(target, Checksum("previous")); err != nil {
		t.Fatal(err)
	}
	options := ApplyOptions{BirdBinary: fakeBird(t, dir), BirdcBinary: executable(t, dir, "birdc", "echo '0003 Reconfigured'\n"), Target: target, Checksum: Checksum("next")}
	if err := Apply(context.Background(), "next", options); err == nil {
		t.Fatal("overwrote pending recovery snapshot")
	}
	if err := Recover(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "previous" {
		t.Fatal("recovery lost previous configuration")
	}
	if _, err := os.Stat(target + ".moedove-pending"); !os.IsNotExist(err) {
		t.Fatal("journal not cleared")
	}
	if err := writePending(target, Checksum("tampered")); err != nil {
		t.Fatal(err)
	}
	if err := Recover(context.Background(), options); err == nil {
		t.Fatal("tampered backup accepted")
	}
}

func TestBirdControlRequiresPositiveResponse(t *testing.T) {
	for _, output := range []string{"", "hello", "0001 BIRD 2.17.5 ready", "0006 Reconfiguration ignored, shutting down", "0005 Reconfiguration queued", "0019 Nothing to do", "8002 Parse error"} {
		if err := checkControlReply(output, []string{"configure", "confirm"}); err == nil {
			t.Fatalf("accepted incomplete/negative reply: %q", output)
		}
	}
	if err := checkControlReply("0001 BIRD 2.17.5 ready\n0018 Reconfiguration confirmed", []string{"configure", "confirm"}); err != nil {
		t.Fatal(err)
	}
	if err := checkControlReply("0004-Reconfiguration in progress", []string{"configure", "status"}); err != errReconfiguring {
		t.Fatal("did not wait for in-progress reconfiguration")
	}
	if err := checkControlReply("0003-Daemon is up and running\n0022-Configuration unconfirmed, undo in 60 s", []string{"configure", "status"}); err != nil {
		t.Fatal(err)
	}
}
