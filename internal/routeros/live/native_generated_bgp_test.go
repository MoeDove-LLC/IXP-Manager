package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

const generatedSocket = "/scratch/bgp-generated.sock"

type generatedContext struct {
	SchemaVersion     int    `json:"schemaVersion"`
	Fixture           string `json:"fixture"`
	ControlSocket     string `json:"controlSocket"`
	RouterAddress     string `json:"routerAddress"`
	RouterASN         uint32 `json:"routerASN"`
	SenderAddress     string `json:"senderAddress"`
	SenderASN         uint32 `json:"senderASN"`
	SenderListener    string `json:"senderListener"`
	RecipientAddress  string `json:"recipientAddress"`
	RecipientASN      uint32 `json:"recipientASN"`
	RecipientListener string `json:"recipientListener"`
}
type generatedPathSegment struct {
	Type int      `json:"type"`
	ASNs []uint32 `json:"asns"`
}
type generatedRoute struct {
	PrefixID string                 `json:"prefixId"`
	Origin   int                    `json:"origin"`
	Path     []generatedPathSegment `json:"path"`
	NextHop  string                 `json:"nextHop"`
}
type generatedEvent struct {
	Sequence uint64          `json:"sequence"`
	Kind     string          `json:"kind"`
	PrefixID string          `json:"prefixId"`
	Removed  bool            `json:"removed"`
	Route    *generatedRoute `json:"route,omitempty"`
}
type generatedPeer struct {
	Established       bool             `json:"established"`
	Opens             int              `json:"opens"`
	AnnouncementsSent int              `json:"announcementsSent"`
	Refreshes         int              `json:"refreshes"`
	Ends              int              `json:"ends"`
	UpdatesReceived   int              `json:"updatesReceived"`
	EORsReceived      int              `json:"eorsReceived"`
	RIB               []generatedRoute `json:"rib"`
	Events            []generatedEvent `json:"events"`
}
type generatedObservation struct {
	SchemaVersion int    `json:"schemaVersion"`
	OK            bool   `json:"ok"`
	FailureCode   string `json:"failureCode"`
	Sequence      uint64 `json:"sequence"`
	Peers         struct {
		Sender    generatedPeer `json:"sender"`
		Recipient generatedPeer `json:"recipient"`
	} `json:"peers"`
}

// This error retains only fixed comparisons, never raw route/event attributes.
type generatedAttributeFailure struct {
	Code                                                             string
	OriginMatch, PathMatch, NextHopMatch                             bool
	PathEmpty, PathOneSequence, PathOnlySource, PathServerThenSource bool
	PathContainsServer, PathContainsRecipient                        bool
	NextHopServer, NextHopRecipient, NextHopGateway                  bool
	OriginIGP, OriginEGP, OriginIncomplete                           bool
}

func (*generatedAttributeFailure) Error() string { return "generated BGP attribute mismatch" }
func generatedAttributeError(route generatedRoute) error {
	e := &generatedAttributeFailure{OriginMatch: route.Origin == 0, NextHopMatch: route.NextHop == "10.0.2.100", PathOneSequence: len(route.Path) == 1 && route.Path[0].Type == 2, NextHopServer: route.NextHop == "10.0.2.15", NextHopRecipient: route.NextHop == "10.0.2.101", NextHopGateway: route.NextHop == "10.0.2.2", OriginIGP: route.Origin == 0, OriginEGP: route.Origin == 1, OriginIncomplete: route.Origin == 2}
	total := 0
	onlySource := true
	for _, segment := range route.Path {
		if segment.Type != 2 {
			onlySource = false
		}
		for _, asn := range segment.ASNs {
			total++
			onlySource = onlySource && asn == 64501
			e.PathContainsServer = e.PathContainsServer || asn == 64500
			e.PathContainsRecipient = e.PathContainsRecipient || asn == 64502
		}
	}
	e.PathEmpty = total == 0
	e.PathOnlySource = total > 0 && onlySource
	e.PathMatch = e.PathOneSequence && total == 1 && e.PathOnlySource
	e.PathServerThenSource = e.PathOneSequence && len(route.Path[0].ASNs) == 2 && route.Path[0].ASNs[0] == 64500 && route.Path[0].ASNs[1] == 64501
	switch {
	case !e.PathMatch:
		e.Code = "generated-as-path-changed"
	case !e.NextHopMatch:
		e.Code = "generated-next-hop-changed"
	case !e.OriginMatch:
		e.Code = "generated-origin-changed"
	default:
		return nil
	}
	return e
}

