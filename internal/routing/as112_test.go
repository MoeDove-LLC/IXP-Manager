package routing

import (
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/as112"
)

func as112RouteFixture(platform, mode string, v4, v6 bool) (Router, []Peer) {
	r := Router{ID: "as112_fixture", Platform: platform, Role: RoleAS112, ASN: 112, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", LocalIPv6: "2001:db8::112", AS112Service: mode, AS112IPv4: v4, AS112IPv6: v6}
	if platform == "junos" {
		if v4 {
			r.AS112NextHop4 = "192.0.2.200"
		}
		if v6 {
			r.AS112NextHop6 = "2001:db8::200"
		}
	}
	peers := []Peer{}
	if v4 {
		peers = append(peers, Peer{ID: "peer4", ASN: 64501, Address: "192.0.2.1", Enabled: true, MaxPrefixes: 10, Password: "Synthetic-AS112-Password", AuthenticationRevision: "abcdefghijklmnopqrstuv"})
	}
	if v6 {
		peers = append(peers, Peer{ID: "peer6", ASN: 64502, Address: "2001:db8::1", Enabled: true, MaxPrefixes: 10, Password: "Synthetic-AS112-Password", AuthenticationRevision: "abcdefghijklmnopqrstuv"})
	}
	return r, peers
}
func TestAS112AllPlatformsExactOriginOnlyAndSecretSafe(t *testing.T) {
	for _, platform := range []string{"bird2", "bird3", "frr", "openbgpd", "junos"} {
		t.Run(platform, func(t *testing.T) {
			for _, mode := range []string{as112.DirectDelegation, as112.DNAME, as112.Both} {
				for _, family := range []int{4, 6, 46} {
					r, peers := as112RouteFixture(platform, mode, family != 6, family != 4)
					config, err := RenderFor(platform, r, peers)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = parseAS112Artifact(platform, config); err != nil {
						t.Fatal("exact generated AS112 profile rejected", err)
					}
					wanted := map[string]bool{}
					for _, prefix := range AS112Service(r).Prefixes() {
						wanted[prefix] = true
					}
					for _, prefix := range (as112.Service{Mode: as112.Both, IPv4: true, IPv6: true}).Prefixes() {
						if strings.Contains(config, prefix) != wanted[prefix] {
							t.Fatal("wrong AS112 prefix selection", mode, family, prefix)
						}
					}
					for _, forbidden := range []string{"0.0.0.0/0", "::/0", "protocol kernel", "redistribute", "route-server-client", "rs client;", "transparent-as yes"} {
						if strings.Contains(config, forbidden) {
							t.Fatal("AS112 artifact gained transit/kernel/server behavior", forbidden)
						}
					}
					switch platform {
					case "bird2", "bird3":
						if !strings.Contains(config, "source != RTS_STATIC") || !strings.Contains(config, "if proto != \"as112_static") || !strings.Contains(config, "bgp_path.len != 0") || !strings.Contains(config, "import none;") {
							t.Fatal("BIRD lacked local-static/empty-path/import-deny constraints")
						}
					case "frr":
						if !strings.Contains(config, "md_as112_local permit ^$") || !strings.Contains(config, "match as-path md_as112_local") || !strings.Contains(config, "route-map md_as112_deny in") {
							t.Fatal("FRR lacked origin-only policy")
						}
					case "openbgpd":
						if !strings.Contains(config, "deny from any") || !strings.Contains(config, "deny quick to any max-as-len 0") {
							t.Fatal("OpenBGPD lacked origin-only policy")
						}
					case "junos":
						if !strings.Contains(config, "<protocol>static</protocol>") || !strings.Contains(config, "<exact/>") || !strings.Contains(config, "_deny</import>") {
							t.Fatal("Junos lacked static-source and explicit deny")
						}
						if err = ValidateJunosXML(config); err != nil {
							t.Fatal(err)
						}
					}
					preview, err := RedactConfig(platform, config)
					if err != nil || strings.Contains(preview, peers[0].Password) || !strings.Contains(preview, RedactedBGPPassword) {
						t.Fatal("AS112 secret-safe preview unavailable", err)
					}
					for _, bad := range []string{config + "# unreviewed addition\n", strings.Replace(config, "192.175.48.0/24", "192.175.48.0/25", 1)} {
						if bad == config {
							continue
						}
						if _, err = RedactConfig(platform, bad); err == nil {
							t.Fatal("modified profile exposed content")
						}
					}
				}
			}
		})
	}
}
func TestAS112RoleAndJunosBackendBoundaries(t *testing.T) {
	r, peers := as112RouteFixture("bird2", as112.Both, true, true)
	for _, change := range []func(*Router, []Peer){func(r *Router, _ []Peer) { r.ASN = 64500 }, func(r *Router, _ []Peer) { r.AS112IPv4 = false; r.AS112IPv6 = false }, func(r *Router, _ []Peer) { r.CollectorPolicy = "observe" }, func(r *Router, _ []Peer) { r.RPKI = &RPKIConfig{Host: "192.0.2.50", Port: 323} }, func(r *Router, _ []Peer) { v := true; r.RFC1997Passthrough = &v }, func(_ *Router, p []Peer) { p[0].AllowedPrefixes = []string{"192.175.48.0/24"} }, func(r *Router, _ []Peer) { r.AS112NextHop4 = "192.0.2.200" }} {
		candidate := r
		p := append([]Peer{}, peers...)
		change(&candidate, p)
		if _, err := RenderFor("bird2", candidate, p); err == nil {
			t.Fatal("invalid AS112 role/policy accepted")
		}
	}
	for _, platform := range []string{"routeros7", "bird1", "cisco", "quagga"} {
		if RoleSupported(platform, RoleAS112) {
			t.Fatal("unsupported AS112 platform advertised")
		}
		if _, err := RenderFor(platform, r, peers); err == nil {
			t.Fatal("unsupported AS112 platform rendered")
		}
	}
	j, p := as112RouteFixture("junos", as112.Both, true, true)
	config, err := RenderFor("junos", j, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range AS112Service(j).Addresses() {
		prefix := ip + "/128"
		if !strings.Contains(ip, ":") {
			prefix = ip + "/32"
		}
		if !strings.Contains(config, "<name>"+prefix+"</name><next-hop>") {
			t.Fatal("Junos omitted exact DNS host forwarding route", prefix)
		}
		if strings.Contains(config, "<address>"+prefix+"</address>") {
			t.Fatal("Junos exported a service host route")
		}
	}
	for _, value := range []string{"", "192.175.48.1", "192.31.196.200", "192.0.2.112", "2001:db8::200"} {
		bad := j
		bad.AS112NextHop4 = value
		if _, err = RenderFor("junos", bad, p); err == nil {
			t.Fatal("invalid Junos DNS backend next-hop accepted", value)
		}
	}
}
