package routing

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRuntimeRPKIRendering(t *testing.T) {
	r, p := fixture()
	r.RPKI = &RPKIConfig{Host: "127.0.0.1", Policy: "require-valid", ExpireSeconds: 600}
	bird, err := RenderFor("bird2", r, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"expire keep 600", "import table yes", "rpki reload on", "roa_check(md_roa4, net, bgp_path.last) != ROA_VALID", "roa_check(md_roa6, net, bgp_path.last) != ROA_VALID"} {
		if !strings.Contains(bird, required) {
			t.Fatalf("missing %q", required)
		}
	}
	frr, err := RenderFor("frr", r, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"rpki expire_interval 600", "rpki cache tcp 127.0.0.1 323 preference 1", "match rpki valid", "soft-reconfiguration inbound"} {
		if !strings.Contains(frr, required) {
			t.Fatalf("missing %q", required)
		}
	}
	if err = ValidateManagedFRR(frr); err != nil {
		t.Fatalf("watchdog rejected supported RPKI profile: %v", err)
	}
	for _, platform := range []string{"junos", "routeros7"} {
		if _, err = RenderFor(platform, r, p); err == nil {
			t.Fatal("unsupported vendor silently dropped RPKI policy")
		}
	}
	r.RPKI.Policy = "reject-invalid"
	if _, err = Render(r, p); err == nil {
		t.Fatal("runtime policy failed open on unknown")
	}
	r.RPKI.Policy = "require-valid"
	r.RPKI.Host = "127.0.0.1; include bad"
	if _, err = Render(r, p); err == nil {
		t.Fatal("cache host injected config")
	}
}
func TestNativeRuntimeRPKI(t *testing.T) {
	r, p := pairedFixture()
	r.RPKI = &RPKIConfig{Host: "127.0.0.1", Port: 3323, Policy: "require-valid", ExpireSeconds: 600}
	if binary := os.Getenv("BIRD_BIN"); binary != "" {
		config, err := RenderFor("bird2", r, p)
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidateFor(context.Background(), "bird2", binary, config); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Log("BIRD_BIN unset: native BIRD RPKI check skipped")
	}
	if binary := os.Getenv("FRR_BIN"); binary != "" && os.Getenv("MOEDOVE_FRR_MODULE_DIR") != "" {
		config, err := RenderFor("frr", r, p)
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidateFor(context.Background(), "frr", binary, config); err != nil {
			t.Fatal(err)
		}
		if err = ValidateFor(context.Background(), "frr", binary, config+"invalid-rpki-config-command\n"); err == nil {
			t.Fatal("RTR transport-log filtering hid a real parse error")
		}
	} else {
		t.Log("FRR RPKI module not selected: native FRR RPKI check skipped")
	}
}
