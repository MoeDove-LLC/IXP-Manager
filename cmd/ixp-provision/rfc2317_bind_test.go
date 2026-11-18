package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRFC2317BINDStatusRejectsUnprovedLoadedAuthority(t *testing.T) {
	const zone = "2.0.192.in-addr.arpa."
	const path = "/scratch/bind/parent.zone"
	const native = "name: " + zone + "\ntype: primary\nfiles: " + path + "\nserial: 0\nnodes: 1\nlast loaded: Wed, 18 Nov 2026 05:15:48 GMT\nsecure: no\ndynamic: no\nreconfigurable via modzone: no\n"
	status, err := parseRFC2317LoadedZone(native, zone, path)
	if err != nil || status.Serial == nil || *status.Serial != 0 {
		t.Fatal("exact loaded serial zero is not an unloaded zone", err, status)
	}
	for _, test := range []struct{ before, after string }{
		{"primary", "secondary"}, {"dynamic: no", "dynamic: yes"}, {"secure: no", "secure: yes"},
		{path, path + ", /scratch/bind/include.zone"}, {"serial: 0", "serial: 4294967296"}, {"serial: 0", "serial: 0\nserial: 1"},
		{"reconfigurable via modzone: no", "reconfigurable via modzone: yes"}, {"secure: no", "secure: no\ninline signing: yes"},
		{"secure: no", "secure: no\nkey maintenance: automatic"}, {"secure: no", "secure: no\nsigned serial: 0"},
		{"last loaded: Wed, 18 Nov 2026 05:15:48 GMT\n", ""}, {zone, "3.0.192.in-addr.arpa."},
	} {
		if _, err := parseRFC2317LoadedZone(strings.Replace(native, test.before, test.after, 1), zone, path); err == nil {
			t.Fatalf("unexpected authority accepted when replacing %q with %q", test.before, test.after)
		}
	}
}

func TestRFC2317BINDUnloadedRequiresExactExplicitSafeSettings(t *testing.T) {
	const zone = "128-129.2.0.192.in-addr.arpa."
	const path = "/scratch/bind/child.zone"
	const native = `zone "` + zone + `" IN { type primary; file "` + path + `"; inline-signing no; allow-update { "none"; }; dnssec-policy "none"; };`
	if !matchRFC2317UnloadedZone(native+"\n", zone, path) {
		t.Fatal("native complete unloaded authority rejected")
	}
	for _, test := range []struct{ before, after string }{
		{"type primary", "type secondary"}, {path, "/wrong/child.zone"},
		{"inline-signing no;", ""}, {"inline-signing no;", "inline-signing yes;"},
		{`allow-update { "none"; };`, ""}, {`allow-update { "none"; };`, `allow-update { "any"; };`},
		{`dnssec-policy "none";`, ""}, {`dnssec-policy "none";`, `dnssec-policy "default";`},
		{`dnssec-policy "none";`, `dnssec-policy "none"; dnssec-policy "none";`},
		{`type primary;`, `type primary; include "key";`}, {`" IN`, `" CH`},
	} {
		if matchRFC2317UnloadedZone(strings.Replace(native, test.before, test.after, 1), zone, path) {
			t.Fatalf("unproved configured authority accepted when replacing %q with %q", test.before, test.after)
		}
	}
}

func TestRFC2317BINDNumericLoopbackOnly(t *testing.T) {
	for _, value := range []string{"127.0.0.1:953", "[::1]:953"} {
		if _, err := rfc2317Loopback(value); err != nil {
			t.Fatal("valid explicit loopback endpoint", value, err)
		}
	}
	for _, value := range []string{"localhost:953", "192.0.2.1:953", "127.0.0.1", "127.0.0.1:0", "[::1%lo]:953", "[::ffff:127.0.0.1]:953", "[::]:953"} {
		if _, err := rfc2317Loopback(value); err == nil {
			t.Fatal("invalid endpoint accepted", value)
		}
	}
}

func TestRFC2317NativeBINDAdapter(t *testing.T) {
	if os.Getenv("RFC2317_BIND_ADAPTER_NATIVE") != "1" {
		t.Skip("requires isolated stock BIND authority fixture")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" {
		t.Fatal("native BIND fixture requires its dedicated loopback namespace")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !strings.Contains(string(status), "CapEff:\t0000000000000000\n") || !strings.Contains(string(status), "CapBnd:\t0000000000000000\n") || !strings.Contains(string(status), "NoNewPrivs:\t1\n") {
		t.Fatal("native BIND fixture requires zero capabilities and no-new-privileges")
	}
	const parent, child = "2.0.192.in-addr.arpa.", "128-129.2.0.192.in-addr.arpa."
	const parentFile, childFile = "/scratch/bind/parent.zone", "/scratch/bind/child.zone"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, err := openRFC2317BIND(rfc2317BINDOptions{RNDCBinary: "/native/usr/sbin/rndc", ControlKey: "/scratch/bind/ctl.key", ControlEndpoint: "127.0.0.1:19530", DNSEndpoint: "127.0.0.1:15353"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	p, err := b.Status(ctx, parent, parentFile)
	if err != nil || p.Serial == nil || *p.Serial != 41 {
		t.Fatal("already loaded parent not verified", err, p)
	}
	if serial, err := b.SOA(ctx, parent); err != nil || serial != 41 {
		t.Fatal("exact authoritative parent TCP SOA not observed", serial, err)
	}
	c, err := b.Status(ctx, child, childFile)
	if err != nil || c.Serial != nil || c.Zone != child || c.File != childFile {
		t.Fatal("explicit configured static child without a master not proved", err, c)
	}
	if _, err := b.SOA(ctx, child); err == nil {
		t.Fatal("SOA accepted for an unloaded child")
	}
	if _, err := b.Status(ctx, "not-configured."+parent, childFile); err == nil {
		t.Fatal("unconfigured zone accepted as configured and unloaded")
	}
	if err := b.Reload(ctx, child, parentFile); err == nil {
		t.Fatal("reload accepted a different master path")
	}
	// This is a synthetic file under the isolated fixture's only writable
	// scratch path. The adapter itself never writes a master file.
	text := "$ORIGIN " + child + "\n$TTL 300\n@ IN SOA ns.example.test. hostmaster.example.test. (0 3600 60 604800 300)\n@ IN NS ns.example.test.\n"
	if err := os.WriteFile(childFile, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(childFile)
	if err := b.Reload(ctx, child, childFile); err != nil {
		t.Fatal("proven configured child did not reload", err)
	}
	c, err = b.Status(ctx, child, childFile)
	if err != nil || c.Serial == nil || *c.Serial != 0 {
		t.Fatal("first actual reload did not verify loaded serial zero", err, c)
	}
	if serial, err := b.SOA(ctx, child); err != nil || serial != 0 {
		t.Fatal("exact authoritative child TCP SOA not observed", serial, err)
	}
	t.Log("bind_adapter_verified=true native_loaded_parent=true authenticated_showzone_proved_unloaded_child=true wrong_master_reload_rejected=true actual_child_reload_serial_zero=true authoritative_tcp_soa_both=true")
}