func generatedReadContext(t *testing.T) {
	t.Helper()
	raw, e := readFixedFile("/scratch/bgp-generated-context.json", 4096, true)
	if e != nil {
		t.Fatal("generated_bgp_context_valid=false")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var got generatedContext
	if decoder.Decode(&got) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("generated_bgp_context_valid=false")
	}
	wanted := generatedContext{1, "generated-two-peer-ipv4", generatedSocket, "10.0.2.15", 64500, "10.0.2.100", 64501, "127.0.0.1:11790", "10.0.2.101", 64502, "127.0.0.1:11791"}
	if got != wanted {
		t.Fatal("generated_bgp_context_valid=false")
	}
}
func generatedRead(ctx context.Context, action string) (generatedObservation, error) {
	var out generatedObservation
	if action != "snapshot" && action != "announce-once" && action != "allow-shutdown" {
		return out, fail("generated-control-action")
	}
	info, e := os.Lstat(generatedSocket)
	if e != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return out, fail("generated-control-socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return out, fail("generated-control-socket")
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, e := (&net.Dialer{}).DialContext(call, "unix", generatedSocket)
	if e != nil {
		return out, fail("generated-control-io")
	}
	defer conn.Close()
	deadline, _ := call.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return out, fail("generated-control-io")
	}
	request, _ := json.Marshal(struct {
		Action string `json:"action"`
	}{action})
	request = append(request, '\n')
	if n, e := conn.Write(request); e != nil || n != len(request) {
		return out, fail("generated-control-io")
	}
	raw, e := io.ReadAll(io.LimitReader(conn, 65537))
	if e != nil || len(raw) == 0 || len(raw) > 65536 {
		return out, fail("generated-control-response")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF {
		return out, fail("generated-control-response")
	}
	if out.SchemaVersion != 1 || !out.OK || out.FailureCode != "" || out.Sequence > 256 {
		return out, fail("generated-peer-failed")
	}
	for _, peer := range []generatedPeer{out.Peers.Sender, out.Peers.Recipient} {
		if peer.Opens < 0 || peer.Opens > 1 || peer.AnnouncementsSent < 0 || peer.AnnouncementsSent > 1 || peer.Refreshes != 0 || peer.Ends != 0 || peer.UpdatesReceived < 0 || peer.UpdatesReceived > 1024 || peer.EORsReceived < 0 || peer.EORsReceived > peer.UpdatesReceived || len(peer.RIB) > 2 || len(peer.Events) > 128 {
			return out, fail("generated-counter-invalid")
		}
		reconstructed := map[string]generatedRoute{}
		last := uint64(0)
		for _, event := range peer.Events {
			if event.Sequence <= last || event.Sequence > out.Sequence || (event.PrefixID != "a" && event.PrefixID != "b") {
				return out, fail("generated-event-invalid")
			}
			last = event.Sequence
			switch event.Kind {
			case "withdraw":
				_, present := reconstructed[event.PrefixID]
				if event.Route != nil || event.Removed != present {
					return out, fail("generated-event-invalid")
				}
				delete(reconstructed, event.PrefixID)
			case "announce":
				if event.Route == nil || event.Route.PrefixID != event.PrefixID || event.Removed {
					return out, fail("generated-event-invalid")
				}
				if e := generatedAttributeError(*event.Route); e != nil {
					return out, e
				}
				reconstructed[event.PrefixID] = *event.Route
			default:
				return out, fail("generated-event-invalid")
			}
		}
		if len(reconstructed) != len(peer.RIB) {
			return out, fail("generated-rib-invalid")
		}
		seen := map[string]bool{}
		for _, route := range peer.RIB {
			expected, ok := reconstructed[route.PrefixID]
			if !ok || seen[route.PrefixID] || !reflect.DeepEqual(expected, route) {
				return out, fail("generated-rib-invalid")
			}
			seen[route.PrefixID] = true
		}
	}
	if len(out.Peers.Sender.RIB) != 0 || len(out.Peers.Sender.Events) != 0 || out.Peers.Recipient.AnnouncementsSent != 0 {
		return out, fail("generated-source-reflection")
	}
	return out, nil
}
func generatedRouteValid(route generatedRoute) bool {
	return (route.PrefixID == "a" || route.PrefixID == "b") && generatedAttributeError(route) == nil
}
func generatedCommon(o generatedObservation, announced int) bool {
	return o.Peers.Sender.Established && o.Peers.Recipient.Established && o.Peers.Sender.Opens == 1 && o.Peers.Recipient.Opens == 1 && o.Peers.Sender.AnnouncementsSent == announced && o.Peers.Sender.Refreshes == 0 && o.Peers.Recipient.Refreshes == 0 && o.Peers.Sender.Ends == 0 && o.Peers.Recipient.Ends == 0
}
func generatedTransition(o generatedObservation, after uint64, withdrawn, announced string) bool {
	gone, added := false, false
	for _, event := range o.Peers.Recipient.Events {
		if event.Sequence <= after {
			continue
		}
		if event.Kind == "withdraw" && event.PrefixID == withdrawn && event.Removed {
			gone = true
		}
		if event.Kind == "announce" && event.PrefixID == announced && event.Route != nil && generatedRouteValid(*event.Route) {
			added = true
		}
	}
	return gone && added
}
func generatedRIB(o generatedObservation, prefix string) bool {
	return len(o.Peers.Recipient.RIB) == 1 && o.Peers.Recipient.RIB[0].PrefixID == prefix && generatedRouteValid(o.Peers.Recipient.RIB[0])
}
func generatedBaseline(o generatedObservation) bool {
	if !generatedCommon(o, 1) || !generatedRIB(o, "a") {
		return false
	}
	for _, event := range o.Peers.Recipient.Events {
		if event.Kind == "announce" && event.PrefixID == "b" {
			return false
		}
	}
	return true
}
func generatedWait(ctx context.Context, condition func(generatedObservation) bool) (generatedObservation, error) {
	wait, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var since time.Time
	var latest generatedObservation
	for {
		var e error
		latest, e = generatedRead(wait, "snapshot")
		if e != nil {
			return latest, e
		}
		if condition(latest) {
			if since.IsZero() {
				since = time.Now()
			}
			if time.Since(since) >= time.Second {
				return latest, nil
			}
		} else {
			since = time.Time{}
		}
		select {
		case <-wait.Done():
			return latest, fail("generated-convergence-timeout")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Explicit forwarding avoids automatically opting into future Runtime batch or
// cleanup interfaces. This case tests the reviewed ordinary supervisor path.
type generatedRuntime struct {
	runtime     *Runtime
	lastRequest control.ReadRequest
	checkpoint  control.Observation
	bootstrap   bool
	beforeTake  func(context.Context) error
}

func (g *generatedRuntime) Binding() control.Binding            { return g.runtime.Binding() }
func (g *generatedRuntime) ResetIdle(ctx context.Context) error { return g.runtime.ResetIdle(ctx) }
func (g *generatedRuntime) BindPlan(p routeros.Plan, c routeros.Credentials) error {
	return g.runtime.BindPlan(p, c)
}
func (g *generatedRuntime) Observe(ctx context.Context, q control.ReadRequest) (control.Observation, error) {
	o, e := g.runtime.Observe(ctx, q)
	if e == nil {
		g.lastRequest = q
	}
	return o, e
}
func (g *generatedRuntime) Apply(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	return g.runtime.Apply(ctx, m)
}
func (g *generatedRuntime) Owner(ctx context.Context) (control.Owner, error) {
	return g.runtime.Owner(ctx)
}
func (g *generatedRuntime) Confirm(ctx context.Context) (control.Owner, error) {
	return g.runtime.Confirm(ctx)
}
func (g *generatedRuntime) Cancel(ctx context.Context) (control.Owner, error) {
	return g.runtime.Cancel(ctx)
}
func (g *generatedRuntime) CloseOwner() error { return g.runtime.CloseOwner() }
func (g *generatedRuntime) Take(ctx context.Context, deadline time.Time) (control.Owner, error) {
	if g.beforeTake != nil {
		if e := g.beforeTake(ctx); e != nil {
			return control.Owner{}, e
		}
	}
	checkpoint, e := g.runtime.Observe(ctx, g.lastRequest)
	if e != nil {
		return control.Owner{}, e
	}
	if !checkpoint.Owner.Known || checkpoint.Owner.Active || !checkpoint.Owner.HistoryKnown {
		return control.Owner{}, fail("generated-checkpoint-owner")
	}
	count := 0
	owner := routeros.OwnerName(g.runtime.target.RouterID)
	for _, row := range checkpoint.Inventory.Connections {
		if row["instance"] == owner {
			count++
			if (row["disabled"] == "true") != g.bootstrap {
				return control.Owner{}, fail("generated-checkpoint-connection")
			}
		}
	}
	if count != 2 || len(checkpoint.Credentials) != 2 {
		return control.Owner{}, fail("generated-checkpoint-connection")
	}
	for _, credential := range checkpoint.Credentials {
		if !credential.Matches(nil) {
			return control.Owner{}, fail("generated-checkpoint-credential")
		}
	}
	g.checkpoint = checkpoint
	return g.runtime.Take(ctx, deadline)
}
func generatedKnown(p routeros.Plan) routeros.KnownState {
	known := routeros.KnownState{Target: p.Target, ActiveGeneration: p.Generation, Generations: []routeros.KnownGeneration{{Target: p.Target, Desired: p.Desired, PolicyChecksum: p.PolicyChecksum}}}
	for _, peer := range p.Desired.Peers {
		known.Peers = append(known.Peers, routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
	}
	return known
}
func generatedFail(t *testing.T, label string, e error) {
	t.Helper()
	code := nativeSupervisorCode(e)
	var attributes *generatedAttributeFailure
	if errors.As(e, &attributes) {
		code = attributes.Code
		t.Logf("generated_bgp_attribute_diagnostic=true generated_bgp_origin_match=%t generated_bgp_path_match=%t generated_bgp_next_hop_match=%t generated_bgp_path_empty=%t generated_bgp_path_one_sequence=%t generated_bgp_path_only_source=%t generated_bgp_path_server_then_source=%t generated_bgp_path_contains_server=%t generated_bgp_path_contains_recipient=%t generated_bgp_next_hop_server=%t generated_bgp_next_hop_recipient=%t generated_bgp_next_hop_gateway=%t generated_bgp_origin_igp=%t generated_bgp_origin_egp=%t generated_bgp_origin_incomplete=%t", attributes.OriginMatch, attributes.PathMatch, attributes.NextHopMatch, attributes.PathEmpty, attributes.PathOneSequence, attributes.PathOnlySource, attributes.PathServerThenSource, attributes.PathContainsServer, attributes.PathContainsRecipient, attributes.NextHopServer, attributes.NextHopRecipient, attributes.NextHopGateway, attributes.OriginIGP, attributes.OriginEGP, attributes.OriginIncomplete)
	}
	if code == "unclassified" {
		code = "generated-unclassified"
	}
	t.Fatalf("%s=false code=%s", label, code)
}

func TestNativeCHRLiveGeneratedTwoPeerIPv4Policy(t *testing.T) {
	if os.Getenv("MOEDOVE_CHR_GENERATED_BGP_TEST") != "1" {
		t.Skip("generated_bgp_fixture_enabled=false")
	}
	cfg := nativeLiveConfig(t)
	generatedReadContext(t)
	cfg.RouterID = "native-generated-ipv4"
	cfg.SessionLifetimeSeconds = 600
	cfg.OperationTimeoutSeconds = 15
	cfg.ObservationTimeoutSeconds = 120
	ctx, cancel := context.WithTimeout(context.Background(), 260*time.Second)
	defer cancel()
	runtime, e := Open(ctx, cfg)
	if e != nil {
		generatedFail(t, "generated_bgp_runtime", e)
	}
	defer runtime.Close()
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_, _ = generatedRead(cleanup, "allow-shutdown")
	}()
	initialWire, e := generatedRead(ctx, "snapshot")
	if e != nil {
		generatedFail(t, "generated_bgp_initial_wire", e)
	}
	if initialWire.Peers.Sender.Opens != 0 || initialWire.Peers.Recipient.Opens != 0 || initialWire.Sequence != 0 {
		t.Fatal("generated_bgp_initial_wire=false")
	}
	target := runtime.Binding().Target
	empty := routeros.KnownState{Target: target}
	initial, e := runtime.Observe(ctx, control.ReadRequest{KnownState: empty, Strict: true})
	if e != nil || initial.Snapshot == nil || !initial.Snapshot.NamespaceEmpty {
		generatedFail(t, "generated_bgp_initial_namespace", e)
	}
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "10.0.2.15"}
	peers := []routing.Peer{{ID: "sender", ASN: 64501, Address: "10.0.2.100", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}, AllowedRoutes: []routing.PrefixOrigin{{Prefix: "203.0.113.0/24", OriginASN: 64501}}}, {ID: "recipient", ASN: 64502, Address: "10.0.2.101", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"198.51.100.0/24"}, AllowedRoutes: []routing.PrefixOrigin{{Prefix: "198.51.100.0/24", OriginASN: 64502}}}}
	primary, alternate := peers[0].AllowedPrefixes[0], peers[1].AllowedPrefixes[0]
	first, e := routeros.BuildPlan(target, router, peers, *initial.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if e != nil {
		generatedFail(t, "generated_bgp_first_plan", e)
	}
	// Test-only capability override in a fixed isolated guest. Production gates
	// remain code-owned false, regardless of this fixture's outcome.
	runtime.profile.PreparationVerified = true
	runtime.profile.ActivationVerified = true
	runtime.profile.ConfirmNativeVerified = true
	runtime.profile.MaxHistoryActions = 80
	driver := &generatedRuntime{runtime: runtime, bootstrap: true}
	dir, e := os.MkdirTemp("/scratch/tmp", "ixp-generated-bgp-journal-")
	if e != nil {
		generatedFail(t, "generated_bgp_journal", e)
	}
	defer os.RemoveAll(dir)
	supervisor, e := control.Open(control.Config{Dir: dir, Target: target, OperationTimeout: 30 * time.Second, PrepareTimeout: 3 * time.Minute, MinConfirmationTimeout: 10 * time.Second, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: time.Second}, driver)
	if e != nil {
		generatedFail(t, "generated_bgp_supervisor", e)
	}
	defer supervisor.Close()
	status, e := supervisor.Stage(ctx, control.Request{Plan: first, KnownState: empty, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if e != nil || status.Phase != control.AwaitingConfirmation {
		generatedFail(t, "generated_bgp_bootstrap_stage", e)
	}
	status, e = supervisor.Confirm(ctx, status.TransactionID, first.Checksum)
	if e != nil || status.Phase != control.Confirmed || !status.ConfirmationVerified {
		generatedFail(t, "generated_bgp_bootstrap_confirm", e)
	}
	known := generatedKnown(first)
	verified, e := runtime.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if e != nil || verified.Snapshot == nil || verified.Snapshot.TableFIB {
		generatedFail(t, "generated_bgp_bootstrap_snapshot", e)
	}
	noop, e := routeros.BuildPlan(target, router, peers, *verified.Snapshot, routeros.PlanOptions{})
	if e != nil || len(noop.Operations) != 0 {
		generatedFail(t, "generated_bgp_bootstrap_roundtrip", e)
	}
	_, e = generatedWait(ctx, func(o generatedObservation) bool {
		return generatedCommon(o, 0) && len(o.Peers.Recipient.RIB) == 0 && o.Sequence == 0
	})
	if e != nil {
		generatedFail(t, "generated_bgp_sessions", e)
	}
	if _, e = generatedRead(ctx, "announce-once"); e != nil {
		generatedFail(t, "generated_bgp_announce", e)
	}
	_, e = generatedWait(ctx, generatedBaseline)
	if e != nil {
		generatedFail(t, "generated_bgp_baseline", e)
	}
	if code := nativeRSRIB(ctx, runtime, cfg, primary, alternate); code != "" {
		t.Fatalf("generated_bgp_rib_baseline=false code=%s", code)
	}
	t.Log("generated_bgp_rib_baseline=true")
	t.Log("generated_bgp_baseline_exact=true generated_bgp_source_binding=true generated_bgp_as_path_transparent=true generated_bgp_next_hop_transparent=true generated_bgp_self_echo_absent=true")

	peers[0].AllowedPrefixes = []string{"198.51.100.0/24"}
	peers[0].AllowedRoutes = []routing.PrefixOrigin{{Prefix: "198.51.100.0/24", OriginASN: 64501}}
	base, e := runtime.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if e != nil || base.Snapshot == nil {
		generatedFail(t, "generated_bgp_update_base", e)
	}
	second, e := routeros.BuildPlan(target, router, peers, *base.Snapshot, routeros.PlanOptions{})
	if e != nil {
		generatedFail(t, "generated_bgp_update_plan", e)
	}
	activation := 0
	for _, operation := range second.Operations {
		if operation.Phase == routeros.Activate {
			activation++
			if operation.Kind != routeros.SetEntry {
				t.Fatal("generated_bgp_policy_only=false")
			}
		} else if operation.Phase == routeros.Prepare && operation.Kind != routeros.AddRule {
			t.Fatal("generated_bgp_policy_only=false")
		}
	}
	if activation != 3 {
		t.Fatal("generated_bgp_policy_only=false")
	}
	driver.bootstrap = false
	var switchSequence uint64
	driver.beforeTake = func(ctx context.Context) error {
		o, e := generatedRead(ctx, "snapshot")
		if e != nil {
			return e
		}
		if !generatedBaseline(o) {
			return fail("generated-prepare-changed-wire")
		}
		switchSequence = o.Sequence
		return nil
	}
	status, e = supervisor.Stage(ctx, control.Request{Plan: second, KnownState: known, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if e != nil || status.Phase != control.AwaitingConfirmation {
		generatedFail(t, "generated_bgp_update_stage", e)
	}
	switched, e := generatedWait(ctx, func(o generatedObservation) bool {
		return generatedCommon(o, 1) && generatedRIB(o, "b") && generatedTransition(o, switchSequence, "a", "b")
	})
	if e != nil {
		generatedFail(t, "generated_bgp_cached_switch", e)
	}
	if code := nativeRSRIB(ctx, runtime, cfg, alternate, primary); code != "" {
		t.Fatalf("generated_bgp_rib_switch=false code=%s", code)
	}
	t.Log("generated_bgp_rib_switch=true")
	t.Log("generated_bgp_policy_only=true generated_bgp_cached_withdrawal=true generated_bgp_switch_path_transparent=true generated_bgp_switch_next_hop_transparent=true")
	cancelSequence := switched.Sequence
	checkpointDigest, e := stateDigest(driver.checkpoint.Inventory, target)
	if e != nil {
		generatedFail(t, "generated_bgp_checkpoint", e)
	}
	status, e = supervisor.Cancel(ctx, status.TransactionID)
	until := time.Now().Add(30 * time.Second)
	for status.Phase != control.RolledBack && time.Now().Before(until) && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		status, e = supervisor.Recover(ctx)
	}
	if e != nil || status.Phase != control.RolledBack || !status.RollbackVerified {
		generatedFail(t, "generated_bgp_cancel", e)
	}
	preparedKnown := known
	preparedKnown.Generations = append(append([]routeros.KnownGeneration(nil), known.Generations...), routeros.KnownGeneration{Target: target, Desired: second.Desired, PolicyChecksum: second.PolicyChecksum})
	rolled, e := runtime.Observe(ctx, control.ReadRequest{KnownState: preparedKnown, Strict: true})
	if e != nil || rolled.Snapshot == nil || rolled.Snapshot.ActiveGeneration != first.Generation {
		generatedFail(t, "generated_bgp_cancel_snapshot", e)
	}
	rolledDigest, e := stateDigest(rolled.Inventory, target)
	if e != nil || rolledDigest != checkpointDigest || !sameCredentials(driver.checkpoint.Credentials, rolled.Credentials) {
		generatedFail(t, "generated_bgp_cancel_checkpoint", e)
	}
	_, e = generatedWait(ctx, func(o generatedObservation) bool {
		return generatedCommon(o, 1) && generatedRIB(o, "a") && generatedTransition(o, cancelSequence, "b", "a")
	})
	if e != nil {
		generatedFail(t, "generated_bgp_cached_cancel", e)
	}
	if code := nativeRSRIB(ctx, runtime, cfg, primary, alternate); code != "" {
		t.Fatalf("generated_bgp_rib_cancel=false code=%s", code)
	}
	t.Log("generated_bgp_rib_cancel=true")
	t.Log("generated_bgp_ipv4_fixture_verified=true generated_bgp_cached_cancel_restored=true generated_bgp_sender_announced_once=true generated_bgp_sessions_single_open=true generated_bgp_no_refresh=true generated_bgp_manual_routing_changes=false generated_bgp_ipv6_verified=false generated_bgp_packet_forwarding_verified=false")
}

func TestGeneratedWireChecksActualAttributesAndWithdrawal(t *testing.T) {
	good := generatedRoute{PrefixID: "a", Origin: 0, Path: []generatedPathSegment{{Type: 2, ASNs: []uint32{64501}}}, NextHop: "10.0.2.100"}
	if !generatedRouteValid(good) {
		t.Fatal("fixed actual route rejected")
	}
	bad := good
	bad.NextHop = "10.0.2.15"
	if generatedRouteValid(bad) {
		t.Fatal("rewritten next-hop accepted")
	}
	var mismatch *generatedAttributeFailure
	if !errors.As(generatedAttributeError(bad), &mismatch) || mismatch.Code != "generated-next-hop-changed" || !mismatch.NextHopServer || !mismatch.PathMatch || !mismatch.OriginMatch {
		t.Fatal("next-hop diagnostic lost its exact category")
	}
	bad = good
	bad.Path = []generatedPathSegment{{Type: 2, ASNs: []uint32{64500, 64501}}}
	if generatedRouteValid(bad) {
		t.Fatal("RS ASN prepending accepted")
	}
	if !errors.As(generatedAttributeError(bad), &mismatch) || mismatch.Code != "generated-as-path-changed" || !mismatch.PathServerThenSource || !mismatch.PathContainsServer || mismatch.PathContainsRecipient || !mismatch.NextHopMatch {
		t.Fatal("AS_PATH diagnostic lost its exact category")
	}
	bad = good
	bad.Origin = 2
	if !errors.As(generatedAttributeError(bad), &mismatch) || mismatch.Code != "generated-origin-changed" || !mismatch.OriginIncomplete || !mismatch.PathMatch || !mismatch.NextHopMatch {
		t.Fatal("ORIGIN diagnostic lost its exact category")
	}
	var observation generatedObservation
	next := good
	next.PrefixID = "b"
	observation.Peers.Recipient.Events = []generatedEvent{{Sequence: 2, Kind: "withdraw", PrefixID: "a", Removed: false}, {Sequence: 3, Kind: "announce", PrefixID: "b", Route: &next}}
	if generatedTransition(observation, 1, "a", "b") {
		t.Fatal("withdrawal of never-observed route accepted")
	}
	observation.Peers.Recipient.Events[0].Removed = true
	if !generatedTransition(observation, 1, "a", "b") {
		t.Fatal("actual withdrawal/announcement lost")
	}
}
