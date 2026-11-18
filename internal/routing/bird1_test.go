package routing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func bird1Input(family int) (Router, []Peer) {
	r := Router{ID: "synthetic-bird1", Name: "untrusted name; include must not interpolate", Platform: "bird1", ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "192.0.2.1"}
	p := []Peer{{ID: "peer-b", ASN: 64512, Address: "192.0.2.2", Enabled: true, MaxPrefixes: 50, AllowedPrefixes: []string{"203.0.113.0/24", "198.51.100.0/24"}, AllowedRoutes: []PrefixOrigin{{Prefix: "198.51.100.0/24", OriginASN: 64513}, {Prefix: "203.0.113.0/24", OriginASN: 64514}}, Password: "Synthetic_BIRD1_Key", AuthenticationRevision: "passwordAAAAAAAAAAAAAA"}, {ID: "peer-a", ASN: 4200000001, Address: "192.0.2.3", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"192.0.2.0/24"}}}
	if family == 6 {
		r.LocalIPv4 = ""
		r.LocalIPv6 = "2001:db8::1"
		p[0].Address = "2001:db8::2"
		p[1].Address = "2001:db8::3"
		p[0].AllowedPrefixes = []string{"2001:db8:2::/48", "2001:db8:1::/48"}
		p[0].AllowedRoutes = []PrefixOrigin{{Prefix: "2001:db8:1::/48", OriginASN: 64513}, {Prefix: "2001:db8:2::/48", OriginASN: 64514}}
		p[1].AllowedPrefixes = []string{"2001:db8:3::/48"}
	}
	return r, p
}
func TestBIRD1DedicatedPoliciesAndRedaction(t *testing.T) {
	for _, family := range []int{4, 6} {
		for _, role := range []string{RoleRouteServer, RoleRouteCollector} {
			for _, policy := range []string{CollectorObserve, CollectorAuthorizedOnly} {
				if role == RoleRouteServer && policy == CollectorAuthorizedOnly {
					continue
				}
				r, p := bird1Input(family)
				r.Role = role
				if role == RoleRouteCollector {
					r.CollectorPolicy = policy
				}
				config, e := RenderFor("bird1", r, p)
				if e != nil {
					t.Fatal(e)
				}
				actual, e := BIRD1Family(config)
				if e != nil || actual != family {
					t.Fatal("family binding", actual, e)
				}
				for _, want := range []string{"local as 64500;", "source address ", "if bgp_path.first != 64512 then reject;", "if from != bgp_next_hop then reject;", "if bgp_path.len > 64 then reject;", "bgp_path.last = 64513", "import limit 50 action block;"} {
					if !strings.Contains(config, want) {
						t.Fatal("missing policy", want)
					}
				}
				for _, forbidden := range []string{"protocol kernel", "protocol direct", "protocol static", "ipv4 {", "ipv6 {", "import table", "authentication md5", "next hop self", "bgp_path.prepend", "bgp_next_hop =", "untrusted name; include"} {
					if strings.Contains(config, forbidden) {
						t.Fatal("unsafe/foreign-family syntax", forbidden)
					}
				}
				if role == RoleRouteCollector {
					if strings.Count(config, "export none;") != 2 || strings.Contains(config, "rs client;") || strings.Contains(config, "export filter") {
						t.Fatal("collector can export")
					}
					kept := strings.Count(config, "import keep filtered yes;")
					limited := strings.Count(config, "receive limit")
					if policy == CollectorObserve && (kept != 2 || limited != 2) || policy == CollectorAuthorizedOnly && (kept != 0 || limited != 0) {
						t.Fatal("filtered observation cache not bounded")
					}
				} else if strings.Count(config, "rs client;") != 2 || strings.Count(config, "next hop keep;") != 2 || strings.Count(config, "source != RTS_BGP") != 2 {
					t.Fatal("route server transparency/source guard missing")
				}
				preview, e := RedactConfig("bird1", config)
				if e != nil || strings.Contains(preview, p[0].Password) || !strings.Contains(preview, RedactedBGPPassword) {
					t.Fatal("unsafe redaction", e)
				}
				if e = ValidateManagedBIRD1(preview); e == nil {
					t.Fatal("redacted credential authorized deployment")
				}
				lines := strings.Split(config, "\n")
				raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(lines[1], bird1ModelPrefix))
				if strings.Contains(string(raw), p[0].Password) || strings.Contains(string(raw), `"Password"`) {
					t.Fatal("model persisted authentication secret")
				}
				shuffled := []Peer{p[1], p[0]}
				again, e := RenderFor("bird1", r, shuffled)
				if e != nil || again != config {
					t.Fatal("input order changed reviewed bytes")
				}
			}
		}
	}
}
func TestBIRD1RejectsUnsupportedAndMixedTargets(t *testing.T) {
	for _, name := range []string{"dual-local", "no-local", "mixed-peer", "wrong-prefix", "partial-origins", "collector-empty-authorized", "runtime-rpki", "rfc1997", "as112", "malformed-password"} {
		t.Run(name, func(t *testing.T) {
			r, p := bird1Input(4)
			switch name {
			case "dual-local":
				r.LocalIPv6 = "2001:db8::1"
			case "no-local":
				r.LocalIPv4 = ""
			case "mixed-peer":
				p[1].Address = "2001:db8::3"
			case "wrong-prefix":
				p[1].AllowedPrefixes = []string{"2001:db8::/32"}
			case "partial-origins":
				p[0].AllowedRoutes = p[0].AllowedRoutes[:1]
			case "collector-empty-authorized":
				r.Role = RoleRouteCollector
				r.CollectorPolicy = CollectorAuthorizedOnly
				p[0].AllowedPrefixes = nil
				p[0].AllowedRoutes = nil
			case "runtime-rpki":
				r.RPKI = &RPKIConfig{}
			case "rfc1997":
				value := true
				r.RFC1997Passthrough = &value
			case "as112":
				r.Role = RoleAS112
				r.ASN = 112
				r.AS112Service = "both"
				r.AS112IPv4 = true
			case "malformed-password":
				p[0].Password = "bad\";include \"file"
			}
			if config, e := RenderFor("bird1", r, p); e == nil || config != "" {
				t.Fatal("unsupported model accepted", name)
			}
		})
	}
}
func TestBIRD1EmptyObservationStillDeniesExport(t *testing.T) {
	r, p := bird1Input(4)
	r.Role = RoleRouteCollector
	r.CollectorPolicy = CollectorObserve
	for i := range p {
		p[i].AllowedPrefixes = nil
		p[i].AllowedRoutes = nil
	}
	config, e := RenderBIRD1(r, p)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(config, "    accept;") || strings.Count(config, "    reject;") != 2 || strings.Count(config, "export none;") != 2 || strings.Count(config, "receive limit") != 2 {
		t.Fatal("empty allowlist became import-all or unbounded")
	}
}
func TestBIRD1ExactProfileRejectsInjectedBehavior(t *testing.T) {
	r, p := bird1Input(4)
	config, e := RenderBIRD1(r, p)
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{
		config + "protocol kernel { export all; }\n", config + "include \"/etc/foreign\";\n",
		strings.Replace(config, "next hop keep;", "next hop self;", 1), strings.Replace(config, "source != RTS_BGP", "source = RTS_BGP", 1),
		strings.Replace(config, "if from != bgp_next_hop then reject;", "", 1), strings.Replace(config, "import limit 50 action block;", "import limit 5000 action block;", 1),
		strings.Replace(config, "bgp_path.last = 64513", "bgp_path.last = 64515", 1), strings.Replace(config, "198.51.100.0/24", "198.51.100.0/24+", 1),
		strings.Replace(config, "  password \"Synthetic_BIRD1_Key\";", "  password \"unexpected-secret with spaces\";", 1),
	} {
		if e := ValidateManagedBIRD1(bad); e == nil {
			t.Fatal("modified managed profile accepted")
		}
		if preview, e := RedactBIRD1(bad); e == nil || preview != "" || strings.Contains(e.Error(), "unexpected-secret") {
			t.Fatal("unrecognized profile exposed content")
		}
	}
	r.Role = RoleRouteCollector
	r.CollectorPolicy = CollectorObserve
	config, e = RenderBIRD1(r, p)
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(config, "export none;", "export all;", 1), strings.Replace(config, "  receive limit 50 action disable;\n", "", 1)} {
		if e := ValidateManagedBIRD1(bad); e == nil {
			t.Fatal("collector isolation change accepted")
		}
	}
}
func TestBIRD1FamilyBindingAndNewerMajorRefusal(t *testing.T) {
	r, p := bird1Input(6)
	config, e := RenderBIRD1(r, p)
	if e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(config, "\n")
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(lines[1], bird1ModelPrefix))
	var model bird1Model
	if json.Unmarshal(raw, &model) != nil {
		t.Fatal("model")
	}
	model.Family = 4
	raw, _ = json.Marshal(model)
	lines[1] = bird1ModelPrefix + base64.RawURLEncoding.EncodeToString(raw)
	if _, e = BIRD1Family(strings.Join(lines, "\n")); e == nil {
		t.Fatal("family comment could change checker selection")
	}
	for _, major := range []int{2, 3} {
		if e = validateBIRDArtifactMajor(config, major); e == nil {
			t.Fatal("BIRD1 mislabeled for newer checker")
		}
	}
	if _, e = birdMajor(1); e == nil {
		t.Fatal("native BIRD1 parser addition silently enabled live apply")
	}
	var found bool
	for _, c := range Capabilities() {
		if c.Platform == "bird1" {
			found = true
			if !c.Render || !c.Validate || !c.Apply || !c.Rollback || !c.Fetch || c.ValidationLevel != "native" || !reflect.DeepEqual(c.Roles, []string{RoleRouteServer, RoleRouteCollector}) {
				t.Fatal("overstated BIRD1 capabilities", c)
			}
		}
	}
	if !found {
		t.Fatal("missing capability")
	}
}
func TestBIRD1ActualNativeFamilyMatrix(t *testing.T) {
	bins := map[int]string{4: os.Getenv("BIRD1_BIN"), 6: os.Getenv("BIRD1_IPV6_BIN")}
	if bins[4] == "" || bins[6] == "" {
		t.Skip("set both actual BIRD 1 bird and bird6 parser fixtures")
	}
	for _, family := range []int{4, 6} {
		for _, profile := range []string{"server", CollectorObserve, CollectorAuthorizedOnly, "empty-observation", "no-peers"} {
			t.Run(string(rune('0'+family))+"/"+profile, func(t *testing.T) {
				r, p := bird1Input(family)
				if profile == CollectorObserve || profile == CollectorAuthorizedOnly || profile == "empty-observation" {
					r.Role = RoleRouteCollector
					r.CollectorPolicy = profile
					if profile == "empty-observation" {
						r.CollectorPolicy = CollectorObserve
						for i := range p {
							p[i].AllowedPrefixes = nil
							p[i].AllowedRoutes = nil
						}
					}
				}
				if profile == "no-peers" {
					p = nil
				}
				config, e := RenderBIRD1(r, p)
				if e != nil {
					t.Fatal(e)
				}
				if e = ValidateFor(context.Background(), "bird1", bins[family], config); e != nil {
					t.Fatal("actual matching BIRD 1 parser", e)
				}
				other := 4
				if family == 4 {
					other = 6
				}
				if e = ValidateFor(context.Background(), "bird1", bins[other], config); e == nil {
					t.Fatal("wrong-family native binary accepted")
				}
				if newer := os.Getenv("BIRD_BIN"); newer != "" {
					version, err := run(context.Background(), newer, "--version")
					match := birdVersionLine.FindStringSubmatch(version)
					if err != nil || match == nil || match[1] != "2" {
						t.Fatal("actual BIRD2 negative-control fixture unavailable")
					}
					if e = ValidateFor(context.Background(), "bird1", newer, config); e == nil || !strings.Contains(e.Error(), "major") {
						t.Fatal("BIRD2 used in place of BIRD1", e)
					}
				}
			})
		}
	}
	// Native parsing itself rejects a malformed configuration, independently of
	// the managed-profile reconstruction gate above.
	config := "router id 192.0.2.1;\nprotocol bgp broken { local as ; }\n"
	f, e := os.CreateTemp("", "bird1-negative-*.conf")
	if e != nil {
		t.Fatal(e)
	}
	defer os.Remove(f.Name())
	f.WriteString(config)
	f.Close()
	if _, e = run(context.Background(), bins[4], "-p", "-c", f.Name()); e == nil {
		t.Fatal("actual BIRD1 parser accepted invalid syntax")
	}
}
