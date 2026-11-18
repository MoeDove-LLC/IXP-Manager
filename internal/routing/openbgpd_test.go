package routing

import (
	"context"
	"os"
	"strings"
	"testing"
)

func openBGPDTestInput() (Router, []Peer) {
	r := Router{ID: "r", ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "192.0.2.1", LocalIPv6: "2001:db8::1"}
	p := []Peer{{ID: "p4", ASN: 64501, Address: "192.0.2.2", Enabled: true, MaxPrefixes: 20, AllowedPrefixes: []string{"198.51.100.0/24"}, AllowedRoutes: []PrefixOrigin{{Prefix: "198.51.100.0/24", OriginASN: 64502}}, Password: "testSecret_+=/", AuthenticationRevision: "0123456789abcdefghijkL"}, {ID: "p6", ASN: 64503, Address: "2001:db8::2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"2001:db8:1::/48"}}}
	return r, p
}
func TestOpenBGPDNativeProfiles(t *testing.T) {
	binary := os.Getenv("OPENBGPD_BIN")
	if binary == "" {
		t.Skip("set OPENBGPD_BIN for real offline parser")
	}
	r, p := openBGPDTestInput()
	for _, policy := range []string{"server", CollectorObserve, CollectorAuthorizedOnly} {
		t.Run(policy, func(t *testing.T) {
			if policy != "server" {
				r.Role = RoleRouteCollector
				r.CollectorPolicy = policy
			}
			config, err := RenderOpenBGPD(r, p)
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateOpenBGPD(context.Background(), binary, config); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestOpenBGPDManagedPolicyAndRedaction(t *testing.T) {
	r, p := openBGPDTestInput()
	config, err := RenderOpenBGPD(r, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fib-update no", "rde rib MOEDOVE", "transparent-as yes", "set nexthop no-modify", "enforce neighbor-as yes", "enforce local-as yes", "max-prefix 20", "deny quick from any max-as-len 64", "peer-as 64501", "source-as 64502"} {
		if !strings.Contains(config, want) {
			t.Fatal("missing", want)
		}
	}
	if strings.Contains(config, "community NO_") {
		t.Fatal("implicit community export policy changed transparent broker semantics")
	}
	if err = ValidateManagedOpenBGPD(config); err != nil {
		t.Fatal(err)
	}
	preview, err := RedactOpenBGPD(config)
	if err != nil || strings.Contains(preview, p[0].Password) || !strings.Contains(preview, RedactedBGPPassword) {
		t.Fatal("redaction failed", err)
	}
	if err = ValidateManagedOpenBGPD(preview); err == nil {
		t.Fatal("redacted artifact accepted")
	}
	for _, bad := range []string{strings.Replace(config, "fib-update no", "fib-update yes", 1), config + "include \"/tmp/arbitrary\"\n", strings.Replace(config, "max-prefix 20", "max-prefix 20000", 1), strings.Replace(config, "enforce neighbor-as yes", "enforce neighbor-as no", 1), strings.Replace(config, " source-as 64502", "", 1)} {
		if err := ValidateManagedOpenBGPD(bad); err == nil {
			t.Fatal("unmanaged mutation accepted")
		}
		if redacted, err := RedactOpenBGPD(bad); err == nil || redacted != "" {
			t.Fatal("unmanaged redaction exposed bytes")
		}
	}
	r.Role = RoleRouteCollector
	r.CollectorPolicy = CollectorObserve
	config, err = RenderOpenBGPD(r, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config, "rde rib MOEDOVE no evaluate") || !strings.Contains(config, "rde rib Loc-RIB include filtered") || strings.Contains(config, "allow to any") || strings.Count(config, "export none") != 2 {
		t.Fatal("collector isolation missing")
	}
	r.RPKI = &RPKIConfig{}
	if _, err = RenderOpenBGPD(r, p); err == nil {
		t.Fatal("unverified RPKI accepted")
	}
}

func TestOpenBGPDRFC1997PassthroughSemantics(t *testing.T) {
	trueValue, falseValue := true, false
	binary := os.Getenv("OPENBGPD_BIN")
	for _, role := range []string{RoleRouteServer, RoleRouteCollector} {
		for _, value := range []struct {
			name   string
			option *bool
		}{{"default", nil}, {"pass", &trueValue}, {"interpret", &falseValue}} {
			t.Run(role+"/"+value.name, func(t *testing.T) {
				r, peers := openBGPDTestInput()
				r.Role, r.RFC1997Passthrough = role, value.option
				config, err := RenderOpenBGPD(r, peers)
				if err != nil {
					t.Fatal(err)
				}
				model, err := parseManagedOpenBGPD(config)
				if err != nil {
					t.Fatal(err)
				}
				if (model.Router.RFC1997Passthrough == nil) != (value.option == nil) || value.option != nil && *model.Router.RFC1997Passthrough != *value.option {
					t.Fatal("typed community policy was not preserved in the strict model")
				}
				wantDeny := role == RoleRouteServer && value.option != nil && !*value.option
				for _, community := range []string{"NO_EXPORT", "NO_ADVERTISE", "NO_EXPORT_SUBCONFED"} {
					line := "deny quick to any community " + community + "\n"
					if strings.Contains(config, line) != wantDeny {
						t.Fatal("rendered community policy does not match explicit option", community)
					}
					if wantDeny && strings.Index(config, line) < strings.Index(config, "allow to any\n") {
						t.Fatal("community deny must follow the general export allow")
					}
					if wantDeny && ValidateManagedOpenBGPD(strings.Replace(config, line, "", 1)) == nil {
						t.Fatal("removing selected community enforcement passed strict rebuild")
					}
				}
				if role == RoleRouteCollector && (strings.Contains(config, "allow to any") || strings.Count(config, "  export none\n") != len(peers)) {
					t.Fatal("community option weakened collector export isolation")
				}
				if binary != "" {
					if err := ValidateOpenBGPD(context.Background(), binary, config); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
