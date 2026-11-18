package routing

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func quaggaFixture() (Router, []Peer) {
	return Router{ID: "quagga", Name: "Legacy source", Platform: "quagga", ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "192.0.2.1", LocalIPv6: "2001:db8::1"}, []Peer{
		{ID: "v4-a", ASN: 64501, Address: "192.0.2.2", Enabled: true, MaxPrefixes: 20, AllowedPrefixes: []string{"198.51.100.0/24", "203.0.113.0/24"}, AllowedRoutes: []PrefixOrigin{{Prefix: "198.51.100.0/24", OriginASN: 64511}, {Prefix: "203.0.113.0/24", OriginASN: 64512}}},
		{ID: "v4-b-same-as", ASN: 64501, Address: "192.0.2.3", Enabled: true, MaxPrefixes: 20, AllowedPrefixes: []string{"198.51.100.0/24"}, AllowedRoutes: []PrefixOrigin{{Prefix: "198.51.100.0/24", OriginASN: 64512}}},
		{ID: "v6", ASN: 4200000001, Address: "2001:db8::2", Enabled: true, MaxPrefixes: 20, AllowedPrefixes: []string{"2001:db8:100::/48"}, AllowedRoutes: []PrefixOrigin{{Prefix: "2001:db8:100::/48", OriginASN: 4200000002}}},
	}
}
func TestQuaggaDedicatedViewAndActualSourceImportPolicy(t *testing.T) {
	router, peers := quaggaFixture()
	config, err := RenderQuagga(router, peers)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedQuagga(config); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bgp multiple-instance\n", "router bgp 64500 view MOEDOVE_QUAGGA\n", "bgp enforce-first-as", "no bgp default ipv4-unicast", "neighbor 2001:db8::2 nexthop-local unchanged", "ip as-path access-list ", "^64501(_[0-9]+){0,62}_64511$", "^4200000001(_[0-9]+){0,62}_4200000002$"} {
		if !strings.Contains(config, want) {
			t.Fatal("missing Quagga policy", want)
		}
	}
	for _, peer := range peers {
		name := peerName(peer)
		for _, want := range []string{"match peer " + peer.Address + "\n call " + name + "_policy", "neighbor " + peer.Address + " route-map mq_import import", "neighbor " + peer.Address + " route-map " + name + "_policy export", "neighbor " + peer.Address + " route-map " + name + "_policy in", "neighbor " + peer.Address + " maximum-prefix 20"} {
			if !strings.Contains(config, want) {
				t.Fatal("source-specific RS policy missing", want)
			}
		}
	}
	for _, bad := range []string{"frr defaults", "bgp as-path access-list", "route-map mq_import in", "redistribute ", " network ", "next-hop-self", "set as-path", "set ip next-hop", "le 32", "le 128", "warning-only"} {
		if strings.Contains(config, bad) {
			t.Fatal("unsafe copied grammar or policy expansion", bad)
		}
	}
	for _, line := range strings.Split(config, "\n") {
		if len(line) > 256 {
			t.Fatal("model or command line exceeds conservative native line bound")
		}
	}
	reordered := []Peer{peers[2], peers[1], peers[0]}
	again, err := RenderQuagga(router, reordered)
	if err != nil || again != config {
		t.Fatal("rendering depends on caller order", err)
	}
}
func TestQuaggaPathFirstOriginLengthAndSetBoundaries(t *testing.T) {
	// The same underscore expansion is verified against stock bgp_regex.c in
	// the optional native policy harness; these cases cover the typed contract.
	for _, test := range []struct {
		path          string
		first, origin uint32
		allow         bool
	}{
		{"64501", 64501, 0, true}, {"64501", 64501, 64501, true}, {"64501 64511", 64501, 64511, true}, {"64501 64512", 64501, 64511, false}, {"64502 64511", 64501, 64511, false}, {"", 64501, 0, false}, {"64501 {64511,64512}", 64501, 0, false}, {"64501 (64511)", 64501, 0, false}, {"64501 " + strings.Repeat("64502 ", 62) + "64511", 64501, 64511, true}, {"64501 " + strings.Repeat("64502 ", 63) + "64511", 64501, 64511, false},
	} {
		pattern := strings.ReplaceAll(quaggaPathPattern(test.first, test.origin), "_", "(^|[,{}() ]|$)")
		match, err := regexp.MatchString(pattern, test.path)
		if err != nil || match != test.allow {
			t.Fatal("path authorization changed", test.path, match, err)
		}
	}
}

func TestQuaggaRouteMapSequencesFitLegacyGrammar(t *testing.T) {
	router, peers := quaggaFixture()
	p := peers[0]
	p.AllowedPrefixes = []string{"198.51.100.0/24"}
	p.AllowedRoutes = nil
	for i := 0; i < 6554; i++ {
		p.AllowedRoutes = append(p.AllowedRoutes, PrefixOrigin{Prefix: p.AllowedPrefixes[0], OriginASN: uint32(100000 + i)})
	}
	config, err := RenderQuagga(router, []Peer{p})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config, fmt.Sprintf("route-map %s_policy deny 6555\n", peerName(p))) {
		t.Fatal("legacy <1-65535> sequence space was wasted or overflowed")
	}
}

