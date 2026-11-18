package policy

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func fixedTime() time.Time { return time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC) }
func roaFixture() ROASnapshot {
	now := fixedTime()
	return ROASnapshot{Version: 1, FetchedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Source: "validated-cache", ROAs: []ROA{{"192.0.2.0/24", 26, 64501}, {"192.0.2.0/25", 25, 64502}, {"2001:db8::/32", 48, 64501}, {"198.51.100.0/24", 24, 0}}}
}
func TestOriginValidationRFC6811(t *testing.T) {
	cases := []struct {
		prefix string
		asn    uint32
		want   Validation
	}{
		{"192.0.2.0/24", 64501, Valid}, {"192.0.2.0/25", 64501, Valid}, // less-specific valid beats contradictory specific
		{"192.0.2.0/25", 64502, Valid}, {"192.0.2.0/26", 64501, Valid}, {"192.0.2.0/27", 64501, Invalid},
		{"192.0.2.0/24", 64502, Invalid}, {"192.0.0.0/16", 64501, NotFound}, {"203.0.113.0/24", 64501, NotFound},
		{"2001:db8:1::/48", 64501, Valid}, {"2001:db8:1::/64", 64501, Invalid}, {"2001:db9::/32", 64501, NotFound},
		{"198.51.100.0/24", 64501, Invalid}, // AS0 VRP authorizes no usable BGP origin
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s-AS%d", tc.prefix, tc.asn), func(t *testing.T) {
			got, err := ValidateOrigin(roaFixture(), Route{Prefix: tc.prefix, OriginASN: tc.asn}, fixedTime())
			if err != nil || got != tc.want {
				t.Fatalf("got %s %v want %s", got, err, tc.want)
			}
		})
	}
}
func TestPolicySnapshotsFailClosed(t *testing.T) {
	for _, change := range []func(*ROASnapshot){func(s *ROASnapshot) { s.ExpiresAt = fixedTime() }, func(s *ROASnapshot) { s.ROAs[0].MaxLength = 23 }, func(s *ROASnapshot) { s.ROAs[0].Prefix = "192.0.2.1/24" }, func(s *ROASnapshot) { s.FetchedAt = fixedTime().Add(time.Hour) }, func(s *ROASnapshot) { s.ExpiresAt = s.FetchedAt.Add(48 * time.Hour) }} {
		s := roaFixture()
		change(&s)
		if state, err := ValidateOrigin(s, Route{Prefix: "192.0.2.0/24", OriginASN: 64501}, fixedTime()); err == nil || state != Unavailable {
			t.Fatal("invalid cache produced a usable validation result")
		}
	}
	index, err := NewROAIndex(roaFixture(), fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = index.Validate(Route{Prefix: "192.0.2.0/24", OriginASN: 64501}, fixedTime().Add(2*time.Hour)); err == nil {
		t.Fatal("reused index ignored expiry")
	}
}
func TestSimulatorExplicitPolicyAndASPath(t *testing.T) {
	rpki := roaFixture()
	request := Request{PeerASN: 64501, AllowedPrefixes: []string{"192.0.2.0/24", "192.0.2.0/25", "203.0.113.0/24"}, RPKI: &rpki, Routes: []Route{{Prefix: "192.0.2.0/24", OriginASN: 64501}, {Prefix: "192.0.2.0/25", OriginASN: 64502, ASPath: []uint32{64501, 64502}}, {Prefix: "203.0.113.0/24", OriginASN: 64501}}}
	d, err := Simulate(request, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if !d[0].Accepted || d[0].PathValidated || !d[1].Accepted || !d[1].PathValidated || !d[2].Accepted || d[2].RPKI != NotFound {
		t.Fatalf("unexpected decisions %+v", d)
	}
	request.RejectRPKINotFound = true
	d, err = Simulate(request, fixedTime())
	if err != nil || d[2].Accepted {
		t.Fatal("strict unknown route accepted")
	}
	request.Routes[1].ASPath = []uint32{64503, 64502}
	d, err = Simulate(request, fixedTime())
	if err != nil || d[1].Accepted {
		t.Fatal("wrong first AS accepted")
	}
	request.Routes[1].ASPath = nil
	d, err = Simulate(request, fixedTime())
	if err != nil || d[1].Accepted {
		t.Fatal("transit origin assumed without AS path")
	}
	request.RPKI = nil
	if _, err = Simulate(request, fixedTime()); err == nil {
		t.Fatal("strict RPKI without snapshot accepted")
	}
	request.RejectRPKINotFound = false
	request.RequireIRR = true
	if _, err = Simulate(request, fixedTime()); err == nil {
		t.Fatal("required IRR silently skipped")
	}
}

type fakeIRR struct {
	responses map[string]string
	queries   []string
}

func (f *fakeIRR) Query(_ context.Context, q string) (string, error) {
	f.queries = append(f.queries, q)
	r, ok := f.responses[q]
	if !ok {
		return "", fmt.Errorf("unexpected query %s", q)
	}
	return r, nil
}
func TestRecursiveIRRAndInverseAuthorization(t *testing.T) {
	f := &fakeIRR{responses: map[string]string{
		"-r -T as-set AS-EXAMPLE":                      "as-set: AS-EXAMPLE\nmembers: AS64501,\n AS-CHILD\nmbrs-by-ref: MNT-OK\n",
		"-r -T as-set AS-CHILD":                        "as-set: AS-CHILD\nmembers: AS64502\n",
		"-r -T aut-num,as-set -i member-of AS-EXAMPLE": "aut-num: AS64503\nmember-of: AS-EXAMPLE\nmnt-by: MNT-OK\n\naut-num: AS64504\nmember-of: AS-EXAMPLE\nmnt-by: MNT-UNTRUSTED\n",
		"-r -T route,route6 -i origin AS64501":         "route: 192.0.2.0/24\norigin: AS64501\nsource: TEST\n\nroute6: 2001:db8::/32\norigin: AS64501\n",
		"-r -T route,route6 -i origin AS64502":         "route: 198.51.100.0/24\norigin: AS64502\n",
		"-r -T route,route6 -i origin AS64503":         "% No entries found\n",
	}}
	s, err := CollectIRR(context.Background(), f, "as-example", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ASNs) != 3 || len(s.Routes) != 3 {
		t.Fatalf("expansion lost or trusted unauthorized members: %+v", s)
	}
	proposals, err := CandidatePrefixes(s, 64501, time.Now())
	if err != nil || len(proposals) != 2 {
		t.Fatalf("candidate prefixes: %v %v", proposals, err)
	}
	r := Request{PeerASN: 64501, AllowedPrefixes: proposals, IRR: &s, RequireIRR: true, Routes: []Route{{Prefix: "192.0.2.0/24", OriginASN: 64501}, {Prefix: "198.51.100.0/24", OriginASN: 64501}}}
	d, err := Simulate(r, time.Now())
	if err != nil || !d[0].Accepted || d[1].Accepted {
		t.Fatal("IRR origin authorization not enforced")
	}
}
func TestIRRLimitsCyclesAndMalformedRPSL(t *testing.T) {
	cycle := &fakeIRR{responses: map[string]string{"-r -T as-set AS-A": "as-set: AS-A\nmembers: AS-B\n", "-r -T as-set AS-B": "as-set: AS-B\nmembers: AS-A\n"}}
	if s, err := CollectIRR(context.Background(), cycle, "AS-A", time.Hour); err == nil || s.Version != 0 {
		t.Fatal("cycle returned partial usable snapshot")
	}
	for _, change := range []func(*Limits){func(l *Limits) { l.MaxQueries = 1 }, func(l *Limits) { l.MaxDepth = 1 }, func(l *Limits) { l.MaxResponseBytes = 3 }, func(l *Limits) { l.MaxTotalBytes = 3 }, func(l *Limits) { l.MaxASNs = 1 }} {
		f := &fakeIRR{responses: map[string]string{"-r -T as-set AS-A": "as-set: AS-A\nmembers: AS-B,AS64501\n", "-r -T as-set AS-B": "as-set: AS-B\nmembers: AS-C,AS64502\n", "-r -T as-set AS-C": "as-set: AS-C\nmembers: AS64503\n"}}
		limits := DefaultLimits()
		change(&limits)
		if _, err := CollectIRRWithLimits(context.Background(), f, "AS-A", time.Hour, limits); err == nil {
			t.Fatal("collection bound ignored")
		}
	}
	f := &fakeIRR{}
	if _, err := CollectIRR(context.Background(), f, "AS-OK\n-i origin AS64500", time.Hour); err == nil || len(f.queries) != 0 {
		t.Fatal("WHOIS query injection accepted")
	}
	for _, raw := range []string{"route: 192.0.2.1/24\norigin: AS64501", "route6: 192.0.2.0/24\norigin: AS64501", "route: 192.0.2.0/24\norigin: AS64501\norigin: AS64502", "% ERROR rate limit exceeded"} {
		if _, err := ParseRoutes(raw); err == nil {
			t.Fatalf("bad RPSL accepted: %s", raw)
		}
	}
}
func TestRPSLContinuationAndDuplicateRoutes(t *testing.T) {
	raw := "route: 192.0.2.0/24 # comment\norigin: AS64501\ndescr: text\n+ more text\n\nroute: 192.0.2.0/24\norigin: AS64501\n"
	routes, err := ParseRoutes(raw)
	if err != nil || len(routes) != 1 {
		t.Fatalf("parse/deduplicate: %v %v", routes, err)
	}
	if _, err := ParseRoutes(strings.Repeat("x", 1<<20) + "\n"); err == nil {
		t.Fatal("unbounded RPSL line accepted")
	}
}

func TestFullASSetCandidatePairsAndStoredAuthorization(t *testing.T) {
	now := fixedTime()
	s := IRRSnapshot{Version: 1, FetchedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Source: "test IRR", ASSet: "AS-EXAMPLE", ASNs: []uint32{64501, 64502}, Routes: []Route{{Prefix: "192.0.2.0/24", OriginASN: 64501}, {Prefix: "192.0.2.0/24", OriginASN: 64502}}}
	own, err := CandidateRoutes(s, 64501, false, now)
	if err != nil || len(own) != 1 {
		t.Fatal("default unexpectedly broadened to downstream origins")
	}
	all, err := CandidateRoutes(s, 64501, true, now)
	if err != nil || len(all) != 2 {
		t.Fatal("full AS-SET proposal dropped origin association")
	}
	if _, err = CandidateRoutes(s, 64503, true, now); err == nil {
		t.Fatal("unrelated peer accepted full AS-SET")
	}
	request := Request{PeerASN: 64501, AllowedPrefixes: []string{"192.0.2.0/24"}, AllowedRoutes: own, Routes: []Route{{Prefix: "192.0.2.0/24", OriginASN: 64502, ASPath: []uint32{64501, 64502}}}}
	d, err := Simulate(request, now)
	if err != nil || d[0].Accepted {
		t.Fatal("simulation ignored reviewed origin mapping")
	}
	request.AllowedRoutes = all
	d, err = Simulate(request, now)
	if err != nil || !d[0].Accepted {
		t.Fatal("explicitly authorized downstream rejected")
	}
}

func TestOriginStateIndependentFromPathPolicy(t *testing.T) {
	s := roaFixture()
	path := make([]uint32, 65)
	for i := range path {
		path[i] = 64501
	}
	route := Route{Prefix: "192.0.2.0/24", OriginASN: 64501, ASPath: path}
	state, err := ValidateOrigin(s, route, fixedTime())
	if err != nil || state != Valid {
		t.Fatal("path length policy changed RPKI state")
	}
	decisions, err := Simulate(Request{PeerASN: 64501, AllowedPrefixes: []string{route.Prefix}, RPKI: &s, Routes: []Route{route}}, fixedTime())
	if err != nil || decisions[0].Accepted || decisions[0].RPKI != Valid {
		t.Fatal("rejected path did not retain RPKI state")
	}
}
func TestIRRAmbiguousSourcesRejected(t *testing.T) {
	f := &fakeIRR{responses: map[string]string{"-r -T as-set AS-EXAMPLE": "as-set: AS-EXAMPLE\nmembers: AS64501\nsource: RIPE\n\nas-set: AS-EXAMPLE\nmembers: AS64502\nsource: RADB\n"}}
	if _, err := CollectIRR(context.Background(), f, "AS-EXAMPLE", time.Hour); err == nil {
		t.Fatal("ambiguous registry objects silently unioned")
	}
}
func TestIRRInverseMembershipCannotCrossSources(t *testing.T) {
	f := &fakeIRR{responses: map[string]string{"-r -T as-set AS-EXAMPLE": "as-set: AS-EXAMPLE\nmbrs-by-ref: ANY\nsource: RIPE\n", "-r -T aut-num,as-set -i member-of AS-EXAMPLE": "aut-num: AS64501\nmember-of: AS-EXAMPLE\nsource: RADB\n"}}
	snapshot, err := CollectIRR(context.Background(), f, "AS-EXAMPLE", time.Hour)
	if err != nil || len(snapshot.ASNs) != 0 {
		t.Fatal("inverse member from unrelated registry trusted")
	}
}

func TestAbsentSnapshotCollectionsAreNotEmptyCaches(t *testing.T) {
	s := roaFixture()
	s.ROAs = nil
	if _, err := NewROAIndex(s, fixedTime()); err == nil {
		t.Fatal("missing ROA collection accepted as usable empty cache")
	}
	s.ROAs = []ROA{}
	if _, err := NewROAIndex(s, fixedTime()); err != nil {
		t.Fatal("explicit empty cache is valid not-found evidence")
	}
	now := fixedTime()
	irr := IRRSnapshot{Version: 1, FetchedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Source: "test", ASSet: "AS-EXAMPLE"}
	if err := ValidateIRRSnapshot(irr, now); err == nil {
		t.Fatal("missing IRR collections accepted")
	}
}
