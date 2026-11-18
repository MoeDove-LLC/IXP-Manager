package as112

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func nativeBundle(t *testing.T) (Bundle, string) {
	t.Helper()
	// Each label is 198 UTF-8 bytes. Together with the other identity TXT
	// records this exercises a real UDP response larger than classic 512 bytes.
	bundle, err := RenderNode("native_as112", "2026-01-01T00:00:00Z", Service{Mode: Both, IPv4: true, IPv6: true}, Identity{ServerName: "as112.example.test", ResponsibleMailbox: "hostmaster.example.test", Facility: strings.Repeat("合", 66), Location: strings.Repeat("界", 66), UniqueIP: "192.0.2.112", LOC: "31 13 0 N 121 28 0 E 10m"}, []string{"192.0.2.112", "2001:db8::112"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, file := range bundle.Files {
		if filepath.IsAbs(file.Path) || filepath.Clean(file.Path) != file.Path || strings.HasPrefix(file.Path, "..") {
			t.Fatal("generated path escaped the native fixture")
		}
		name := filepath.Join(dir, file.Path)
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(file.Content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return bundle, dir
}

type nativeOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *nativeOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if remaining := (64 << 10) - len(b.data); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *nativeOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
func nativeCommand(t *testing.T, dir, binary string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = dir
	output := &nativeOutput{}
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err != nil {
		t.Fatalf("native fixture command rejected generated input: %v\n%s", err, output.String())
	}
}

func TestAS112NativePowerDNSAndZones(t *testing.T) {
	pdns, named := os.Getenv("PDNS_SERVER_BIN"), os.Getenv("NAMED_CHECKZONE_BIN")
	if pdns == "" || named == "" {
		t.Skip("PDNS_SERVER_BIN and NAMED_CHECKZONE_BIN are required")
	}
	bundle, dir := nativeBundle(t)
	nativeCommand(t, dir, pdns, "--config-dir="+dir, "--config=check")
	for i, zone := range bundle.Zones {
		nativeCommand(t, dir, named, zone, filepath.Join(dir, "zones", fmt.Sprintf("zone-%02d.zone", i)))
	}
	if pdnsutil := os.Getenv("PDNSUTIL_BIN"); pdnsutil != "" {
		nativeCommand(t, dir, pdnsutil, "--config-dir="+dir, "check-all-zones")
	}
	for _, file := range bundle.Files {
		content, err := os.ReadFile(filepath.Join(dir, file.Path))
		if err != nil || string(content) != file.Content {
			t.Fatal("native parsing changed the generated bundle")
		}
	}
	// A separate malformed zone must be rejected by the actual BIND parser.
	bad := filepath.Join(dir, "bad.zone")
	if err := os.WriteFile(bad, []byte("$ORIGIN invalid.example.\n@ IN SOA broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, named, "invalid.example.", bad).Run(); err == nil || ctx.Err() != nil {
		t.Fatal("native BIND did not reject the malformed zone")
	}
	t.Logf("powerdns_configuration_checked=true native_zones=%d malformed_zone_rejected=true bundle_unchanged=true", len(bundle.Zones))
}

// Requires a private network namespace with only loopback. A trusted bootstrap
// installs the fixed fixture addresses, then drops every capability and sets
// no_new_privs before launching these tests and the DNS process.
func TestAS112NativeDNSLocalNamespace(t *testing.T) {
	if os.Getenv("AS112_NATIVE_NETNS") != "1" || os.Getenv("PDNS_SERVER_BIN") == "" {
		t.Skip("explicit isolated DNS namespace fixture is required")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback == 0 {
			t.Fatal("native DNS fixture refuses a namespace containing a non-loopback interface")
		}
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal("native DNS fixture cannot verify its capability boundary")
	}
	for _, field := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
		want := "0000000000000000"
		if field == "NoNewPrivs" {
			want = "1"
		}
		found := false
		for _, line := range strings.Split(string(status), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 && parts[0] == field+":" {
				found = parts[1] == want
			}
		}
		if !found {
			t.Fatal("native DNS fixture requires all capabilities dropped and no_new_privs set")
		}
	}
	bundle, dir := nativeBundle(t)
	missingIdentity := bundle.Identity
	missingIdentity.UniqueIP = "192.0.2.113"
	missing, err := RenderNode(bundle.RouterID, bundle.RouterCreatedAt, Service{Mode: Both, IPv4: true, IPv6: true}, missingIdentity, []string{"192.0.2.113", "2001:db8::112"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := Probe(context.Background(), missing); err == nil || result.Queries != 0 || result.Healthy {
		t.Fatal("missing local service addresses did not stop the probe before network access")
	}
	missingSecond, err := RenderNode(bundle.RouterID, bundle.RouterCreatedAt, Service{Mode: Both, IPv4: true, IPv6: true}, bundle.Identity, []string{"192.0.2.112", "2001:db8::113"})
	if err != nil || Validate(missingSecond) != nil {
		t.Fatal("missing second-node-address fixture is not a valid bound contract", err)
	}
	if result, err := Probe(context.Background(), missingSecond); err == nil || result.Queries != 0 || result.Healthy {
		t.Fatal("missing second DNS next hop did not stop the probe before network access")
	}
	command := exec.Command(os.Getenv("PDNS_SERVER_BIN"), "--config-dir="+dir, "--daemon=no", "--guardian=no", "--disable-syslog=yes", "--write-pid=no", "--socket-dir="+dir, "--loglevel=3")
	command.Dir = dir
	output := &nativeOutput{}
	command.Stdout, command.Stderr = output, output
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait(); close(done) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = command.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = command.Process.Kill()
				<-done
			}
		})
	}
	t.Cleanup(stop)
	listening := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-done:
			t.Fatalf("isolated DNS server exited: %v\n%s", err, output.String())
		default:
		}
		connection, err := net.DialTimeout("tcp", net.JoinHostPort(bundle.ListenAddresses[0], "53"), 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			listening = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !listening {
		t.Fatalf("isolated DNS listener unavailable\n%s", output.String())
	}
	result, err := Probe(context.Background(), bundle)
	wanted := len(bundle.ListenAddresses) * 2 * len(bundle.HealthPlan.Checks)
	if err != nil || !result.Healthy || !result.LocalAddressesPresent || result.Queries != wanted || len(result.Failures) != 0 {
		t.Fatalf("actual UDP/TCP IPv4/IPv6 DNS response failed: %+v %v\n%s", result, err, output.String())
	}
	stop()
	after, err := Probe(context.Background(), bundle)
	if err != nil || after.Healthy || after.Queries != 1 || len(after.Failures) == 0 {
		t.Fatal("stopped DNS service was not detected", after, err)
	}
	t.Logf("isolated_dns_udp_tcp_ipv4_ipv6=true actual_queries=%d identity_and_negative_soa_verified=true missing_addresses_stop_before_query=true missing_second_node_address_zero_queries=true bound_node_tuple=true long_utf8_identity=true stopped_server_rejected=true capability_zero=true no_new_privs=true", wanted)
}
