package routing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectorDefaultRolePreservesPublishedBytes(t *testing.T) {
	// Recorded before introducing Role, including TCP-MD5 and prefix/origin pairs.
	// RouterOS was updated after CHR 7.24.6 rejected fib=no and demonstrated
	// that connections default to main instead of their instance's table.
	// The generated-wire fixture also required explicit zero AS prepend.
	golden := map[string]string{
		"bird2":     "531894db28e43ecd5e9da438cc90040eb9ad8ea289eeda42526626febf45eb5e",
		"frr":       "d54f412aad21b5daefe25fc387bbe4e627727de511fdfe6518650cfcfcd86b09",
		"junos":     "f5cdf8b7d6a8e1779756aabe972e4bdf4bc5b9fa7e4575e70a028732f4204f23",
		"routeros7": "8cd4adc5f6c2d78367f63c688072334264452a4edad92fe7d47d772d0ed9fb9a",
	}
	for platform, expected := range golden {
		r, p := authenticatedFixture()
		implicit, err := RenderFor(platform, r, p)
		if err != nil {
			t.Fatal(err)
		}
		r.Role = RoleRouteServer
		explicit, err := RenderFor(platform, r, p)
		if err != nil || explicit != implicit || Checksum(explicit) != expected {
			t.Fatal(platform, "changed default route-server bytes", err, "checksum", Checksum(explicit))
		}
	}
}

func TestCollectorRoleAndPolicyBoundaries(t *testing.T) {
	r, p := authenticatedFixture()
	for _, change := range []func(*Router){
		func(r *Router) { r.Role = "unknown" },
		func(r *Router) { r.CollectorPolicy = CollectorObserve },
		func(r *Router) { r.Role = RoleRouteCollector; r.CollectorPolicy = "collect-anything" },
	} {
		candidate := r
		change(&candidate)
		if _, err := RenderFor("bird2", candidate, p); err == nil {
			t.Fatal("invalid role/policy accepted")
		}
	}
	r.Role = RoleRouteCollector
	for _, platform := range []string{"junos", "routeros7", "cisco", "quagga"} {
		if RoleSupported(platform, r.Role) {
			t.Fatal("unsupported role advertised", platform)
		}
		if _, err := RenderFor(platform, r, p); err == nil {
			t.Fatal("unsupported collector silently rendered a server", platform)
		}
	}
	for _, platform := range []string{"bird2", "bird3", "frr", "openbgpd"} {
		implicit, err := RenderFor(platform, r, p)
		if err != nil {
			t.Fatal(err)
		}
		r.CollectorPolicy = CollectorObserve
		explicit, err := RenderFor(platform, r, p)
		if err != nil || explicit != implicit {
			t.Fatal("collector default policy is not stable")
		}
		r.CollectorPolicy = ""
	}
	for _, cap := range Capabilities() {
		hasCollector := false
		for _, role := range cap.Roles {
			if role == RoleRouteCollector {
				hasCollector = true
			}
		}
		if hasCollector != (cap.Platform == "bird1" || cap.Platform == "bird2" || cap.Platform == "bird3" || cap.Platform == "frr" || cap.Platform == "openbgpd") {
			t.Fatal("capability role boundary incorrect")
		}
		if hasCollector && len(cap.CollectorPolicies) != 2 {
			t.Fatal("collector policies absent")
		}
	}
}

func TestCollectorRetainsFilteredRoutesWithoutAdvertising(t *testing.T) {
	for _, platform := range []string{"bird2", "frr"} {
		for _, policy := range []string{CollectorObserve, CollectorAuthorizedOnly} {
			r, p := authenticatedFixture()
			r.Role = RoleRouteCollector
			r.CollectorPolicy = policy
			r.RPKI = &RPKIConfig{Host: "127.0.0.1", Policy: "require-valid", ExpireSeconds: 600}
			config, err := RenderFor(platform, r, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"protocol kernel", "route-server-client", "rs client;", "redistribute", "network ", "md_authorized"} {
				if strings.Contains(config, forbidden) {
					t.Fatal(platform, "collector contains server/forwarding feature", forbidden)
				}
			}
			required := []string{"198.51.100.0/24", "203.0.113.0/24", fixturePassword, "moedove-role role=route-collector policy=" + policy}
			if platform == "bird2" {
				required = append(required, "enforce first as on;", "bgp_path.first != 64513", "bgp_path.len > 64", "bgp_path.last = 64520", "roa_check(md_roa4, net, bgp_path.last) != ROA_VALID")
				if strings.Count(config, "export none;") != len(p) || strings.Contains(config, "export filter") {
					t.Fatal("BIRD collector can export")
				}
				required = append(required, "import keep filtered yes;", "receive limit 100 action disable;")
				if strings.Count(config, "import table yes;") != len(p) {
					t.Fatal("RPKI/observation import table duplicated or missing")
				}
			} else {
				required = append(required, "route-map md_collect_export deny 10", "neighbor 192.0.2.2 enforce-first-as", "_64520$", "match rpki valid", "view MOEDOVE")
				if strings.Count(config, "route-map md_collect_export out") != len(p) {
					t.Fatal("FRR collector peer lacks export deny")
				}
				required = append(required, "maximum-prefix 100 force", "soft-reconfiguration inbound")
				if err := ValidateManagedFRR(config); err != nil {
					t.Fatal("agent rejected collector profile", err)
				}
			}
			for _, fragment := range required {
				if !strings.Contains(config, fragment) {
					t.Fatal(platform, "collector lost required behavior", fragment)
				}
			}
			preview, err := RedactConfig(platform, config)
			if err != nil || strings.Contains(preview, fixturePassword) || strings.Count(preview, RedactedBGPPassword) != len(p) {
				t.Fatal("collector redaction failed", err)
			}
		}
	}
}

