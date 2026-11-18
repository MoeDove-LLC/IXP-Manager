package routing

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
)

func pairedFixture() (Router, []Peer) {
	r, p := fixture()
	p[0].AllowedPrefixes = append(p[0].AllowedPrefixes, "203.0.113.0/24")
	p[0].AllowedRoutes = []PrefixOrigin{{Prefix: "198.51.100.0/24", OriginASN: 64513}, {Prefix: "203.0.113.0/24", OriginASN: 64520}}
	p[1].AllowedRoutes = []PrefixOrigin{{Prefix: "2001:db8:1::/48", OriginASN: 64514}}
	return r, p
}
func TestPrefixOriginAcrossPlatforms(t *testing.T) {
	r, p := pairedFixture()
	expected := map[string][]string{
		"bird2":     {"bgp_path.last = 64520 && net ~ [ 203.0.113.0/24 ]", "bgp_path.first != 64513"},
		"frr":       {"_o64520", "^64513(_[0-9]+){0,62}_64520$", "match as-path md_"},
		"junos":     {"64513 .{0,62} 64520", "<address>203.0.113.0/24</address>"},
		"routeros7": {"dst == 203.0.113.0/24", "bgp-as-path ^64513 && bgp-as-path 64520\\$"},
	}
	for platform, need := range expected {
		config, err := RenderFor(platform, r, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, piece := range need {
			if !strings.Contains(config, piece) {
				t.Errorf("%s lost prefix-origin policy %q", platform, piece)
			}
		}
		if platform == "frr" {
			if err = ValidateManagedFRR(config); err != nil {
				t.Fatal(err)
			}
		}
	}
	p[0].AllowedRoutes = p[0].AllowedRoutes[:1]
	if _, err := Render(r, p); err == nil {
		t.Fatal("partial mapping silently fell back to prefix-only")
	}
}
func TestFRROriginRegexSemantics(t *testing.T) {
	for _, origin := range []uint32{64513, 64520} {
		pattern := strings.ReplaceAll(frrOriginPattern(64513, origin), "_", " ")
		re, err := regexp.CompilePOSIX(pattern)
		if err != nil {
			t.Fatal(err)
		}
		last := "64520"
		if origin == 64513 {
			last = "64513"
		}
		if !re.MatchString("64513 "+last) || !re.MatchString("64513 "+strings.Repeat("64500 ", 62)+last) {
			t.Fatal("authorized AS path rejected")
		}
		for _, bad := range []string{"64512 " + last, "64513 64599", "64513 {" + last + "}", "64513 " + strings.Repeat("64500 ", 63) + last} {
			if re.MatchString(bad) {
				t.Fatalf("unauthorized path matched %s", bad)
			}
		}
		if origin == 64513 && !re.MatchString("64513") {
			t.Fatal("direct origin was lost")
		}
	}
}
func TestNativePairedOriginPolicy(t *testing.T) {
	r, p := pairedFixture()
	for _, test := range []struct{ platform, env string }{{"bird2", "BIRD_BIN"}, {"frr", "FRR_BIN"}} {
		binary := os.Getenv(test.env)
		if binary == "" {
			continue
		}
		config, err := RenderFor(test.platform, r, p)
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidateFor(context.Background(), test.platform, binary, config); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFRRSameASPeersKeepDistinctOriginPolicy(t *testing.T) {
	r, p := fixture()
	p = p[:1]
	p[0].AllowedRoutes = []PrefixOrigin{{Prefix: "198.51.100.0/24", OriginASN: 64520}}
	second := p[0]
	second.ID = "same-as-second-peer"
	second.Address = "192.0.2.3"
	second.AllowedRoutes = []PrefixOrigin{{Prefix: "198.51.100.0/24", OriginASN: 64521}}
	p = append(p, second)
	config, err := RenderFor("frr", r, p)
	if err != nil {
		t.Fatal(err)
	}
	// Parse the emitted ingress terms and ACLs, then evaluate the source/path
	// conditions. This catches accidental unioning across same-AS neighbors.
	type term struct{ peer, acl string }
	terms := []term{}
	acls := map[string]*regexp.Regexp{}
	current := -1
	for _, line := range strings.Split(config, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == "!" {
			current = -1
			continue
		}
		if len(fields) >= 6 && fields[0] == "bgp" && fields[1] == "as-path" {
			re, e := regexp.CompilePOSIX(strings.ReplaceAll(fields[5], "_", " "))
			if e != nil {
				t.Fatal(e)
			}
			acls[fields[3]] = re
		}
		if len(fields) == 4 && fields[0] == "route-map" && fields[1] == "md_ingress" && fields[2] == "permit" {
			terms = append(terms, term{})
			current = len(terms) - 1
		}
		if current >= 0 && len(fields) == 3 && fields[0] == "match" {
			if fields[1] == "peer" {
				terms[current].peer = fields[2]
			}
			if fields[1] == "as-path" {
				terms[current].acl = fields[2]
			}
		}
	}
	accepts := func(source, path string) bool {
		for _, term := range terms {
			if term.peer == source && acls[term.acl] != nil && acls[term.acl].MatchString(path) {
				return true
			}
		}
		return false
	}
	if !accepts("192.0.2.2", "64513 64520") || accepts("192.0.2.2", "64513 64521") || !accepts("192.0.2.3", "64513 64521") || accepts("192.0.2.3", "64513 64520") {
		t.Fatal("same-AS neighbors' paired policies contaminated each other")
	}
}
func TestRouterAggregatePolicyBound(t *testing.T) {
	r, p := fixture()
	p[0].AllowedPrefixes = make([]string, 10000)
	for i := range p[0].AllowedPrefixes {
		p[0].AllowedPrefixes[i] = "198.51.100.0/24"
	}
	p = append(p, p[0], p[0])
	p[2].ID = "third"
	p[3].ID = "fourth"
	if _, err := RenderFor("junos", r, p); err == nil || !strings.Contains(err.Error(), "20000") {
		t.Fatal("router-wide bounded work not enforced before rendering")
	}
}
