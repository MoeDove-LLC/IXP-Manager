package lookingglass

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func queryFixture(platform, view string) Query {
	return Query{RouterID: "router-a", PeerID: "peer-a", Platform: platform, NeighborAddress: "192.0.2.2", NeighborASN: 64513, Family: 4, View: view, MaxRows: 50, OwnershipRevision: strings.Repeat("a", 64)}
}

const birdIdentityFixture = "0001 BIRD 2.17.5 ready.\n2002-Name Proto Table State Since Info\n1002-peer_a BGP --- up 12:00:00 Established\n1006-  BGP state: Established\n         Neighbor address: 192.0.2.2\n         Neighbor AS: 64513\n         Local AS: 64512\n       Channel ipv4\n         State: UP\n         Table: master4\n         Routes: 2 imported, 1 filtered, 0 exported, 2 preferred\n"
const birdRouteFixture = "0001 BIRD 2.17.5 ready.\n1007-Table master4:\n     198.51.100.0/24 unicast [peer_a 12:00:00] * (100) [AS64513i]\n1008-\tBGP.as_path: 64513 {64520 64521}\n     \tBGP.next_hop: 192.0.2.2\n1012-\tBGP.community: (65535,65281) (64513,1)\n     \tBGP.large_community: (64512,1101,7)\n"
const frrSummaryFixture = `{"ipv4Unicast":{"routerId":"192.0.2.1","as":64512,"peers":{"192.0.2.2":{"remoteAs":64513,"state":"Established","pfxRcd":1}}}}`
const frrRoutesFixture = `{"routes":{"198.51.100.0/24":[{"path":"64513 {64520,64521}","peerId":"192.0.2.2","nexthops":[{"ip":"192.0.2.2"}]}]}}`
const openNeighborsFixture = `{"neighbors":[{"remote_as":"64513","remote_addr":"192.0.2.2","state":"Established","stats":{"prefixes":{"received":1,"sent":2}},"config":{"authentication":"md5sig"},"my_shutdown_reason":"PrivateRawTextDoNotUpload"}]}`
const openRoutesFixture = `{"rib":[{"prefix":"198.51.100.0/24","aspath":"64513 {64520 64521}","exit_nexthop":"192.0.2.2","true_nexthop":"192.0.2.2","neighbor":{"remote_addr":"192.0.2.2","bgp_id":"192.0.2.20"},"valid":true,"communities":["NO_EXPORT","64513:1"],"large_communities":["64512:1101:7"]}]}`

func TestTypedPathSegmentsAndBoundaries(t *testing.T) {
	path, e := ParseASPath("64513 {64520,64521} (64530 64531) [64540,64541] 64550")
	if e != nil {
		t.Fatal(e)
	}
	for i, kind := range []string{"sequence", "set", "confed-sequence", "confed-set", "sequence"} {
		if path[i].Type != kind {
			t.Fatal("unordered/confederation segment was flattened")
		}
	}
	for _, bad := range []string{"64513 private-text", "64513 {64520", "64513 {64520 [64521]}", "{64513,,64514}", "{64513,}", "4294967296", strings.Repeat("1 ", 513)} {
		if _, e := ParseASPath(bad); e == nil {
			t.Fatal("invalid/oversize AS path accepted")
		}
	}
	q := queryFixture("bird2", ViewAccepted)
	for _, change := range []func(*Query){func(q *Query) { q.NeighborAddress = "192.0.2.2; configure" }, func(q *Query) { q.Prefix = "198.51.100.1/24" }, func(q *Query) { q.MaxRows = 201 }, func(q *Query) { q.View = "arbitrary command" }, func(q *Query) { q.OwnershipRevision = "stale" }} {
		bad := q
		change(&bad)
		if _, e := NormalizeQuery(bad); e == nil {
			t.Fatal("invalid query accepted")
		}
		if _, e := BuildFRRRouteCommand(bad); e == nil {
			t.Fatal("command builder bypassed query validation")
		}
	}
}

