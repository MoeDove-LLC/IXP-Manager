package routing

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestPlatformPolicyParity(t *testing.T) {
	r, p := fixture()
	for _, platform := range []string{"bird2", "frr", "routeros7", "junos"} {
		t.Run(platform, func(t *testing.T) {
			config, err := RenderFor(platform, r, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"198.51.100.0/24", "2001:db8:1::/48"} {
				if !strings.Contains(config, prefix) {
					t.Errorf("missing authorized prefix %s", prefix)
				}
			}
			if strings.Contains(config, "Untrusted name") || strings.Contains(config, "/etc/shadow") {
				t.Fatal("untrusted name interpolated")
			}
			p[0].Address = "192.0.2.2; execute malicious"
			if _, err := RenderFor(platform, r, p); err == nil {
				t.Fatal("adapter bypassed common validation")
			}
			p[0].Address = "192.0.2.2"
		})
	}
	if _, err := RenderFor("cisco", r, p); err == nil {
		t.Fatal("unsupported vendor silently accepted")
	}
	if err := ValidateFor(context.Background(), "routeros7", "", "script"); err == nil {
		t.Fatal("preview-only platform labeled validated")
	}
}

func TestNativeFRRIntegration(t *testing.T) {
	binary := os.Getenv("FRR_BIN")
	if binary == "" {
		t.Skip("set FRR_BIN to run bgpd native dry-run")
	}
	r, p := fixture()
	config, err := RenderFor("frr", r, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateFor(context.Background(), "frr", binary, config); err != nil {
		t.Fatalf("%v\n%s", err, config)
	}
	if err := ValidateFor(context.Background(), "frr", binary, "nonsense-config-command\n"); err == nil {
		t.Fatal("native FRR accepted invalid command")
	}
}
