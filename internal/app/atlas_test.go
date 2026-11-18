package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/atlas"
)

type atlasFake struct {
	creates          int
	stops            []int64
	uncertain        bool
	creationError    string
	receiptFailure   bool
	deferStop        bool
	missingTarget    bool
	extraSourceProbe bool
	resultLimit      int
	resultReads      int
	beforeDiscover   func()
	beforeCreate     func()
	wrongPublic      bool
	specs            map[int64]atlas.Spec
	states           map[int64]string
}

func (f *atlasFake) Origin() string { return atlas.OfficialOrigin }
func (f *atlasFake) Discover(_ context.Context, asn uint32, af int, _ bool) ([]atlas.Probe, []byte, error) {
	if f.beforeDiscover != nil {
		f.beforeDiscover()
	}
	id := int64(asn-64500) * 11
	p := atlas.Probe{ID: id, ASNV4: asn, ASNV6: asn, AddressV4: fmt.Sprintf("198.51.100.%d", id), AddressV6: fmt.Sprintf("2001:db8::%x", id), IsPublic: true, Status: "connected", LastConnected: time.Now().UTC()}
	probes := []atlas.Probe{p}
	if f.extraSourceProbe && asn == 64501 {
		extra := p
		extra.ID = 12
		extra.AddressV4 = "198.51.100.12"
		probes = append(probes, extra)
	}
	return probes, []byte(`{"fixture":"private probe snapshot"}`), nil
}
func (f *atlasFake) Create(_ context.Context, s atlas.Spec) (int64, []byte, error) {
	f.creates++
	id := int64(1000 + f.creates)
	f.specs[id] = s
	f.states[id] = "scheduled"
	if f.beforeCreate != nil {
		f.beforeCreate()
	}
	if f.uncertain {
		code := f.creationError
		if code == "" {
			code = "transport-or-timeout"
		}
		return 0, nil, &atlas.Error{Code: code, Uncertain: true}
	}
	if f.receiptFailure {
		return id, []byte("{"), nil
	}
	return id, []byte(fmt.Sprintf(`{"measurements":[%d]}`, id)), nil
}
func (f *atlasFake) Measurement(_ context.Context, id int64) (atlas.Measurement, []byte, error) {
	s, ok := f.specs[id]
	if !ok {
		return atlas.Measurement{}, nil, &atlas.Error{Code: "http-404"}
	}
	m := atlas.Measurement{ID: id, Type: s.Type, AF: s.AF, Target: s.Target, Description: s.Description, IsPublic: s.IsPublic || f.wrongPublic, IsOneoff: s.IsOneoff, IntervalSeconds: s.IntervalSeconds, StartAt: s.StartAt, Status: f.states[id]}
	if !s.IsOneoff {
		m.StopAt = s.StartAt.Add(time.Duration(s.DurationSeconds) * time.Second)
	}
	raw, _ := json.Marshal(m)
	return m, raw, nil
}
func (f *atlasFake) Find(ctx context.Context, description string) ([]atlas.Measurement, []byte, error) {
	items := []atlas.Measurement{}
	for id, s := range f.specs {
		if s.Description == description {
			m, _, _ := f.Measurement(ctx, id)
			items = append(items, m)
		}
	}
	raw, _ := json.Marshal(items)
	return items, raw, nil
}
func (f *atlasFake) Results(_ context.Context, id int64, _, _ time.Time) ([]atlas.Result, []byte, error) {
	f.resultReads++
	s := f.specs[id]
	rtt := 1.2
	result := atlas.Result{MeasurementID: id, ProbeID: s.ProbeIDs[0], Type: s.Type, AF: s.AF, TargetAddress: s.Target, Timestamp: s.StartAt.Add(time.Second), Hops: []atlas.Hop{{Hop: 1, Replies: []atlas.Reply{{Address: "192.0.2.10", RTT: &rtt}, {Address: "192.0.2.11", RTT: &rtt}, {Timeout: true}}}}}
	if s.Type == "ping" {
		sent, received := 3, 0
		result.Hops = nil
		result.Sent = &sent
		result.Received = &received
	}
	if f.missingTarget {
		result.TargetAddress = ""
		result.Error = "probe-error"
		result.Hops = nil
		result.MinRTT, result.AvgRTT, result.MaxRTT = nil, nil, nil
	}
	count := len(s.ProbeIDs)
	if f.resultLimit > 0 && f.resultLimit < count {
		count = f.resultLimit
	}
	rows := make([]atlas.Result, 0, count)
	for _, probeID := range s.ProbeIDs[:count] {
		copy := result
		copy.ProbeID = probeID
		rows = append(rows, copy)
	}
	raw, _ := json.Marshal(rows)
	return rows, raw, nil
}
func (f *atlasFake) Stop(_ context.Context, id int64) error {
	if _, ok := f.specs[id]; !ok {
		return &atlas.Error{Code: "http-404"}
	}
	f.stops = append(f.stops, id)
	if !f.deferStop {
		f.states[id] = "stopped"
	}
	return nil
}

