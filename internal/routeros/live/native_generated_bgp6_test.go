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

const generated6Socket = "/scratch/bgp-generated6.sock"

type generated6Context struct {
	SchemaVersion     int    `json:"schemaVersion"`
	Fixture           string `json:"fixture"`
	ControlSocket     string `json:"controlSocket"`
	RouterAddress     string `json:"routerAddress"`
	RouterASN         uint32 `json:"routerASN"`
	SenderAddress     string `json:"senderAddress"`
	SenderASN         uint32 `json:"senderASN"`
	RecipientAddress  string `json:"recipientAddress"`
	RecipientASN      uint32 `json:"recipientASN"`
	PeerConnectTarget string `json:"peerConnectTarget"`
}
type generated6PathSegment struct {
	Type int      `json:"type"`
	ASNs []uint32 `json:"asns"`
}
type generated6Route struct {
	PrefixID         string                  `json:"prefixId"`
	Origin           int                     `json:"origin"`
	Path             []generated6PathSegment `json:"path"`
	NextHop          string                  `json:"nextHop"`
	LinkLocalNextHop string                  `json:"linkLocalNextHop,omitempty"`
}
type generated6Event struct {
	Sequence uint64           `json:"sequence"`
	Kind     string           `json:"kind"`
	PrefixID string           `json:"prefixId"`
	Removed  bool             `json:"removed"`
	Route    *generated6Route `json:"route,omitempty"`
}
type generated6Peer struct {
	IPv6Negotiated     bool              `json:"ipv6Negotiated"`
	OpensSent          int               `json:"opensSent"`
	ConnectAttempts    int               `json:"connectAttempts"`
	IPv6SocketVerified bool              `json:"ipv6SocketVerified"`
	Established        bool              `json:"established"`
	Opens              int               `json:"opens"`
	AnnouncementsSent  int               `json:"announcementsSent"`
	Refreshes          int               `json:"refreshes"`
	Ends               int               `json:"ends"`
	UpdatesReceived    int               `json:"updatesReceived"`
	EORsReceived       int               `json:"eorsReceived"`
	RIB                []generated6Route `json:"rib"`
	Events             []generated6Event `json:"events"`
}
type generated6Observation struct {
	NativeSourcesVerified bool   `json:"nativeSourcesVerified"`
	SchemaVersion         int    `json:"schemaVersion"`
	OK                    bool   `json:"ok"`
	FailureCode           string `json:"failureCode"`
	Sequence              uint64 `json:"sequence"`
	Peers                 struct {
		Sender    generated6Peer `json:"sender"`
		Recipient generated6Peer `json:"recipient"`
	} `json:"peers"`
}

// This error retains only fixed comparisons, never raw route/event attributes.
type generated6AttributeFailure struct {
	LinkLocalAbsent                                                  bool
	Code                                                             string
	OriginMatch, PathMatch, NextHopMatch                             bool
	PathEmpty, PathOneSequence, PathOnlySource, PathServerThenSource bool
	PathContainsServer, PathContainsRecipient                        bool
	NextHopServer, NextHopRecipient, NextHopGateway                  bool
	OriginIGP, OriginEGP, OriginIncomplete                           bool
}

func (*generated6AttributeFailure) Error() string { return "generated6 BGP attribute mismatch" }
func generated6AttributeError(route generated6Route) error {
	e := &generated6AttributeFailure{LinkLocalAbsent: route.LinkLocalNextHop == "", OriginMatch: route.Origin == 0, NextHopMatch: route.NextHop == "2001:db8:ffff::100", PathOneSequence: len(route.Path) == 1 && route.Path[0].Type == 2, NextHopServer: route.NextHop == "2001:db8:ffff::15", NextHopRecipient: route.NextHop == "2001:db8:ffff::101", NextHopGateway: route.NextHop == "2001:db8:ffff::1", OriginIGP: route.Origin == 0, OriginEGP: route.Origin == 1, OriginIncomplete: route.Origin == 2}
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
		e.Code = "generated6-as-path-changed"
	case !e.NextHopMatch:
		e.Code = "generated6-next-hop-changed"
	case !e.LinkLocalAbsent:
		e.Code = "generated6-link-local-next-hop-changed"
	case !e.OriginMatch:
		e.Code = "generated6-origin-changed"
	default:
		return nil
	}
	return e
}