func TestPlatformRouteFixturesPreserveScopeAndCommunities(t *testing.T) {
	for _, major := range []string{"2.17.5", "3.4.0"} {
		q := queryFixture("bird2", ViewFiltered)
		if strings.HasPrefix(major, "3") {
			q.Platform = "bird3"
		}
		raw := strings.ReplaceAll(birdRouteFixture, "2.17.5", major)
		if q.Platform == "bird3" {
			raw = strings.NewReplacer("BGP.as_path", "bgp_path", "BGP.next_hop", "bgp_next_hop", "BGP.community", "bgp_community", "BGP.large_community", "bgp_large_community").Replace(raw)
		}
		routes, e := ParseBIRDRoutes([]byte(raw), q, "peer_a")
		if e != nil || len(routes) != 1 {
			t.Fatal("BIRD native format not parsed", major, e)
		}
		if routes[0].ASPath[1].Type != "set" || routes[0].Filtered == nil || !*routes[0].Filtered || len(routes[0].Communities) != 2 || len(routes[0].LargeCommunities) != 1 {
			t.Fatal("BIRD attributes lost")
		}
		if _, e = ParseBIRDRoutes([]byte(strings.Replace(raw, "[peer_a ", "[other_peer ", 1)), q, "peer_a"); e == nil {
			t.Fatal("other member's route accepted")
		}
	}
	q := queryFixture("frr", ViewAccepted)
	routes, e := ParseFRRRoutes([]byte(frrRoutesFixture), q)
	if e != nil || len(routes) != 1 {
		t.Fatal(e)
	}
	if routes[0].Communities != nil || len(routes[0].UnavailableAttributes) != 2 || routes[0].ASPath[1].Type != "set" {
		t.Fatal("FRR summary fabricated empty communities or flattened set")
	}
	q.View = ViewPrePolicy
	pre := `{"receivedRoutes":{"198.51.100.0/24":{"path":"64513 64520","nextHop":"192.0.2.2"}},"totalPrefixCounter":1}`
	routes, e = ParseFRRRoutes([]byte(pre), q)
	if e != nil || len(routes) != 1 || routes[0].Filtered != nil {
		t.Fatal("FRR pre-policy was reclassified", e)
	}
	args, e := BuildFRRRouteCommand(q)
	if e != nil || strings.Contains(strings.Join(args, " "), " detail") {
		t.Fatal("pre-policy command would apply FRR's policy-modifying detail path")
	}
	q = queryFixture("openbgpd", ViewAccepted)
	routes, e = ParseOpenBGPDRoutes([]byte(openRoutesFixture), q)
	if e != nil || len(routes) != 1 || routes[0].Communities[0] != [2]uint32{65535, 65281} {
		t.Fatal("OpenBGPD real JSON formatter fields not parsed", e)
	}
	for _, raw := range []string{`{"unknown":[]}`, `{"status":"FAILED","error":"PrivateRawTextDoNotUpload"}`, `{"rib":"wrong"}`} {
		if _, e := ParseOpenBGPDRoutes([]byte(raw), q); e == nil {
			t.Fatal("unknown/error native format fabricated empty table")
		}
	}
	if Supported("openbgpd", ViewTable) || Supported("openbgpd", ViewFiltered) {
		t.Fatal("unproven OpenBGPD views advertised")
	}
}
func executable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if e := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); e != nil {
		t.Fatal(e)
	}
	return path
}
func quote(raw string) string { return "'" + strings.ReplaceAll(raw, "'", "'\\''") + "'" }
func fakeBird(t *testing.T, mutateAfter bool, retention bool) (Config, string) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	status := birdIdentityFixture
	if !retention {
		status = strings.Replace(status, "2 imported, 1 filtered,", "2 imported,", 1)
	}
	after := status
	if mutateAfter {
		after = strings.ReplaceAll(status, "Neighbor AS: 64513", "Neighbor AS: 64514")
	}
	body := "marker=" + quote(filepath.Join(dir, "first")) + "\nlog=" + quote(calls) + "\nprintf '%s\\n' \"$*\" >> \"$log\"\ncase \"$*\" in\n *'show protocols all'*) if [ -f \"$marker\" ]; then printf '%s' " + quote(after) + "; else touch \"$marker\"; printf '%s' " + quote(status) + "; fi;;\n *'show route'*) printf '%s' " + quote(birdRouteFixture) + ";;\n *) exit 1;;\nesac\n"
	return Config{RouterID: "router-a", Platform: "bird2", Peers: map[string]string{"peer-a": "peer_a"}, Birdc: executable(t, dir, "birdc", body)}, calls
}
func TestExecuteIdentityBeforeAfterRetentionAndNoRawLeaks(t *testing.T) {
	q := queryFixture("bird2", ViewFiltered)
	c, calls := fakeBird(t, false, true)
	result := Execute(context.Background(), c, q)
	if result.Status != "ok" || !result.Complete || len(result.Routes) != 1 {
		t.Fatalf("query failed: %+v", result)
	}
	raw, _ := os.ReadFile(calls)
	if strings.Count(string(raw), "show protocols all") != 2 || strings.Count(string(raw), "show route") != 1 {
		t.Fatal("native identity not checked before/after query")
	}
	c, _ = fakeBird(t, true, true)
	result = Execute(context.Background(), c, q)
	if result.Status != "error" || len(result.Routes) != 0 || result.Complete {
		t.Fatal("changed native identity uploaded routes")
	}
	c, calls = fakeBird(t, false, false)
	result = Execute(context.Background(), c, q)
	if result.Status != "unsupported" || result.Complete {
		t.Fatal("missing filtered retention became a complete empty table")
	}
	raw, _ = os.ReadFile(calls)
	if strings.Contains(string(raw), "show route") {
		t.Fatal("unavailable view queried despite missing retention")
	}
	q.View = ViewTable
	q.PeerID = ""
	q.NeighborAddress = ""
	q.NeighborASN = 0
	q.ExpectedRouterID = "192.0.2.1"
	q.ExpectedRouterASN = 64512
	result = Execute(context.Background(), c, q)
	if result.Status != "unsupported" {
		t.Fatal("table ran without local opt-in")
	}
	q = queryFixture("bird2", ViewAccepted)
	c, _ = fakeBird(t, false, true)
	c.Birdc = executable(t, t.TempDir(), "bad", "echo 'private-password-do-not-upload' >&2; exit 1\n")
	result = Execute(context.Background(), c, q)
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "private-password") || result.Complete || len(result.Routes) != 0 {
		t.Fatal("native diagnostics escaped typed result")
	}
}