type atlasFixture struct {
	c        *client
	dir      string
	provider *atlasFake
	members  []Record
	vlan     Record
}

func newAtlasFixture(t *testing.T) atlasFixture {
	t.Helper()
	c, dir := setupClient(t)
	fake := &atlasFake{specs: map[int64]atlas.Spec{}, states: map[int64]string{}}
	c.a.atlasProvider = fake
	c.a.atlasConfig.Enabled = true
	location := c.create("locations", Record{"name": "Atlas DC"})
	sw := c.create("switches", Record{"name": "Atlas switch", "locationId": location["id"]})
	vlan := c.create("vlans", Record{"name": "Atlas peering", "tag": 123, "ipv4Prefix": "192.0.2.0/24"})
	members := []Record{}
	for i := 0; i < 2; i++ {
		m := c.create("members", Record{"name": fmt.Sprintf("Atlas member%d", i), "asn": 64501 + i, "status": "active"})
		port := c.create("ports", Record{"name": fmt.Sprintf("p%d", i), "switchId": sw["id"], "status": "active"})
		c.create("connections", Record{"name": fmt.Sprintf("Atlas uplink%d", i), "memberId": m["id"], "portId": port["id"], "vlanId": vlan["id"], "ipv4": fmt.Sprintf("192.0.2.%d", 10+i), "status": "active"})
		members = append(members, m)
		c.ok("POST", "/api/atlas/probes/discover", Record{"memberId": m["id"], "af": 4})
	}
	return atlasFixture{c, dir, fake, members, vlan}
}
func (f atlasFixture) single() Record {
	return Record{"name": "Single private trace", "sourceMemberId": f.members[0]["id"], "targetMemberId": f.members[1]["id"], "targetType": "probe", "targetId": "22", "type": "traceroute", "af": 4, "probeIds": []int{11}, "isOneoff": true}
}
func (f atlasFixture) approve(t *testing.T, proposal Record) string {
	t.Helper()
	path := "/api/atlas/runs/" + str(proposal, "id")
	f.c.ok("POST", path+"/approve", Record{"checksum": proposal["checksum"], "acknowledgeCredits": true, "acknowledgePublic": proposal["isPublic"], "currentPassword": "very-long-test-password"})
	return path
}
func TestAtlasSingleAndMatrixLifecycleEncryptedSnapshots(t *testing.T) {
	f := newAtlasFixture(t)
	c := f.c
	p := c.ok("POST", "/api/atlas/runs", f.single())
	path := f.approve(t, p)
	run := c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
	if run["status"] != "scheduled" || f.provider.creates != 1 {
		t.Fatal(run)
	}
	for id, s := range f.provider.specs {
		if s.IsPublic || len(s.ProbeIDs) != 1 || s.ProbeIDs[0] != 11 {
			t.Fatal("private explicit probe policy lost")
		}
		f.provider.states[id] = "stopped"
	}
	if err := c.a.atlasTick(context.Background(), time.Now().UTC().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	results := c.ok("GET", path+"/results", nil)
	items := results["items"].([]any)
	if len(items) != 1 {
		t.Fatal(results)
	}
	hops := items[0].(map[string]any)["hops"].([]any)
	if len(hops[0].(map[string]any)["replies"].([]any)) != 3 {
		t.Fatal("ECMP/timeouts lost")
	}
	state := c.a.store.Snapshot()
	if err := ValidateAtlasArtifacts(f.dir, state, c.a.store.securityKey); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(f.dir, "state.json"))
	if strings.Contains(string(raw), "private probe snapshot") || strings.Contains(string(raw), `"replies"`) {
		t.Fatal("raw arrays entered hot state")
	}
	for _, a := range state.Atlas.Artifacts {
		bytes, _ := os.ReadFile(filepath.Join(f.dir, "atlas", a.ID+".enc"))
		if strings.Contains(string(bytes), "private probe snapshot") || strings.Contains(string(bytes), "MoeDove Atlas") {
			t.Fatal("artifact stored plaintext")
		}
	}
	if w := c.request("POST", path+"/execute", Record{"checksum": p["checksum"]}); w.Code != 409 {
		t.Fatal("completed creation repeated")
	}
	c.ok("POST", path+"/prune", Record{"checksum": p["checksum"], "reason": "retention test", "acknowledge": true})
	if w := c.request("GET", path+"/results", nil); w.Code != 410 {
		t.Fatal("pruned results exposed")
	}
	c.ok("DELETE", path, Record{"checksum": p["checksum"], "reason": "metadata retention test", "acknowledge": true})
	retained := c.a.store.Snapshot()
	if len(retained.Atlas.CreditBuckets) != 1 || retained.Atlas.CreditBuckets[0].Reserved != 60 {
		t.Fatal("history pruning erased the conservative credit reservation")
	}
	matrix := Record{"name": "Two-way private matrix", "mode": "matrix", "vlanId": f.vlan["id"], "participants": []Record{{"memberId": f.members[0]["id"], "probeIds": []int{11}, "targetProbeId": 11}, {"memberId": f.members[1]["id"], "probeIds": []int{22}, "targetProbeId": 22}}, "type": "traceroute", "af": 4, "isOneoff": true}
	p = c.ok("POST", "/api/atlas/runs", matrix)
	if p["measurementCount"] != float64(2) || p["estimatedCredits"] != float64(120) {
		t.Fatal("directed matrix/cost incorrect", p)
	}
	path = f.approve(t, p)
	c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
	if err := c.a.atlasTick(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 3 {
		t.Fatal("matrix did not create exactly two directions")
	}
	c.ok("POST", path+"/stop", Record{"checksum": p["checksum"]})
	if err := c.a.atlasTick(context.Background(), time.Now().UTC().Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(f.provider.stops) != 2 {
		t.Fatal("did not stop only the two owned matrix measurements")
	}
	cache := c.ok("GET", "/api/atlas/probes?memberId="+str(f.members[0], "id")+"&af=4", nil)["snapshot"].(map[string]any)
	c.ok("DELETE", "/api/atlas/probes/"+cache["id"].(string), Record{"checksum": cache["checksum"], "reason": "release bounded probe cache", "acknowledge": true})
	if response := c.ok("GET", "/api/atlas/probes?memberId="+str(f.members[0], "id")+"&af=4", nil); response["snapshot"] != nil || response["stale"] != true {
		t.Fatal("deleted cache still returned")
	}
}
func TestAtlasUncertainCreateReconcilesWithoutRepeatingPOST(t *testing.T) {
	f := newAtlasFixture(t)
	f.provider.uncertain = true
	f.provider.creationError = "http-202"
	p := f.c.ok("POST", "/api/atlas/runs", f.single())
	path := f.approve(t, p)
	run := f.c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
	if run["status"] != "uncertain" || f.provider.creates != 1 {
		t.Fatal(run)
	}
	if run["lastError"] != "http-202" {
		t.Fatal("unexpected accepted response was not preserved as an uncertain outcome")
	}
	if w := f.c.request("POST", path+"/execute", Record{"checksum": p["checksum"]}); w.Code != 409 {
		t.Fatal("uncertain create retried")
	}
	f.c.ok("POST", path+"/refresh", Record{})
	if f.provider.creates != 1 {
		t.Fatal("reconciliation sent another POST")
	}
	state := f.c.a.store.Snapshot()
	m := atlasRun(&state, str(p, "id")).Items[0]
	if !m.OwnershipConfirmed || m.ProviderID == 0 {
		t.Fatal("receipt could not be safely reconciled")
	}
	// Tampered metadata cannot rebind an authentic encrypted receipt.
	state.Atlas.Runs[0].Items[0].ProviderID++
	if err := ValidateAtlasArtifacts(f.dir, state, f.c.a.store.securityKey); err == nil {
		t.Fatal("rebound provider ID passed artifact verification")
	}
}
func TestAtlasApprovalScopeCSRFAndStaleness(t *testing.T) {
	f := newAtlasFixture(t)
	p := f.c.ok("POST", "/api/atlas/runs", f.single())
	path := "/api/atlas/runs/" + str(p, "id")
	if w := f.c.request("POST", path+"/approve", Record{"checksum": p["checksum"], "acknowledgeCredits": true, "currentPassword": "wrong"}); w.Code != 403 {
		t.Fatal("reauth not enforced")
	}
	if w := f.c.request("POST", path+"/approve", Record{"checksum": p["checksum"], "currentPassword": "very-long-test-password"}); w.Code != 409 {
		t.Fatal("credits acknowledgment not enforced")
	}
	csrf := f.c.csrf
	f.c.csrf = "wrong"
	if w := f.c.request("POST", path+"/execute", Record{"checksum": p["checksum"]}); w.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	f.c.csrf = csrf
	rawToken := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "Atlas reader", "currentPassword": "very-long-test-password", "scopes": []string{"atlas:read"}}), "token")
	if w := bearerRequest(f.c.a, rawToken, "GET", "/api/atlas", nil); w.Code != 200 {
		t.Fatal("reader failed")
	}
	if w := bearerRequest(f.c.a, rawToken, "POST", path+"/approve", Record{"checksum": p["checksum"]}); w.Code != 403 {
		t.Fatal("token approved external spend")
	}
	member := editableCredentialPeer(f.members[1])
	member["asn"] = 64503
	f.c.ok("PUT", "/api/resources/members/"+str(f.members[1], "id"), member)
	if w := f.c.request("POST", path+"/approve", Record{"checksum": p["checksum"], "acknowledgeCredits": true, "currentPassword": "very-long-test-password"}); w.Code != 409 {
		t.Fatal("changed target member ASN approved")
	}
	if f.provider.creates != 0 {
		t.Fatal("invalid draft created a measurement")
	}
}
func TestAtlasConcurrentExecuteAndWrongPrivacyStop(t *testing.T) {
	f := newAtlasFixture(t)
	p := f.c.ok("POST", "/api/atlas/runs", f.single())
	path := f.approve(t, p)
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- f.c.request("POST", path+"/execute", Record{"checksum": p["checksum"]}).Code
		}()
	}
	wg.Wait()
	close(codes)
	ok := 0
	for code := range codes {
		if code == 202 {
			ok++
		}
	}
	if ok != 1 || f.provider.creates != 1 {
		t.Fatal("creation intent was not serialized")
	}
	f.provider.wrongPublic = true
	f.c.ok("POST", path+"/refresh", Record{})
	state := f.c.a.store.Snapshot()
	r := atlasRun(&state, str(p, "id"))
	if !r.StopRequested || !r.Items[0].OwnershipConfirmed {
		t.Fatal("policy mismatch neither stopped nor owned safely")
	}
	if err := f.c.a.atlasTick(context.Background(), time.Now().UTC().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(f.provider.stops) != 1 {
		t.Fatal("wrong-public owned measurement not stopped")
	}
}