func TestQuaggaManagedReconstructionAndCredentialBoundary(t *testing.T) {
	router, peers := quaggaFixture()
	peers[0].Password = "SYNTHETIC-Quagga_MD5+Key/="
	peers[0].AuthenticationRevision = "abcdefghijklmnopqrstuv"
	config, err := RenderQuagga(router, peers)
	if err != nil {
		t.Fatal(err)
	}
	redacted, err := RedactQuagga(config)
	if err != nil || strings.Contains(redacted, peers[0].Password) || !strings.Contains(redacted, RedactedBGPPassword) {
		t.Fatal("credential redaction failed", err)
	}
	if ValidateManagedQuagga(redacted) == nil {
		t.Fatal("redacted preview accepted as native configuration")
	}
	for _, bad := range []string{config + "network 0.0.0.0/0\n", strings.Replace(config, " route-map mq_import import", " route-map mq_import in", 1), strings.Replace(config, " match peer 192.0.2.2", " match peer 192.0.2.3", 1), strings.Replace(config, " view MOEDOVE_QUAGGA", "", 1), strings.Replace(config, "seq 5 permit 198.51.100.0/24", "seq 5 permit 198.51.100.0/24 le 32", 1), strings.Replace(config, "match as-path ", "set as-path ", 1)} {
		if ValidateManagedQuagga(bad) == nil {
			t.Fatal("mutated generated profile accepted")
		}
		if result, err := RedactQuagga(bad); err == nil || result != "" {
			t.Fatal("unrecognized source returned a partial preview")
		}
	}
}
func TestQuaggaUnsupportedProfilesFailExplicitly(t *testing.T) {
	router, peers := quaggaFixture()
	for _, role := range []string{RoleRouteCollector, RoleAS112} {
		copy := router
		copy.Role = role
		if _, err := RenderQuagga(copy, peers); err == nil {
			t.Fatal("unsupported role rendered", role)
		}
	}
	for _, choice := range []bool{false, true} {
		copy := router
		copy.RFC1997Passthrough = &choice
		if _, err := RenderQuagga(copy, peers); err == nil {
			t.Fatal("community policy silently substituted")
		}
	}
	router.RPKI = &RPKIConfig{}
	if _, err := RenderQuagga(router, peers); err == nil {
		t.Fatal("unsupported runtime RPKI rendered")
	}
	for _, output := range []string{"bgpd version 10.3\nCopyright FRRouting", "bgpd version 1.2.3\nCopyright 1996-2005 Kunihiro Ishiguro, et al.", "BIRD version 1.6.8", "bgpd version 1.2.4\nFRRouting"} {
		if quaggaVersionSupported(output) {
			t.Fatal("other daemon/version accepted")
		}
	}
	if !quaggaVersionSupported("bgpd version 1.2.4\nCopyright 1996-2005 Kunihiro Ishiguro, et al.\nconfigured with:\n") {
		t.Fatal("stock Quagga version rejected")
	}
}

func TestQuaggaCapabilityAndPublicDispatchStayReadOnly(t *testing.T) {
	router, peers := quaggaFixture()
	config, err := RenderFor("quagga", router, peers)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RedactConfig("quagga", config); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, capability := range Capabilities() {
		if capability.Platform == "quagga" {
			found = true
			if !capability.Render || !capability.Validate || !capability.Fetch || capability.Apply || capability.Rollback || capability.ValidationLevel != "native" || len(capability.Roles) != 1 || capability.Roles[0] != RoleRouteServer {
				t.Fatal("Quagga read-only scope incorrect", capability)
			}
		}
	}
	if !found || RoleSupported("quagga", RoleRouteCollector) || RoleSupported("quagga", RoleAS112) {
		t.Fatal("Quagga unsupported role advertised")
	}
	if err := ValidateFor(context.Background(), "quagga", "/missing/quagga", config); err == nil {
		t.Fatal("missing native checker accepted")
	}
}
func TestQuaggaActualNativeSyntax(t *testing.T) {
	binary := os.Getenv("QUAGGA_BGPD_BIN")
	if binary == "" {
		t.Skip("requires pinned actual Quagga 1.2.4 executable in an offline namespace fixture")
	}
	router, peers := quaggaFixture()
	for _, family := range []string{"dual", "ipv4", "ipv6", "empty"} {
		t.Run(family, func(t *testing.T) {
			r, p := router, peers
			switch family {
			case "ipv4":
				r.LocalIPv6 = ""
				p = peers[:2]
			case "ipv6":
				r.LocalIPv4 = ""
				p = peers[2:]
			case "empty":
				p = nil
			}
			config, err := RenderQuagga(r, p)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateQuagga(context.Background(), binary, config); err != nil {
				t.Fatal(err)
			}
		})
	}
	peers[0].Password = strings.Repeat("a", 80)
	peers[0].AuthenticationRevision = "abcdefghijklmnopqrstuv"
	config, err := RenderQuagga(router, peers)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateQuagga(context.Background(), binary, config); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ValidateQuagga(ctx, binary, config) == nil {
		t.Fatal("cancelled native check succeeded")
	}
}

func TestQuaggaActualNativePolicyAndAttributeEncoding(t *testing.T) {
	helper := os.Getenv("QUAGGA_POLICY_HELPER")
	if helper == "" {
		t.Skip("requires offline fixture linked to unmodified Quagga 1.2.4 libraries")
	}
	router, peers := quaggaFixture()
	config, err := RenderQuagga(router, peers)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := dir + "/native-policy.conf"
	if err := os.WriteFile(file, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := runQuagga(ctx, helper, dir, file)
	if err != nil {
		t.Fatal("stock library policy fixture rejected generated policy", err)
	}
	if !strings.Contains(output, `"nativePolicyChecks":21`) || !strings.Contains(output, `"ASPathAndNextHopPreserved":true`) {
		t.Fatal("native policy and attribute checks incomplete")
	}
	t.Log("stock Quagga route-map engine: 21 source/prefix/origin/next-hop/length/attribute checks passed; no daemon event loop or BGP wire session")
}