func TestCollectorEmptyAllowlistIsObservationOnly(t *testing.T) {
	for _, platform := range []string{"bird2", "frr"} {
		r, p := authenticatedFixture()
		r.Role = RoleRouteCollector
		for i := range p {
			p[i].AllowedPrefixes = nil
			p[i].AllowedRoutes = nil
		}
		config, err := RenderFor(platform, r, p)
		if err != nil {
			t.Fatal("empty observation policy refused", err)
		}
		if strings.Contains(config, "accept;") || strings.Contains(config, "route-map md_ingress permit") {
			t.Fatal("empty whitelist admitted a route")
		}
		if _, err := RedactConfig(platform, config); err != nil {
			t.Fatal(err)
		}
		r.CollectorPolicy = CollectorAuthorizedOnly
		if _, err := RenderFor(platform, r, p); err == nil {
			t.Fatal("authorized-only accepted empty allowlist")
		}
		r.CollectorPolicy = CollectorObserve
		p[0].AllowedPrefixes = []string{"0.0.0.0/0"}
		if _, err := RenderFor(platform, r, p); err == nil {
			t.Fatal("observation weakened allowlist validation")
		}
	}
}

func TestCollectorFRRAgentRejectsLostSafetyMechanisms(t *testing.T) {
	r, p := authenticatedFixture()
	r.Role = RoleRouteCollector
	config, err := RenderFor("frr", r, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, modified := range []string{
		strings.ReplaceAll(config, " no bgp default ipv4-unicast\n", ""),
		strings.ReplaceAll(config, "  neighbor 2001:db8::2 route-map md_ingress in\n", ""),
		strings.ReplaceAll(config, " route-map md_collect_export out", " route-map md_ingress out"),
		strings.ReplaceAll(config, "route-map md_collect_export deny 10", "route-map md_collect_export permit 10"),
		strings.ReplaceAll(config, "  neighbor 2001:db8::2 route-map md_collect_export out\n", ""),
		strings.ReplaceAll(config, " maximum-prefix 100 force", " maximum-prefix 100"),
		strings.ReplaceAll(config, "  neighbor 2001:db8::2 soft-reconfiguration inbound\n", ""),
		strings.ReplaceAll(config, " neighbor 192.0.2.2 enforce-first-as\n", ""),
		config + "neighbor 192.0.2.2 route-server-client\n",
	} {
		if modified == config || ValidateManagedFRR(modified) == nil {
			t.Fatal("collector profile accepted absent retention/source/export safety")
		}
	}
}

func TestCollectorFRRWatchdogServerTransitionAndRollback(t *testing.T) {
	h := newFRRTestHost(t)
	r, p := fixture()
	r.Role = RoleRouteCollector
	var err error
	h.new, err = RenderFor("frr", r, p)
	if err != nil {
		t.Fatal(err)
	}
	w := openTestWatchdog(t, h)
	s, err := w.Stage(context.Background(), h.request())
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, h.live, h.new)
	if _, err = w.Cancel(FRRDecision{ID: s.ID, Checksum: s.Checksum}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, h.live, h.old)
	assertFile(t, h.opts.Target, h.old)
	s, err = w.Stage(context.Background(), h.request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Confirm(context.Background(), FRRDecision{ID: s.ID, Checksum: s.Checksum}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, h.opts.Target, h.new)
}

func TestNativeCollectorProfiles(t *testing.T) {
	for _, pair := range []struct{ platform, env string }{{"bird2", "BIRD_BIN"}, {"frr", "FRR_BIN"}} {
		binary := os.Getenv(pair.env)
		if binary == "" {
			t.Log(pair.env, "unset; native collector check skipped")
			continue
		}
		for _, policy := range []string{CollectorObserve, CollectorAuthorizedOnly} {
			for _, variant := range []string{"paired", "empty", "rpki"} {
				if policy == CollectorAuthorizedOnly && variant == "empty" {
					continue
				}
				if variant == "rpki" && pair.platform == "frr" && os.Getenv("MOEDOVE_FRR_MODULE_DIR") == "" {
					continue
				}
				r, p := authenticatedFixture()
				r.Role = RoleRouteCollector
				r.CollectorPolicy = policy
				if variant == "empty" {
					for i := range p {
						p[i].AllowedPrefixes = nil
						p[i].AllowedRoutes = nil
					}
				}
				if variant == "rpki" {
					r.RPKI = &RPKIConfig{Host: "127.0.0.1", Port: 3323, Policy: "require-valid", ExpireSeconds: 600}
				}
				config, err := RenderFor(pair.platform, r, p)
				if err != nil {
					t.Fatal(err)
				}
				if err = ValidateFor(context.Background(), pair.platform, binary, config); err != nil {
					t.Fatal(pair.platform, policy, variant, err)
				}
				if dir := os.Getenv("COLLECTOR_ARTIFACT_DIR"); dir != "" {
					path := filepath.Join(dir, pair.platform+"-"+policy+"-"+variant+".conf")
					if err = os.WriteFile(path, []byte(config), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}

func TestNativeCollectorFRRReloadPlan(t *testing.T) {
	helper, vtysh := os.Getenv("FRR_RELOAD_BIN"), os.Getenv("VTYSH_BIN")
	if helper == "" || vtysh == "" {
		t.Skip("set FRR_RELOAD_BIN and VTYSH_BIN for offline collector reload planning")
	}
	dir := t.TempDir()
	r, p := authenticatedFixture()
	server, err := RenderFor("frr", r, p)
	if err != nil {
		t.Fatal(err)
	}
	r.Role = RoleRouteCollector
	collector, err := RenderFor("frr", r, p)
	if err != nil {
		t.Fatal(err)
	}
	base, candidate := filepath.Join(dir, "base.conf"), filepath.Join(dir, "candidate.conf")
	if err = os.WriteFile(base, []byte(server), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(candidate, []byte(collector), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--test", "--input", base, "--daemon", "bgpd", "--bindir", filepath.Dir(vtysh), "--confdir", dir, "--rundir", dir, "--vty_socket", dir, "--stdout", "--log-level", "error", candidate}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := run(ctx, helper, args...)
	if err != nil || !strings.Contains(out, "route-server-client") || !strings.Contains(out, "md_collect_export") || !strings.Contains(out, "maximum-prefix 100 force") {
		t.Fatal("offline planner did not cover server removal and collector safeguards; raw output withheld")
	}
	if err = os.WriteFile(base, []byte(collector), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = run(ctx, helper, args...)
	if err != nil || strings.TrimSpace(out) != "" {
		t.Fatal("identical collector profile produced a native planner delta; raw output withheld")
	}
}

func TestPrePolicyCachesAlwaysBoundFilteredRoutes(t *testing.T) {
	for _, role := range []string{RoleRouteServer, RoleRouteCollector} {
		policies := []string{""}
		if role == RoleRouteCollector {
			policies = []string{CollectorObserve, CollectorAuthorizedOnly}
		}
		for _, policy := range policies {
			for _, withRPKI := range []bool{false, true} {
				for _, platform := range []string{"bird2", "frr"} {
					r, p := fixture()
					r.Role = role
					r.CollectorPolicy = policy
					if withRPKI {
						r.RPKI = &RPKIConfig{Host: "127.0.0.1", Policy: "require-valid", ExpireSeconds: 600}
					}
					config, err := RenderFor(platform, r, p)
					if err != nil {
						t.Fatal(err)
					}
					retained := withRPKI || (role == RoleRouteCollector && policy == CollectorObserve)
					if platform == "bird2" {
						if retained && (strings.Count(config, "import table yes;") != len(p) || strings.Count(config, "import keep filtered yes;") != len(p) || strings.Count(config, "receive limit 100 action disable;") != len(p)) {
							t.Fatal("BIRD pre-policy cache lacks all-received limit", role, policy, withRPKI)
						}
						if !retained && (strings.Contains(config, "import table yes;") || strings.Contains(config, "import keep filtered yes;")) {
							t.Fatal("unexpected BIRD cache")
						}
					} else {
						if retained && (strings.Count(config, "soft-reconfiguration inbound") != len(p) || strings.Count(config, "maximum-prefix 100 force") != len(p)) {
							t.Fatal("FRR pre-policy cache lacks all-received limit", role, policy, withRPKI)
						}
						if !retained && strings.Contains(config, "soft-reconfiguration inbound") {
							t.Fatal("unexpected FRR cache")
						}
					}
					if _, err := RedactConfig(platform, config); err != nil {
						t.Fatal("cache-bound profile lost safe preview", err)
					}
				}
			}
		}
	}
}