func generated6ReadContext(t *testing.T) {
	t.Helper()
	raw, e := readFixedFile("/scratch/bgp-generated6-context.json", 4096, true)
	if e != nil {
		t.Fatal("generated6_bgp_context_valid=false")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var got generated6Context
	if decoder.Decode(&got) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("generated6_bgp_context_valid=false")
	}
	wanted := generated6Context{SchemaVersion: 1, Fixture: "generated-two-peer-ipv6", ControlSocket: generated6Socket, RouterAddress: "2001:db8:ffff::15", RouterASN: 64500, SenderAddress: "2001:db8:ffff::100", SenderASN: 64501, RecipientAddress: "2001:db8:ffff::101", RecipientASN: 64502, PeerConnectTarget: "[::1]:13790"}
	if got != wanted {
		t.Fatal("generated6_bgp_context_valid=false")
	}
}
func generated6Read(ctx context.Context, action string) (generated6Observation, error) {
	var out generated6Observation
	if action != "snapshot" && action != "announce-once" && action != "allow-shutdown" && action != "connect-once" && action != "verify-native-sources" {
		return out, fail("generated6-control-action")
	}
	info, e := os.Lstat(generated6Socket)
	if e != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return out, fail("generated6-control-socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return out, fail("generated6-control-socket")
	}
	timeout := 3 * time.Second
	if action == "verify-native-sources" {
		timeout = 25 * time.Second
	}
	call, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, e := (&net.Dialer{}).DialContext(call, "unix", generated6Socket)
	if e != nil {
		return out, fail("generated6-control-io")
	}
	defer conn.Close()
	deadline, _ := call.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return out, fail("generated6-control-io")
	}
	request, _ := json.Marshal(struct {
		Action string `json:"action"`
	}{action})
	request = append(request, '\n')
	if n, e := conn.Write(request); e != nil || n != len(request) {
		return out, fail("generated6-control-io")
	}
	raw, e := io.ReadAll(io.LimitReader(conn, 65537))
	if e != nil || len(raw) == 0 || len(raw) > 65536 {
		return out, fail("generated6-control-response")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF {
		return out, fail("generated6-control-response")
	}
	if out.SchemaVersion != 1 || !out.OK || out.FailureCode != "" || out.Sequence > 256 {
		return out, fail("generated6-peer-failed")
	}
	for _, peer := range []generated6Peer{out.Peers.Sender, out.Peers.Recipient} {
		if peer.OpensSent < 0 || peer.OpensSent > 1 || peer.ConnectAttempts < 0 || peer.ConnectAttempts > 1 || peer.Opens > 0 && (!peer.IPv6Negotiated || peer.OpensSent != 1 || peer.ConnectAttempts != 1 || !peer.IPv6SocketVerified) {
			return out, fail("generated6-ipv6-transport-unverified")
		}
		if peer.Opens < 0 || peer.Opens > 1 || peer.AnnouncementsSent < 0 || peer.AnnouncementsSent > 1 || peer.Refreshes != 0 || peer.Ends != 0 || peer.UpdatesReceived < 0 || peer.UpdatesReceived > 1024 || peer.EORsReceived < 0 || peer.EORsReceived > peer.UpdatesReceived || len(peer.RIB) > 2 || len(peer.Events) > 128 {
			return out, fail("generated6-counter-invalid")
		}
		reconstructed := map[string]generated6Route{}
		last := uint64(0)
		for _, event := range peer.Events {
			if event.Sequence <= last || event.Sequence > out.Sequence || (event.PrefixID != "a" && event.PrefixID != "b") {
				return out, fail("generated6-event-invalid")
			}
			last = event.Sequence
			switch event.Kind {
			case "withdraw":
				_, present := reconstructed[event.PrefixID]
				if event.Route != nil || event.Removed != present {
					return out, fail("generated6-event-invalid")
				}
				delete(reconstructed, event.PrefixID)
			case "announce":
				if event.Route == nil || event.Route.PrefixID != event.PrefixID || event.Removed {
					return out, fail("generated6-event-invalid")
				}
				if e := generated6AttributeError(*event.Route); e != nil {
					return out, e
				}
				reconstructed[event.PrefixID] = *event.Route
			default:
				return out, fail("generated6-event-invalid")
			}
		}
		if len(reconstructed) != len(peer.RIB) {
			return out, fail("generated6-rib-invalid")
		}
		seen := map[string]bool{}
		for _, route := range peer.RIB {
			expected, ok := reconstructed[route.PrefixID]
			if !ok || seen[route.PrefixID] || !reflect.DeepEqual(expected, route) {
				return out, fail("generated6-rib-invalid")
			}
			seen[route.PrefixID] = true
		}
	}
	if len(out.Peers.Sender.RIB) != 0 || len(out.Peers.Sender.Events) != 0 || out.Peers.Recipient.AnnouncementsSent != 0 {
		return out, fail("generated6-source-reflection")
	}
	return out, nil
}
func generated6RouteValid(route generated6Route) bool {
	return (route.PrefixID == "a" || route.PrefixID == "b") && generated6AttributeError(route) == nil
}
func generated6Common(o generated6Observation, announced int) bool {
	return o.Peers.Sender.Established && o.Peers.Recipient.Established && o.Peers.Sender.IPv6Negotiated && o.Peers.Recipient.IPv6Negotiated && o.Peers.Sender.IPv6SocketVerified && o.Peers.Recipient.IPv6SocketVerified && o.Peers.Sender.ConnectAttempts == 1 && o.Peers.Recipient.ConnectAttempts == 1 && o.Peers.Sender.OpensSent == 1 && o.Peers.Recipient.OpensSent == 1 && o.Peers.Sender.Opens == 1 && o.Peers.Recipient.Opens == 1 && o.Peers.Sender.AnnouncementsSent == announced && o.Peers.Sender.Refreshes == 0 && o.Peers.Recipient.Refreshes == 0 && o.Peers.Sender.Ends == 0 && o.Peers.Recipient.Ends == 0
}
func generated6Transition(o generated6Observation, after uint64, withdrawn, announced string) bool {
	gone, added := false, false
	for _, event := range o.Peers.Recipient.Events {
		if event.Sequence <= after {
			continue
		}
		if event.Kind == "withdraw" && event.PrefixID == withdrawn && event.Removed {
			gone = true
		}
		if event.Kind == "announce" && event.PrefixID == announced && event.Route != nil && generated6RouteValid(*event.Route) {
			added = true
		}
	}
	return gone && added
}
func generated6RIB(o generated6Observation, prefix string) bool {
	return len(o.Peers.Recipient.RIB) == 1 && o.Peers.Recipient.RIB[0].PrefixID == prefix && generated6RouteValid(o.Peers.Recipient.RIB[0])
}
func generated6Baseline(o generated6Observation) bool {
	if !o.NativeSourcesVerified || !generated6Common(o, 1) || !generated6RIB(o, "a") {
		return false
	}
	for _, event := range o.Peers.Recipient.Events {
		if event.Kind == "announce" && event.PrefixID == "b" {
			return false
		}
	}
	return true
}
func generated6Wait(ctx context.Context, condition func(generated6Observation) bool) (generated6Observation, error) {
	wait, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var since time.Time
	var latest generated6Observation
	for {
		var e error
		latest, e = generated6Read(wait, "snapshot")
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
			return latest, fail("generated6-convergence-timeout")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Explicit forwarding avoids automatically opting into future Runtime batch or
// cleanup interfaces. This case tests the reviewed ordinary supervisor path.
type generated6Runtime struct {
	runtime     *Runtime
	lastRequest control.ReadRequest
	checkpoint  control.Observation
	bootstrap   bool
	beforeTake  func(context.Context) error
}

func (g *generated6Runtime) Binding() control.Binding            { return g.runtime.Binding() }
func (g *generated6Runtime) ResetIdle(ctx context.Context) error { return g.runtime.ResetIdle(ctx) }
func (g *generated6Runtime) BindPlan(p routeros.Plan, c routeros.Credentials) error {
	return g.runtime.BindPlan(p, c)
}
func (g *generated6Runtime) Observe(ctx context.Context, q control.ReadRequest) (control.Observation, error) {
	o, e := g.runtime.Observe(ctx, q)
	if e == nil {
		g.lastRequest = q
	}
	return o, e
}
func (g *generated6Runtime) Apply(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	return g.runtime.Apply(ctx, m)
}
func (g *generated6Runtime) Owner(ctx context.Context) (control.Owner, error) {
	return g.runtime.Owner(ctx)
}
func (g *generated6Runtime) Confirm(ctx context.Context) (control.Owner, error) {
	return g.runtime.Confirm(ctx)
}
func (g *generated6Runtime) Cancel(ctx context.Context) (control.Owner, error) {
	return g.runtime.Cancel(ctx)
}
func (g *generated6Runtime) CloseOwner() error { return g.runtime.CloseOwner() }
func (g *generated6Runtime) Take(ctx context.Context, deadline time.Time) (control.Owner, error) {
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
		return control.Owner{}, fail("generated6-checkpoint-owner")
	}
	count := 0
	owner := routeros.OwnerName(g.runtime.target.RouterID)
	for _, row := range checkpoint.Inventory.Connections {
		if row["instance"] == owner {
			count++
			if (row["disabled"] == "true") != g.bootstrap {
				return control.Owner{}, fail("generated6-checkpoint-connection")
			}
		}
	}
	if count != 2 || len(checkpoint.Credentials) != 2 {
		return control.Owner{}, fail("generated6-checkpoint-connection")
	}
	for _, credential := range checkpoint.Credentials {
		if !credential.Matches(nil) {
			return control.Owner{}, fail("generated6-checkpoint-credential")
		}
	}
	g.checkpoint = checkpoint
	return g.runtime.Take(ctx, deadline)
}
func generated6Known(p routeros.Plan) routeros.KnownState {
	known := routeros.KnownState{Target: p.Target, ActiveGeneration: p.Generation, Generations: []routeros.KnownGeneration{{Target: p.Target, Desired: p.Desired, PolicyChecksum: p.PolicyChecksum}}}
	for _, peer := range p.Desired.Peers {
		known.Peers = append(known.Peers, routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
	}
	return known
}
func generated6Fail(t *testing.T, label string, e error) {
	t.Helper()
	code := nativeSupervisorCode(e)
	var attributes *generated6AttributeFailure
	if errors.As(e, &attributes) {
		code = attributes.Code
		t.Logf("generated6_bgp_link_local_absent=%t", attributes.LinkLocalAbsent)
		t.Logf("generated6_bgp_attribute_diagnostic=true generated6_bgp_origin_match=%t generated6_bgp_path_match=%t generated6_bgp_next_hop_match=%t generated6_bgp_path_empty=%t generated6_bgp_path_one_sequence=%t generated6_bgp_path_only_source=%t generated6_bgp_path_server_then_source=%t generated6_bgp_path_contains_server=%t generated6_bgp_path_contains_recipient=%t generated6_bgp_next_hop_server=%t generated6_bgp_next_hop_recipient=%t generated6_bgp_next_hop_gateway=%t generated6_bgp_origin_igp=%t generated6_bgp_origin_egp=%t generated6_bgp_origin_incomplete=%t", attributes.OriginMatch, attributes.PathMatch, attributes.NextHopMatch, attributes.PathEmpty, attributes.PathOneSequence, attributes.PathOnlySource, attributes.PathServerThenSource, attributes.PathContainsServer, attributes.PathContainsRecipient, attributes.NextHopServer, attributes.NextHopRecipient, attributes.NextHopGateway, attributes.OriginIGP, attributes.OriginEGP, attributes.OriginIncomplete)
	}
	if code == "unclassified" {
		code = "generated6-unclassified"
	}
	t.Fatalf("%s=false code=%s", label, code)
}

func TestNativeCHRLiveGeneratedIPv6TwoPeerPolicy(t *testing.T) {
	if os.Getenv("MOEDOVE_CHR_GENERATED_BGP6_TEST") != "1" {
		t.Skip("generated6_bgp_fixture_enabled=false")
	}
	cfg := nativeLiveConfig(t)
	generated6ReadContext(t)
	cfg.RouterID = "native-generated-ipv6"
	cfg.SessionLifetimeSeconds = 600
	cfg.OperationTimeoutSeconds = 15
	cfg.ObservationTimeoutSeconds = 120
	ctx, cancel := context.WithTimeout(context.Background(), 260*time.Second)
	defer cancel()
	runtime, e := Open(ctx, cfg)
	if e != nil {
		generated6Fail(t, "generated6_bgp_runtime", e)
	}
	defer runtime.Close()
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_, _ = generated6Read(cleanup, "allow-shutdown")
	}()
	initialWire, e := generated6Read(ctx, "snapshot")
	if e != nil {
		generated6Fail(t, "generated6_bgp_initial_wire", e)
	}
	if initialWire.Peers.Sender.Opens != 0 || initialWire.Peers.Recipient.Opens != 0 || initialWire.Peers.Sender.OpensSent != 0 || initialWire.Peers.Recipient.OpensSent != 0 || initialWire.Peers.Sender.ConnectAttempts != 0 || initialWire.Peers.Recipient.ConnectAttempts != 0 || initialWire.NativeSourcesVerified || initialWire.Sequence != 0 {
		t.Fatal("generated6_bgp_initial_wire=false")
	}
	target := runtime.Binding().Target
	empty := routeros.KnownState{Target: target}
	initial, e := runtime.Observe(ctx, control.ReadRequest{KnownState: empty, Strict: true})
	if e != nil || initial.Snapshot == nil || !initial.Snapshot.NamespaceEmpty {
		generated6Fail(t, "generated6_bgp_initial_namespace", e)
	}
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv6: "2001:db8:ffff::15"}
	peers := []routing.Peer{{ID: "sender", ASN: 64501, Address: "2001:db8:ffff::100", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"2001:db8:100::/48"}, AllowedRoutes: []routing.PrefixOrigin{{Prefix: "2001:db8:100::/48", OriginASN: 64501}}}, {ID: "recipient", ASN: 64502, Address: "2001:db8:ffff::101", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"2001:db8:200::/48"}, AllowedRoutes: []routing.PrefixOrigin{{Prefix: "2001:db8:200::/48", OriginASN: 64502}}}}
	primary, alternate := peers[0].AllowedPrefixes[0], peers[1].AllowedPrefixes[0]
	first, e := routeros.BuildPlan(target, router, peers, *initial.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if e != nil {
		generated6Fail(t, "generated6_bgp_first_plan", e)
	}
	// Test-only capability override in a fixed isolated guest. Production gates
	// remain code-owned false, regardless of this fixture's outcome.
	runtime.profile.PreparationVerified = true
	runtime.profile.ActivationVerified = true
	runtime.profile.ConfirmNativeVerified = true
	runtime.profile.MaxHistoryActions = 80
	driver := &generated6Runtime{runtime: runtime, bootstrap: true}
	dir, e := os.MkdirTemp("/scratch/tmp", "ixp-generated6-bgp-journal-")
	if e != nil {
		generated6Fail(t, "generated6_bgp_journal", e)
	}
	defer os.RemoveAll(dir)
	supervisor, e := control.Open(control.Config{Dir: dir, Target: target, OperationTimeout: 30 * time.Second, PrepareTimeout: 3 * time.Minute, MinConfirmationTimeout: 10 * time.Second, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: time.Second}, driver)
	if e != nil {
		generated6Fail(t, "generated6_bgp_supervisor", e)
	}
	defer supervisor.Close()
	status, e := supervisor.Stage(ctx, control.Request{Plan: first, KnownState: empty, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if e != nil || status.Phase != control.AwaitingConfirmation {
		generated6Fail(t, "generated6_bgp_bootstrap_stage", e)
	}
	status, e = supervisor.Confirm(ctx, status.TransactionID, first.Checksum)
	if e != nil || status.Phase != control.Confirmed || !status.ConfirmationVerified {
		generated6Fail(t, "generated6_bgp_bootstrap_confirm", e)
	}
	known := generated6Known(first)
	verified, e := runtime.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if e != nil || verified.Snapshot == nil || verified.Snapshot.TableFIB {
		generated6Fail(t, "generated6_bgp_bootstrap_snapshot", e)
	}
	noop, e := routeros.BuildPlan(target, router, peers, *verified.Snapshot, routeros.PlanOptions{})
	if e != nil || len(noop.Operations) != 0 {
		generated6Fail(t, "generated6_bgp_bootstrap_roundtrip", e)
	}
	if _, e = generated6Read(ctx, "connect-once"); e != nil {
		generated6Fail(t, "generated6_bgp_connect_once", e)
	}
	_, e = generated6Wait(ctx, func(o generated6Observation) bool {
		return generated6Common(o, 0) && len(o.Peers.Recipient.RIB) == 0 && o.Sequence == 0
	})
	if e != nil {
		generated6Fail(t, "generated6_bgp_sessions", e)
	}
	nativeSources, e := generated6Read(ctx, "verify-native-sources")
	if e != nil || !nativeSources.NativeSourcesVerified || !generated6Common(nativeSources, 0) {
		generated6Fail(t, "generated6_bgp_native_sources_verified", e)
	}
	t.Log("generated6_bgp_native_sources_verified=true generated6_bgp_ipv6_socket_verified=true generated6_bgp_mp_ipv6_negotiated=true generated6_bgp_open_sent_once=true")
	if _, e = generated6Read(ctx, "announce-once"); e != nil {
		generated6Fail(t, "generated6_bgp_announce", e)
	}
	_, e = generated6Wait(ctx, generated6Baseline)
	if e != nil {
		generated6Fail(t, "generated6_bgp_baseline", e)
	}
	if code := nativeRSRIB(ctx, runtime, cfg, primary, alternate); code != "" {
		t.Fatalf("generated6_bgp_rib_baseline=false code=%s", code)
	}
	t.Log("generated6_bgp_rib_baseline=true")
	t.Log("generated6_bgp_baseline_exact=true generated6_bgp_source_binding=true generated6_bgp_as_path_transparent=true generated6_bgp_next_hop_transparent=true generated6_bgp_self_echo_absent=true")

	peers[0].AllowedPrefixes = []string{"2001:db8:200::/48"}
	peers[0].AllowedRoutes = []routing.PrefixOrigin{{Prefix: "2001:db8:200::/48", OriginASN: 64501}}
	base, e := runtime.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if e != nil || base.Snapshot == nil {
		generated6Fail(t, "generated6_bgp_update_base", e)
	}
	second, e := routeros.BuildPlan(target, router, peers, *base.Snapshot, routeros.PlanOptions{})
	if e != nil {
		generated6Fail(t, "generated6_bgp_update_plan", e)
	}
	activation := 0
	for _, operation := range second.Operations {
		if operation.Phase == routeros.Activate {
			activation++
			if operation.Kind != routeros.SetEntry {
				t.Fatal("generated6_bgp_policy_only=false")
			}
		} else if operation.Phase == routeros.Prepare && operation.Kind != routeros.AddRule {
			t.Fatal("generated6_bgp_policy_only=false")
		}
	}
	if activation != 3 {
		t.Fatal("generated6_bgp_policy_only=false")
	}
	driver.bootstrap = false
	var switchSequence uint64
	driver.beforeTake = func(ctx context.Context) error {
		o, e := generated6Read(ctx, "snapshot")
		if e != nil {
			return e
		}
		if !generated6Baseline(o) {
			return fail("generated6-prepare-changed-wire")
		}
		switchSequence = o.Sequence
		return nil
	}
	status, e = supervisor.Stage(ctx, control.Request{Plan: second, KnownState: known, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if e != nil || status.Phase != control.AwaitingConfirmation {
		generated6Fail(t, "generated6_bgp_update_stage", e)
	}
	switched, e := generated6Wait(ctx, func(o generated6Observation) bool {
		return o.NativeSourcesVerified && generated6Common(o, 1) && generated6RIB(o, "b") && generated6Transition(o, switchSequence, "a", "b")
	})
	if e != nil {
		generated6Fail(t, "generated6_bgp_cached_switch", e)
	}
	if code := nativeRSRIB(ctx, runtime, cfg, alternate, primary); code != "" {
		t.Fatalf("generated6_bgp_rib_switch=false code=%s", code)
	}
	t.Log("generated6_bgp_rib_switch=true")
	t.Log("generated6_bgp_policy_only=true generated6_bgp_cached_withdrawal=true generated6_bgp_switch_path_transparent=true generated6_bgp_switch_next_hop_transparent=true")
	cancelSequence := switched.Sequence
	checkpointDigest, e := stateDigest(driver.checkpoint.Inventory, target)
	if e != nil {
		generated6Fail(t, "generated6_bgp_checkpoint", e)
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
		generated6Fail(t, "generated6_bgp_cancel", e)
	}
	preparedKnown := known
	preparedKnown.Generations = append(append([]routeros.KnownGeneration(nil), known.Generations...), routeros.KnownGeneration{Target: target, Desired: second.Desired, PolicyChecksum: second.PolicyChecksum})
	rolled, e := runtime.Observe(ctx, control.ReadRequest{KnownState: preparedKnown, Strict: true})
	if e != nil || rolled.Snapshot == nil || rolled.Snapshot.ActiveGeneration != first.Generation {
		generated6Fail(t, "generated6_bgp_cancel_snapshot", e)
	}
	rolledDigest, e := stateDigest(rolled.Inventory, target)
	if e != nil || rolledDigest != checkpointDigest || !sameCredentials(driver.checkpoint.Credentials, rolled.Credentials) {
		generated6Fail(t, "generated6_bgp_cancel_checkpoint", e)
	}
	_, e = generated6Wait(ctx, func(o generated6Observation) bool {
		return o.NativeSourcesVerified && generated6Common(o, 1) && generated6RIB(o, "a") && generated6Transition(o, cancelSequence, "b", "a")
	})
	if e != nil {
		generated6Fail(t, "generated6_bgp_cached_cancel", e)
	}
	if code := nativeRSRIB(ctx, runtime, cfg, primary, alternate); code != "" {
		t.Fatalf("generated6_bgp_rib_cancel=false code=%s", code)
	}
	t.Log("generated6_bgp_rib_cancel=true")
	t.Log("generated6_bgp_ipv6_fixture_verified=true generated6_bgp_cached_cancel_restored=true generated6_bgp_sender_announced_once=true generated6_bgp_sessions_single_open=true generated6_bgp_no_refresh=true generated6_bgp_manual_policy_changes=false generated6_bgp_ipv6_verified=true generated6_bgp_packet_forwarding_verified=false")
}

func TestGenerated6WireChecksActualAttributesAndWithdrawal(t *testing.T) {
	good := generated6Route{PrefixID: "a", Origin: 0, Path: []generated6PathSegment{{Type: 2, ASNs: []uint32{64501}}}, NextHop: "2001:db8:ffff::100"}
	if !generated6RouteValid(good) {
		t.Fatal("fixed actual route rejected")
	}
	bad := good
	bad.NextHop = "2001:db8:ffff::15"
	if generated6RouteValid(bad) {
		t.Fatal("rewritten next-hop accepted")
	}
	var mismatch *generated6AttributeFailure
	if !errors.As(generated6AttributeError(bad), &mismatch) || mismatch.Code != "generated6-next-hop-changed" || !mismatch.NextHopServer || !mismatch.PathMatch || !mismatch.OriginMatch {
		t.Fatal("next-hop diagnostic lost its exact category")
	}
	bad = good
	bad.Path = []generated6PathSegment{{Type: 2, ASNs: []uint32{64500, 64501}}}
	if generated6RouteValid(bad) {
		t.Fatal("RS ASN prepending accepted")
	}
	if !errors.As(generated6AttributeError(bad), &mismatch) || mismatch.Code != "generated6-as-path-changed" || !mismatch.PathServerThenSource || !mismatch.PathContainsServer || mismatch.PathContainsRecipient || !mismatch.NextHopMatch {
		t.Fatal("AS_PATH diagnostic lost its exact category")
	}
	bad = good
	bad.Origin = 2
	if !errors.As(generated6AttributeError(bad), &mismatch) || mismatch.Code != "generated6-origin-changed" || !mismatch.OriginIncomplete || !mismatch.PathMatch || !mismatch.NextHopMatch {
		t.Fatal("ORIGIN diagnostic lost its exact category")
	}
	bad = good
	bad.LinkLocalNextHop = "fe80::100"
	if !errors.As(generated6AttributeError(bad), &mismatch) || mismatch.Code != "generated6-link-local-next-hop-changed" || mismatch.LinkLocalAbsent || !mismatch.NextHopMatch {
		t.Fatal("an added link-local half was hidden behind the unchanged global next-hop")
	}
	bad = good
	bad.Path = []generated6PathSegment{{Type: 1, ASNs: []uint32{64501}}}
	if generated6RouteValid(bad) || !errors.As(generated6AttributeError(bad), &mismatch) || mismatch.Code != "generated6-as-path-changed" || mismatch.PathOneSequence {
		t.Fatal("AS_SET was accepted as an unchanged AS_SEQUENCE")
	}
	var observation generated6Observation
	next := good
	next.PrefixID = "b"
	observation.Peers.Recipient.Events = []generated6Event{{Sequence: 2, Kind: "withdraw", PrefixID: "a", Removed: false}, {Sequence: 3, Kind: "announce", PrefixID: "b", Route: &next}}
	if generated6Transition(observation, 1, "a", "b") {
		t.Fatal("withdrawal of never-observed route accepted")
	}
	observation.Peers.Recipient.Events[0].Removed = true
	if !generated6Transition(observation, 1, "a", "b") {
		t.Fatal("actual withdrawal/announcement lost")
	}
}

func TestGenerated6BaselineRequiresNativeSourcesAndSingleIPv6Connection(t *testing.T) {
	goodPeer := generated6Peer{IPv6Negotiated: true, OpensSent: 1, ConnectAttempts: 1, IPv6SocketVerified: true, Established: true, Opens: 1}
	good := generated6Observation{NativeSourcesVerified: true}
	good.Peers.Sender, good.Peers.Recipient = goodPeer, goodPeer
	good.Peers.Sender.AnnouncementsSent = 1
	good.Peers.Recipient.RIB = []generated6Route{{PrefixID: "a", Origin: 0, Path: []generated6PathSegment{{Type: 2, ASNs: []uint32{64501}}}, NextHop: "2001:db8:ffff::100"}}
	if !generated6Baseline(good) {
		t.Fatal("complete baseline evidence rejected")
	}
	cases := map[string]func(*generated6Observation){
		"host-socket-only": func(o *generated6Observation) { o.NativeSourcesVerified = false },
		"no-mp6":           func(o *generated6Observation) { o.Peers.Sender.IPv6Negotiated = false },
		"no-v6-socket":     func(o *generated6Observation) { o.Peers.Recipient.IPv6SocketVerified = false },
		"tcp-retry":        func(o *generated6Observation) { o.Peers.Sender.ConnectAttempts = 2 },
		"open-retry":       func(o *generated6Observation) { o.Peers.Sender.OpensSent = 2 },
		"router-reopen":    func(o *generated6Observation) { o.Peers.Recipient.Opens = 2 },
		"not-established":  func(o *generated6Observation) { o.Peers.Recipient.Established = false },
		"disconnected":     func(o *generated6Observation) { o.Peers.Sender.Ends = 1 },
		"route-refresh":    func(o *generated6Observation) { o.Peers.Recipient.Refreshes = 1 },
		"source-replay":    func(o *generated6Observation) { o.Peers.Sender.AnnouncementsSent = 2 },
		"wrong-source-prefix-seen": func(o *generated6Observation) {
			o.Peers.Recipient.Events = []generated6Event{{Kind: "announce", PrefixID: "b"}}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := good
			change(&candidate)
			if generated6Baseline(candidate) {
				t.Fatal("incomplete or contradictory transport proof accepted")
			}
		})
	}
}