func TestResultValidationRejectsUnboundAndUnstructuredData(t *testing.T) {
	q := queryFixture("bird2", ViewAccepted)
	result := Result{Query: q, ObservedAt: time.Now().UTC(), Status: "error", ErrorCode: "format-unknown", Routes: []Route{}}
	if e := ValidateResult(result, q); e != nil {
		t.Fatal(e)
	}
	result.Identity.Address = "PrivateNativeOutput"
	if ValidateResult(result, q) == nil {
		t.Fatal("raw identity text accepted on error path")
	}
	result.Identity.Address = ""
	result.ErrorCode = "PrivateNativeOutput"
	if ValidateResult(result, q) == nil {
		t.Fatal("arbitrary diagnostics accepted as error code")
	}
	result.ErrorCode = "format-unknown"
	other := q
	other.PeerID = "other"
	if ValidateResult(result, other) == nil {
		t.Fatal("result accepted for another leased target")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWorkerLeaseRoundTripPinsTargetAndNeverFollowsRedirect(t *testing.T) {
	c, _ := fakeBird(t, false, true)
	q := queryFixture("bird2", ViewAccepted)
	lease := Lease{ID: "lease-one", Token: "opaque-private-lease-token", ExpiresAt: time.Now().Add(time.Minute), Query: q}
	calls := 0
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer private-test-token" {
			t.Fatal("worker token not confined to configured origin")
		}
		var payload string
		switch r.URL.Path {
		case "/api/looking-glass/claim":
			raw, _ := json.Marshal(ClaimResponse{Lease: &lease})
			payload = string(raw)
		case "/api/looking-glass/complete":
			var body Completion
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.LeaseID != lease.ID || body.LeaseToken != lease.Token || ValidateResult(body.Result, q) != nil {
				t.Fatal("completion did not preserve the lease binding")
			}
			payload = `{"ok":true,"duplicate":false}`
		default:
			t.Fatal("worker used an unexpected path")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})}
	if processed, e := RunOnce(context.Background(), client, "https://manager.example/api/monitoring/ingest", "private-test-token", c); e != nil || !processed || calls != 2 {
		t.Fatal("typed worker roundtrip failed", e)
	}
	client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://other.example/secret"}}, Body: io.NopCloser(strings.NewReader("PrivateServerError"))}, nil
	})
	if _, e := Claim(context.Background(), client, "https://manager.example", "private-test-token", c.RouterID); e == nil || strings.Contains(e.Error(), "PrivateServerError") {
		t.Fatal("redirect or private server body accepted")
	}
}

func TestRowTruncationAndOutputFailureAreExplicit(t *testing.T) {
	c, _ := fakeBird(t, false, true)
	program, e := os.ReadFile(c.Birdc)
	if e != nil {
		t.Fatal(e)
	}
	second := strings.TrimPrefix(strings.ReplaceAll(birdRouteFixture, "198.51.100.0/24", "203.0.113.0/24"), "0001 BIRD 2.17.5 ready.\n")
	program = []byte(strings.Replace(string(program), quote(birdRouteFixture), quote(birdRouteFixture+second), 1))
	if e = os.WriteFile(c.Birdc, program, 0700); e != nil {
		t.Fatal(e)
	}
	q := queryFixture("bird2", ViewAccepted)
	q.MaxRows = 1
	result := Execute(context.Background(), c, q)
	if result.Status != "truncated" || !result.Truncated || result.Complete || len(result.Routes) != 1 {
		t.Fatal("row cap was reported as a complete table", result.Status, result.ErrorCode)
	}
	c.Birdc = executable(t, t.TempDir(), "overflow", "python3 -c 'print(\"x\" * (5 * 1024 * 1024))'\n")
	result = Execute(context.Background(), c, q)
	if result.Status != "error" || result.ErrorCode != "output-limit" || result.Complete || len(result.Routes) != 0 {
		t.Fatal("output overflow became a successful empty result")
	}
}