func TestAtlasRestartPreservesUncertainIntentAndClosesOldGeneration(t *testing.T) {
	f := newAtlasFixture(t)
	f.provider.uncertain = true
	p := f.c.ok("POST", "/api/atlas/runs", f.single())
	path := f.approve(t, p)
	f.c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
	if err := f.c.a.store.Update(func(s *State) error {
		r := atlasRun(s, str(p, "id"))
		r.Items[0].Status = "creating"
		r.ExecutionAuthorizedAt = time.Now().UTC()
		atlasRunStatus(r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old := f.c.a
	old.Close()
	next, err := New(Config{DataDir: f.dir, BirdBinary: "/nonexistent/bird"})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	next.atlasProvider = f.provider
	next.atlasConfig.Enabled = true
	s := next.store.Snapshot()
	r := atlasRun(&s, str(p, "id"))
	if r.Items[0].Status != "uncertain" || !r.ExecutionAuthorizedAt.IsZero() {
		t.Fatal("interrupted POST was not paused as uncertain")
	}
	if err := old.atlasTick(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 1 {
		t.Fatal("closed generation submitted work")
	}
	if err := old.cleanupAtlasArtifacts(); err == nil {
		t.Fatal("closed generation changed replacement instance artifacts")
	}
	c := docLoginClient(t, next, "admin@example.net", "very-long-test-password")
	c.ok("POST", path+"/refresh", Record{})
	if f.provider.creates != 1 {
		t.Fatal("restart reconciliation repeated paid POST")
	}
	s = next.store.Snapshot()
	r = atlasRun(&s, str(p, "id"))
	if !r.Items[0].OwnershipConfirmed {
		t.Fatal("unique persisted intent was not recovered")
	}
	if err := ValidateAtlasArtifacts(f.dir, s, make([]byte, 32)); err == nil {
		t.Fatal("wrong encryption key accepted")
	}
	if err := ValidateAtlasArtifacts(f.dir, s, next.store.securityKey); err != nil {
		t.Fatal(err)
	}
}

func TestAtlasCreditAdmissionNeverSendsPastLocalBound(t *testing.T) {
	f := newAtlasFixture(t)
	p := f.c.ok("POST", "/api/atlas/runs", f.single())
	path := f.approve(t, p)
	if err := f.c.a.store.Update(func(s *State) error {
		s.Atlas.CreditBuckets = []AtlasCreditBucket{{Hour: time.Now().Unix() / 3600, Reserved: 100000}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
	s := f.c.a.store.Snapshot()
	r := atlasRun(&s, str(p, "id"))
	if f.provider.creates != 0 || !r.Items[0].AttemptedAt.IsZero() || r.Items[0].LastError != "execution-admission-failed" || !r.ExecutionAuthorizedAt.IsZero() {
		t.Fatal("credit cap sent a request or did not pause safely")
	}
	if err := f.c.a.store.Update(func(s *State) error {
		s.Atlas.CreditBuckets = nil
		for i := int64(1); i <= 25; i++ {
			s.Atlas.CreditBuckets = append(s.Atlas.CreditBuckets, AtlasCreditBucket{Hour: time.Now().Unix()/3600 + i, Reserved: 1})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
	s = f.c.a.store.Snapshot()
	if f.provider.creates != 0 || len(s.Atlas.CreditBuckets) != 25 {
		t.Fatal("backward clock expanded reservation history or sent a POST")
	}
	if err := validateAtlasState(&s); err != nil {
		t.Fatal("clock admission corrupted restartable state", err)
	}
}

func TestAtlasReceiptCheckpointFailureIsImmediatelyUncertain(t *testing.T) {
	f := newAtlasFixture(t)
	f.provider.receiptFailure = true
	p := f.c.ok("POST", "/api/atlas/runs", f.single())
	path := f.approve(t, p)
	run := f.c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
	if run["status"] != "uncertain" || f.provider.creates != 1 {
		t.Fatal("local receipt failure was not tracked as uncertain", run)
	}
	if err := f.c.a.atlasTick(context.Background(), time.Now().UTC().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 1 {
		t.Fatal("checkpoint recovery repeated POST")
	}
	s := f.c.a.store.Snapshot()
	if !atlasRun(&s, str(p, "id")).Items[0].OwnershipConfirmed {
		t.Fatal("unique owned creation did not reconcile after failed checkpoint")
	}
}

func TestAtlasStopInitialDispatchAndRetriesAreDistinct(t *testing.T) {
	f := newAtlasFixture(t)
	p := f.c.ok("POST", "/api/atlas/runs", f.single())
	path := f.approve(t, p)
	f.c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
	f.provider.deferStop = true
	f.c.ok("POST", path+"/stop", Record{"checksum": p["checksum"]})
	if len(f.provider.stops) != 1 {
		t.Fatal("initial stop not dispatched")
	}
	if err := f.c.a.atlasTick(context.Background(), time.Now().UTC().Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(f.provider.stops) != 1 {
		t.Fatal("asynchronous stop retried before backoff")
	}
	if err := f.c.a.atlasTick(context.Background(), time.Now().UTC().Add(70*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(f.provider.stops) != 2 {
		t.Fatal("bounded stop retry did not resume")
	}
	draft := f.c.ok("POST", "/api/atlas/runs", f.single())
	draftPath := "/api/atlas/runs/" + str(draft, "id")
	f.c.a.atlasProvider = nil
	local := f.c.ok("POST", draftPath+"/stop", Record{"checksum": draft["checksum"]})
	if local["status"] != "stopped" || f.provider.creates != 1 {
		t.Fatal("disabled-provider local cancellation failed")
	}
	f.c.ok("POST", draftPath+"/prune", Record{"checksum": draft["checksum"], "reason": "unused draft retention", "acknowledge": true})
}

func TestAtlasOwnedProbeErrorPreservesUnknownObservedTarget(t *testing.T) {
	f := newAtlasFixture(t)
	f.provider.missingTarget = true
	p := f.c.ok("POST", "/api/atlas/runs", f.single())
	path := f.approve(t, p)
	f.c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
	for id := range f.provider.states {
		f.provider.states[id] = "stopped"
	}
	if err := f.c.a.atlasTick(context.Background(), time.Now().UTC().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	result := f.c.ok("GET", path+"/results", nil)
	rows := result["items"].([]any)
	if len(rows) != 1 {
		t.Fatal("owned probe error lost", result)
	}
	row := rows[0].(map[string]any)
	if row["targetAddress"] != "" || row["error"] != "probe-error" || row["avgRtt"] != nil {
		t.Fatal("unknown target replaced with plan or fabricated latency")
	}
	s := f.c.a.store.Snapshot()
	if err := ValidateAtlasArtifacts(f.dir, s, f.c.a.store.securityKey); err != nil {
		t.Fatal("legitimate error snapshot failed integrity binding", err)
	}
}

func TestAtlasLatePartialResultsRefreshOnlyWithinBoundedGrace(t *testing.T) {
	f := newAtlasFixture(t)
	f.provider.extraSourceProbe = true
	f.c.ok("POST", "/api/atlas/probes/discover", Record{"memberId": f.members[0]["id"], "af": 4})
	proposal := f.single()
	proposal["probeIds"] = []int{11, 12}
	createPartial := func() string {
		t.Helper()
		f.provider.resultLimit = 1
		p := f.c.ok("POST", "/api/atlas/runs", proposal)
		path := f.approve(t, p)
		f.c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
		for id := range f.provider.states {
			f.provider.states[id] = "stopped"
		}
		return str(p, "id")
	}
	id := createPartial()
	now := time.Now().UTC().Add(2 * time.Minute)
	if err := f.c.a.atlasTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	s := f.c.a.store.Snapshot()
	m := atlasRun(&s, id).Items[0]
	if m.Status != "completed" || m.ResultCount != 1 || m.EstimatedSamples != 2 {
		t.Fatal("fixture did not stop with a partial result")
	}
	f.provider.resultLimit = 0
	if err := f.c.a.atlasTick(context.Background(), now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if f.provider.resultReads != 1 {
		t.Fatal("late results bypassed minute polling interval")
	}
	if err := f.c.a.atlasTick(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	s = f.c.a.store.Snapshot()
	if atlasRun(&s, id).Items[0].ResultCount != 2 || f.provider.creates != 1 {
		t.Fatal("late selected probe result was not added without another POST")
	}
	id = createPartial()
	if err := f.c.a.atlasTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	reads := f.provider.resultReads
	f.provider.resultLimit = 0
	if err := f.c.a.atlasTick(context.Background(), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if f.provider.resultReads != reads {
		t.Fatal("partial results retried beyond bounded grace")
	}
	f.c.a.atlasMu.Lock()
	err := f.c.a.atlasWorkOneRefresh(context.Background(), id, now.Add(2*time.Hour))
	f.c.a.atlasMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s = f.c.a.store.Snapshot()
	if atlasRun(&s, id).Items[0].ResultCount != 2 || f.provider.creates != 2 {
		t.Fatal("manual late-result refresh failed or repeated creation")
	}
}

func TestAtlasExpiredMatrixApprovalRenewsOnlyUnattemptedRemainder(t *testing.T) {
	f := newAtlasFixture(t)
	request := Record{"name": "Renewable directed matrix", "mode": "matrix", "vlanId": f.vlan["id"], "participants": []Record{{"memberId": f.members[0]["id"], "probeIds": []int{11}, "targetProbeId": 11}, {"memberId": f.members[1]["id"], "probeIds": []int{22}, "targetProbeId": 22}}, "type": "traceroute", "af": 4, "isOneoff": true}
	p := f.c.ok("POST", "/api/atlas/runs", request)
	path := f.approve(t, p)
	f.c.ok("POST", path+"/execute", Record{"checksum": p["checksum"]})
	s := f.c.a.store.Snapshot()
	original := atlasRun(&s, str(p, "id")).Items[0]
	before, _ := json.Marshal(original)
	if err := f.c.a.store.Update(func(s *State) error {
		r := atlasRun(s, str(p, "id"))
		r.ApprovedAt = time.Now().UTC().Add(-16 * time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.c.a.atlasTick(context.Background(), time.Now().UTC()); err == nil {
		t.Fatal("expired approval kept creating the matrix")
	}
	view := f.c.ok("GET", path, nil)
	if view["canReapprove"] != true || view["approvalExpired"] != true || view["canResume"] != false || view["remainingEstimatedCredits"] != float64(60) || view["remainingEstimatedSamples"] != float64(1) {
		t.Fatal("remaining review contract incorrect", view)
	}
	if w := f.c.request("POST", path+"/resume", Record{"checksum": p["checksum"]}); w.Code != 409 {
		t.Fatal("old approval resumed without reauthentication")
	}
	review := Record{"checksum": p["checksum"], "acknowledgeCredits": true, "currentPassword": "wrong"}
	if w := f.c.request("POST", path+"/approve", review); w.Code != 403 {
		t.Fatal("renewal skipped current-password authentication")
	}
	review["currentPassword"] = "very-long-test-password"
	review["acknowledgeCredits"] = false
	if w := f.c.request("POST", path+"/approve", review); w.Code != 409 {
		t.Fatal("renewal skipped credit acknowledgment")
	}
	review["acknowledgeCredits"] = true
	for _, status := range []string{"uncertain", "creating"} {
		if err := f.c.a.store.Update(func(s *State) error {
			r := atlasRun(s, str(p, "id"))
			r.Items[0].Status = status
			atlasRunStatus(r)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if w := f.c.request("POST", path+"/approve", review); w.Code != 409 {
			t.Fatal("unresolved creation was reapproved", status)
		}
	}
	if err := f.c.a.store.Update(func(s *State) error {
		r := atlasRun(s, str(p, "id"))
		r.Items[0].Status = original.Status
		atlasRunStatus(r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	renewed := f.c.ok("POST", path+"/approve", review)
	if renewed["checksum"] != p["checksum"] || renewed["resumeRequired"] != true || renewed["approvalExpired"] != false || renewed["canResume"] != true {
		t.Fatal("renewal did not preserve reviewed plan or require resume")
	}
	s = f.c.a.store.Snapshot()
	r := atlasRun(&s, str(p, "id"))
	after, _ := json.Marshal(r.Items[0])
	if string(before) != string(after) || !r.ExecutionAuthorizedAt.IsZero() || f.provider.creates != 1 {
		t.Fatal("renewal modified attempted item/receipt or automatically executed")
	}
	if err := f.c.a.atlasTick(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 1 {
		t.Fatal("background executed before explicit resume")
	}
	f.c.ok("POST", path+"/resume", Record{"checksum": p["checksum"]})
	if f.provider.creates != 2 {
		t.Fatal("resume did not create exactly the unattempted remainder")
	}
	s = f.c.a.store.Snapshot()
	r = atlasRun(&s, str(p, "id"))
	if r.ResumeRequired {
		t.Fatal("accepted resume left its requirement flag set")
	}
	after, _ = json.Marshal(r.Items[0])
	if string(before) != string(after) {
		t.Fatal("resume rewrote the earlier direction")
	}
	if err := ValidateAtlasArtifacts(f.dir, s, f.c.a.store.securityKey); err != nil {
		t.Fatal(err)
	}
	if w := f.c.request("POST", path+"/resume", Record{"checksum": p["checksum"]}); w.Code != 409 {
		t.Fatal("fully attempted run resumed again")
	}
}

type atlasBlockedReader struct {
	reader  io.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *atlasBlockedReader) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.release })
	return b.reader.Read(p)
}

func TestAtlasAuthorityRecheckedAfterMutexAndNetwork(t *testing.T) {
	for _, mode := range []string{"generation", "session-revoked"} {
		t.Run(mode, func(t *testing.T) {
			f := newAtlasFixture(t)
			a := f.c.a
			a.mu.Lock()
			session := a.sessions[f.c.cookie.Value]
			a.mu.Unlock()
			s := a.store.Snapshot()
			user := withMember(find(&s, "users", session.UserID), session.MemberID)
			// Model a request that authenticated before waiting for atlasMu.
			a.atlasMu.Lock()
			started := make(chan struct{})
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				close(started)
				w := httptest.NewRecorder()
				a.atlasRoutes(w, httptest.NewRequest("GET", "/api/atlas", nil), user, session)
				done <- w
			}()
			<-started
			if mode == "generation" {
				if err := a.store.Update(func(s *State) error { invalidateSecurity(s, session.UserID); return nil }); err != nil {
					t.Fatal(err)
				}
			} else {
				a.revokeSessions(session.UserID, "")
			}
			a.atlasMu.Unlock()
			if response := <-done; response.Code != 403 {
				t.Fatal("stale authenticated request survived Atlas lock wait", response.Code)
			}
			// A fresh session starts discovery; revoke it while provider I/O waits.
			c := docLoginClient(t, a, "admin@example.net", "very-long-test-password")
			a.mu.Lock()
			session = a.sessions[c.cookie.Value]
			a.mu.Unlock()
			before := a.store.Snapshot().Atlas.ProbeSnapshots[0].ID
			networkStarted, release := make(chan struct{}), make(chan struct{})
			f.provider.beforeDiscover = func() { close(networkStarted); <-release }
			go func() {
				done <- c.request("POST", "/api/atlas/probes/discover", Record{"memberId": f.members[0]["id"], "af": 4})
			}()
			<-networkStarted
			if mode == "generation" {
				if err := a.store.Update(func(s *State) error { invalidateSecurity(s, session.UserID); return nil }); err != nil {
					t.Fatal(err)
				}
			} else {
				a.revokeSessions(session.UserID, "")
			}
			close(release)
			if response := <-done; response.Code != 403 {
				t.Fatal("late discovery committed after revocation", response.Code, response.Body.String())
			}
			if after := a.store.Snapshot().Atlas.ProbeSnapshots[0].ID; after != before {
				t.Fatal("revoked discovery replaced private snapshot")
			}
		})
	}
}

func TestAtlasExecuteRechecksActorAtMutationButKeepsPaidReceipt(t *testing.T) {
	f := newAtlasFixture(t)
	p := f.c.ok("POST", "/api/atlas/runs", f.single())
	path := f.approve(t, p)
	a := f.c.a
	raw, _ := json.Marshal(Record{"checksum": p["checksum"]})
	reader := &atlasBlockedReader{reader: strings.NewReader(string(raw)), started: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest("POST", path+"/execute", reader)
	request.AddCookie(f.c.cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", f.c.csrf)
	request.RemoteAddr = "127.0.0.1:10000"
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); a.ServeHTTP(w, request); done <- w }()
	<-reader.started
	a.mu.Lock()
	session := a.sessions[f.c.cookie.Value]
	a.mu.Unlock()
	if err := a.store.Update(func(s *State) error { invalidateSecurity(s, session.UserID); return nil }); err != nil {
		t.Fatal(err)
	}
	close(reader.release)
	if response := <-done; response.Code != 403 {
		t.Fatal("execute mutation used old generation", response.Code, response.Body.String())
	}
	if f.provider.creates != 0 {
		t.Fatal("stale execution contacted provider")
	}
	// Once the durable creating intent is authorized, revocation must not lose
	// the receipt for an external side effect that already occurred.
	c := docLoginClient(t, a, "admin@example.net", "very-long-test-password")
	started, release := make(chan struct{}), make(chan struct{})
	f.provider.beforeCreate = func() { close(started); <-release }
	go func() { done <- c.request("POST", path+"/execute", Record{"checksum": p["checksum"]}) }()
	<-started
	a.revokeSessions(session.UserID, "")
	close(release)
	response := <-done
	if response.Code != 202 {
		t.Fatal("authorized creation receipt discarded after revocation", response.Code, response.Body.String())
	}
	s := a.store.Snapshot()
	m := atlasRun(&s, str(p, "id")).Items[0]
	if f.provider.creates != 1 || !m.OwnershipConfirmed || m.ReceiptArtifactID == "" {
		t.Fatal("paid creation evidence lost")
	}
}

func TestAtlasReapprovalBeforeAnyAttemptRequiresResumeAndNeverRetimes(t *testing.T) {
	f := newAtlasFixture(t)
	p := f.c.ok("POST", "/api/atlas/runs", f.single())
	path := f.approve(t, p)
	if err := f.c.a.store.Update(func(s *State) error {
		atlasRun(s, str(p, "id")).ApprovedAt = time.Now().UTC().Add(-16 * time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	review := Record{"checksum": p["checksum"], "acknowledgeCredits": true, "currentPassword": "very-long-test-password"}
	renewed := f.c.ok("POST", path+"/approve", review)
	if renewed["status"] != "approved" || renewed["canResume"] != true || renewed["resumeRequired"] != true {
		t.Fatal("unstarted renewed run lacks explicit resume contract")
	}
	if w := f.c.request("POST", path+"/execute", Record{"checksum": p["checksum"]}); w.Code != 409 {
		t.Fatal("renewed run bypassed explicit resume")
	}
	f.c.ok("POST", path+"/resume", Record{"checksum": p["checksum"]})
	if f.provider.creates != 1 {
		t.Fatal("renewed unstarted run not created exactly once")
	}
	request := f.single()
	request["startAt"] = time.Now().UTC().Add(-30 * time.Second)
	scheduled := f.c.ok("POST", "/api/atlas/runs", request)
	scheduledPath := f.approve(t, scheduled)
	if err := f.c.a.store.Update(func(s *State) error {
		atlasRun(s, str(scheduled, "id")).ApprovedAt = time.Now().UTC().Add(-16 * time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	review["checksum"] = scheduled["checksum"]
	if w := f.c.request("POST", scheduledPath+"/approve", review); w.Code != 409 || !strings.Contains(w.Body.String(), "fixed start time") {
		t.Fatal("elapsed frozen schedule was silently renewed/retimed", w.Code, w.Body.String())
	}
	view := f.c.ok("GET", scheduledPath, nil)
	if view["canReapprove"] != false {
		t.Fatal("elapsed explicit schedule advertised renewable")
	}
	s := f.c.a.store.Snapshot()
	copy := *atlasRun(&s, str(scheduled, "id"))
	copy.Spec.StartAt = time.Time{}
	copy.StopRequested = true
	if atlasRenewable(copy, time.Now()) {
		t.Fatal("stopped run renewable")
	}
	copy.StopRequested = false
	copy.Pruned = true
	if atlasRenewable(copy, time.Now()) {
		t.Fatal("pruned run renewable")
	}
}
