package routing

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests only invoke the platform's native parser, never a BGP daemon.
// Run them in the bounded, network-isolated native fixture environment.
func TestAS112NativeRoutingParsers(t *testing.T) {
	for _, target := range []struct{ platform, variable string }{
		{"bird2", "BIRD_BIN"}, {"bird3", "BIRD3_BIN"},
		{"frr", "FRR_BIN"}, {"openbgpd", "OPENBGPD_BIN"},
	} {
		t.Run(target.platform, func(t *testing.T) {
			binary := os.Getenv(target.variable)
			if binary == "" {
				t.Skip(target.variable + " is required for real native validation")
			}
			for _, scenario := range []struct {
				name, service string
				v4, v6        bool
			}{
				{"both-dual-stack", "both", true, true},
				{"direct-ipv4", "direct-delegation", true, false},
				{"dname-ipv6", "dname", false, true},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					router := Router{ID: "native_as112", Name: "AS112 native fixture", Platform: target.platform, Role: RoleAS112, ASN: 112, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", LocalIPv6: "2001:db8::112", AS112Service: scenario.service, AS112IPv4: scenario.v4, AS112IPv6: scenario.v6}
					peers := []Peer{}
					if scenario.v4 {
						peers = append(peers, Peer{ID: "native_v4", ASN: 64501, Address: "192.0.2.1", Enabled: true, MaxPrefixes: 10, Password: "Synthetic-AS112-Key", AuthenticationRevision: "abcdefghijklmnopqrstuv"})
					}
					if scenario.v6 {
						peers = append(peers, Peer{ID: "native_v6", ASN: 64502, Address: "2001:db8::1", Enabled: true, MaxPrefixes: 10, Password: "Synthetic-AS112-Key", AuthenticationRevision: "abcdefghijklmnopqrstuv"})
					}
					config, err := RenderFor(target.platform, router, peers)
					if err != nil {
						t.Fatal(err)
					}
					if err = ValidateFor(context.Background(), target.platform, binary, config); err != nil {
						if target.platform == "frr" {
							file := filepath.Join(t.TempDir(), "native-frr.conf")
							if writeErr := os.WriteFile(file, []byte(config), 0600); writeErr != nil {
								t.Fatal(writeErr)
							}
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							output, commandErr := run(ctx, binary, "--dryrun", "--skip_runas", "--bgp_port", "0", "--no_zebra", "--no_kernel", "--vty_socket", filepath.Dir(file), "--pid_file", file+".pid", "--config_file", file)
							cancel()
							t.Logf("synthetic FRR parser diagnostics (%v): %s", commandErr, output)
						}
						t.Fatal(err)
					}
				})
			}
		})
	}
}
