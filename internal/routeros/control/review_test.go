package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routing"
)

func TestReviewProvidesFreshManifestWithoutExecutingOrSharingState(t *testing.T) {
	r, request := fixtureRuntime(t)
	config := testConfig(t, r)
	s := openTest(t, config, r)
	initial, err := s.Review(context.Background())
	if err != nil || !initial.Snapshot.NamespaceEmpty || initial.KnownState.Target != r.target || initial.ObservedAt.IsZero() || r.applyCount != 0 || r.bindCount != 0 || r.takeCount != 0 {
		t.Fatal("initial read-only review did not prove an empty namespace", err)
	}
	st, err := s.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review(context.Background()); err == nil {
		t.Fatal("pending activation was exported as a planning baseline")
	}
	if _, err = s.Confirm(context.Background(), st.TransactionID, st.PlanChecksum); err != nil {
		t.Fatal(err)
	}
	before := [4]int{r.applyCount, r.takeCount, r.confirmCount, r.bindCount}
	result, err := s.Review(context.Background())
	if err != nil || result.Snapshot.ActiveGeneration != request.Plan.Generation || len(result.KnownState.Generations) != 1 {
		t.Fatal("confirmed state was not independently reviewed", err)
	}
	if after := [4]int{r.applyCount, r.takeCount, r.confirmCount, r.bindCount}; after != before {
		t.Fatal("review made a mutation or rebound the plan")
	}
	result.KnownState.Generations[0].Desired.ASN = 64599
	result.Snapshot.Generations[0].Rules[0].Comment = "tampered-copy"
	if _, err = s.Review(context.Background()); err != nil {
		t.Fatal("returned slices changed the journal's internal proof", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTest(t, config, r)
	if _, err = reopened.Review(context.Background()); err != nil {
		t.Fatal("reopened confirmed journal could not provide read-only planning metadata", err)
	}
}

// Real API-SSL decoding produces the opaque credential evidence used by this
// device model. Tests never manufacture SecretValue or assume known-empty.
func reviewOpaqueCredentials(t *testing.T, values ...string) map[string]api.Credential {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "review-fixture.invalid"}, DNSNames: []string{"review-fixture.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		read := func() ([]string, error) {
			words := []string{}
			for len(words) < 16 {
				var size [1]byte
				if _, e := io.ReadFull(conn, size[:]); e != nil {
					return nil, e
				}
				if size[0] == 0 {
					return words, nil
				}
				if size[0] >= 128 {
					return nil, errors.New("fixture word limit")
				}
				word := make([]byte, int(size[0]))
				if _, e := io.ReadFull(conn, word); e != nil {
					return nil, e
				}
				words = append(words, string(word))
			}
			return nil, errors.New("fixture sentence limit")
		}
		send := func(words ...string) error {
			wire := []byte{}
			for _, word := range words {
				if len(word) == 0 || len(word) >= 128 {
					return errors.New("fixture reply limit")
				}
				wire = append(wire, byte(len(word)))
				wire = append(wire, []byte(word)...)
			}
			wire = append(wire, 0)
			_, e := conn.Write(wire)
			return e
		}
		words, e := read()
		if e != nil || len(words) != 3 || words[0] != "/login" {
			done <- errors.New("fixture login shape")
			return
		}
		if e = send("!done"); e != nil {
			done <- e
			return
		}
		for _, value := range values {
			words, e = read()
			if e != nil {
				done <- e
				return
			}
			if len(words) != 4 || words[0] != "/routing/bgp/connection/get" {
				done <- errors.New("fixture credential request shape")
				return
			}
			tag := ""
			for _, word := range words {
				if strings.HasPrefix(word, ".tag=") {
					tag = word
				}
			}
			if tag == "" {
				done <- errors.New("fixture tag missing")
				return
			}
			if e = send("!done", tag, "=ret="+value); e != nil {
				done <- e
				return
			}
		}
		done <- nil
	}()
	client, err := api.Dial(context.Background(), api.Config{Address: listener.Addr().String(), ServerName: "review-fixture.invalid", Username: "fixture", Password: []byte("fixture-login"), RootCAs: roots, DialTimeout: 3 * time.Second, CommandTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	out := map[string]api.Credential{}
	for _, value := range values {
		credential, e := client.ReadCredential(context.Background(), "*1")
		if e != nil || !credential.Matches([]byte(value)) {
			t.Fatal("real API credential evidence unavailable", e)
		}
		out[value] = credential
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return out
}

type reviewDevice struct {
	*memoryRuntime
	opaque                  map[string]api.Credential
	secrets, undoSecrets    map[string]string
	reads                   int
	wrongSnapshot           bool
	changeSessionAfterFirst bool
	requireAuthority        bool
}

func reviewCopySecrets(values map[string]string) map[string]string {
	out := map[string]string{}
	for id, value := range values {
		out[id] = value
	}
	return out
}
func (r *reviewDevice) Observe(ctx context.Context, q ReadRequest) (Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return Observation{}, ctx.Err()
	}
	r.reads++
	inv := copyInventory(r.inv)
	resolver, err := routeros.NewAPIResolver(inv, r.target, r.evidence())
	if err != nil {
		return Observation{}, err
	}
	credentials := map[string]api.Credential{}
	for _, row := range inv.Connections {
		id := row[".id"]
		if r.requireAuthority {
			checksum, e := CredentialAttributesChecksum(row)
			allowed := false
			for _, grant := range q.CredentialAuthority {
				allowed = allowed || (e == nil && grant.ID == id && grant.Name == row["name"] && grant.AttributesChecksum == checksum)
			}
			if !allowed {
				return Observation{}, errors.New("fresh fixture credential authority missing")
			}
		}
		if value, ok := r.secrets[id]; ok {
			credentials[id] = r.opaque[value]
		}
	}
	o := Observation{Target: r.target, Inventory: inv, Resolver: resolver, Credentials: credentials, Owner: r.owner, ObservedAt: time.Now().UTC(), CrossChannelIdentityVerified: true}
	if q.Strict {
		snapshot, e := routeros.BuildSnapshot(inv, q.KnownState, routeros.CredentialEvidence{Observed: credentials, Expected: q.BaselineCredentials}, r.evidence())
		if e != nil {
			return o, e
		}
		if r.wrongSnapshot {
			snapshot.ASN++
		}
		o.Snapshot = &snapshot
	}
	if r.changeSessionAfterFirst && r.reads == 1 {
		r.owner.SessionID = "another-fresh-session-000000000000"
	}
	return o, nil
}
func (r *reviewDevice) Apply(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	result, err := r.memoryRuntime.Apply(ctx, m)
	if err != nil {
		return result, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if m.Request.Resource == api.BGPConnection {
		if m.Request.Action == api.Add {
			delete(r.secrets, result.ID)
		} else if m.Request.Action == api.Remove {
			delete(r.secrets, m.Request.ID)
		}
	}
	for _, row := range r.inv.Connections {
		if m.Request.Resource == api.BGPConnection && m.Request.Action == api.Add && row[".id"] == result.ID {
			for _, field := range strings.Fields("templates input.filter-nlri input.limit-process-routes-ipv4 input.limit-process-routes-ipv6 output.filter-select output.network output.redistribute comment") {
				if _, ok := row[field]; !ok {
					row[field] = ""
				}
			}
			for _, field := range []string{"dynamic", "multihop", "output.remove-private-as", "output.as-override"} {
				row[field] = "false"
			}
			row["vrf"] = "main"
		}
		if value, ok := row["tcp-md5-key"]; ok {
			r.secrets[row[".id"]] = value
			delete(row, "tcp-md5-key")
		}
	}
	return result, nil
}
func (r *reviewDevice) Take(ctx context.Context, deadline time.Time) (Owner, error) {
	r.mu.Lock()
	r.undoSecrets = reviewCopySecrets(r.secrets)
	r.mu.Unlock()
	return r.memoryRuntime.Take(ctx, deadline)
}
func (r *reviewDevice) Cancel(ctx context.Context) (Owner, error) {
	o, err := r.memoryRuntime.Cancel(ctx)
	r.mu.Lock()
	if err == nil && !r.noUndo {
		r.secrets = reviewCopySecrets(r.undoSecrets)
	}
	r.mu.Unlock()
	return o, err
}
func (r *reviewDevice) CloseOwner() error {
	r.mu.Lock()
	active := r.owner.Active && r.owner.ThisSession && !r.noUndo
	r.mu.Unlock()
	err := r.memoryRuntime.CloseOwner()
	if active {
		r.mu.Lock()
		r.secrets = reviewCopySecrets(r.undoSecrets)
		r.mu.Unlock()
	}
	return err
}
func reviewFreshDevice(r *reviewDevice) *reviewDevice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &reviewDevice{memoryRuntime: &memoryRuntime{target: r.target, inv: copyInventory(r.inv), gates: true, owner: Owner{Known: true, Alive: true, HistoryKnown: true, SessionID: "fresh-read-only-session-000000000000"}, byOperation: map[string]int{}}, opaque: r.opaque, secrets: reviewCopySecrets(r.secrets), requireAuthority: true}
}

func reviewManagedPending(t *testing.T) (*Supervisor, *reviewDevice, Config, routeros.Plan, routing.Router, []routing.Peer) {
	t.Helper()
	base, empty := fixtureRuntime(t)
	r := &reviewDevice{memoryRuntime: base, opaque: reviewOpaqueCredentials(t, "review-original-A", "review-replacement-B", "review-added-C", ""), secrets: map[string]string{}}
	config := testConfig(t, base)
	config.OperationTimeout = 3 * time.Second
	config.PrepareTimeout = 15 * time.Second
	config.MaxConfirmationTimeout = 15 * time.Second
	config.PollInterval = 10 * time.Second
	s, err := Open(config, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	router := routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", LocalIPv6: "2001:db8::1"}
	peers := []routing.Peer{{ID: "old", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}, Password: "review-original-A", AuthenticationRevision: strings.Repeat("A", 22)}}
	first, err := routeros.BuildPlan(r.target, router, peers, empty.Plan.Base, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	oldCredentials := routeros.Credentials{"old": {Revision: peers[0].AuthenticationRevision, Password: peers[0].Password}}
	st, err := s.Stage(context.Background(), Request{Plan: first, KnownState: empty.KnownState, Credentials: oldCredentials, Timeout: 10 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.begin.Baseline.Objects) != 0 || len(s.begin.Baseline.Chains) != 0 {
		t.Fatal("preparation/activation mutated the original empty baseline")
	}
	if _, err = s.Confirm(context.Background(), st.TransactionID, st.PlanChecksum); err != nil {
		t.Fatal(err)
	}
	baseline, err := s.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeUpdate, err := r.Observe(context.Background(), ReadRequest{KnownState: baseline.KnownState, BaselineCredentials: oldCredentials, Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	originalState, err := projectObservation(s.log.key, r.target, beforeUpdate)
	if err != nil {
		t.Fatal(err)
	}
	originalDigest := stateMAC(s.log.key, originalState)
	peers[0].Address = "2001:db8::2"
	peers[0].AllowedPrefixes = []string{"2001:db8:1::/48"}
	peers[0].Password = "review-replacement-B"
	peers[0].AuthenticationRevision = strings.Repeat("B", 22)
	peers = append(peers, routing.Peer{ID: "added", ASN: 64502, Address: "198.51.100.3", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.114.0/24"}, Password: "review-added-C", AuthenticationRevision: strings.Repeat("C", 22)})
	second, err := routeros.BuildPlan(r.target, router, peers, baseline.Snapshot, routeros.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	newCredentials := routeros.Credentials{"old": {Revision: peers[0].AuthenticationRevision, Password: peers[0].Password}, "added": {Revision: peers[1].AuthenticationRevision, Password: peers[1].Password}}
	st, err = s.Stage(context.Background(), Request{Plan: second, KnownState: baseline.KnownState, Credentials: newCredentials, BaselineCredentials: oldCredentials, Timeout: 10 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if stateMAC(s.log.key, s.begin.Baseline) != originalDigest {
		t.Fatal("update changed original baseline objects, credentials or chain order")
	}
	return s, r, config, second, router, peers
}

func reviewManagedRollback(t *testing.T) (*Supervisor, *reviewDevice, Config, routeros.Plan, routing.Router, []routing.Peer) {
	t.Helper()
	s, r, config, second, router, peers := reviewManagedPending(t)
	if st, err := s.Cancel(context.Background(), s.Status().TransactionID); err != nil || st.Phase != RolledBack || !st.RollbackVerified {
		t.Fatal("fixture rollback not verified", st, err)
	}
	if s.rollback == nil || stateMAC(s.log.key, *s.rollback) == stateMAC(s.log.key, s.current) {
		t.Fatal("fixture did not distinguish rollback from activation state")
	}
	return s, r, config, second, router, peers
}

func TestReviewRolledBackManagedReusesPreparedGenerationAndOldAuthentication(t *testing.T) {
	s, r, config, cancelled, router, peers := reviewManagedRollback(t)
	if _, err := s.Review(context.Background()); err == nil {
		t.Fatal("same-process cancelled runtime became a planning authority")
	}
	oldStatus := s.Status()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := reviewFreshDevice(r)
	reopened, err := Open(config, fresh)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	before := reopened.Status()
	result, err := reopened.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.applyCount+fresh.takeCount+fresh.confirmCount+fresh.cancelCount+fresh.bindCount != 0 || !reflect.DeepEqual(before, reopened.Status()) {
		t.Fatal("review changed transaction or device")
	}
	if result.Snapshot.ActiveGeneration != cancelled.Base.ActiveGeneration || result.Snapshot.ASN != cancelled.Base.ASN || result.Snapshot.RouterID != cancelled.Base.RouterID || len(result.Snapshot.Generations) != 2 {
		t.Fatal("rollback identity or retained generation lost")
	}
	seen := map[string]routeros.ConnectionState{}
	for _, conn := range result.Snapshot.Connections {
		seen[conn.Config.PeerID] = conn
	}
	if seen["old"].Config.Family != "ip" || seen["old"].Config.Authentication.Revision != strings.Repeat("A", 22) || seen["old"].Disabled || !seen["added"].Disabled || seen["added"].Config.Authentication.Revision != strings.Repeat("C", 22) {
		t.Fatal("AF/MD5 rollback and newly prepared identity were conflated")
	}
	plan, err := routeros.BuildPlan(fresh.target, router, peers, result.Snapshot, routeros.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Generation != cancelled.Generation {
		t.Fatal("retained generation was not reusable")
	}
	for _, operation := range plan.Operations {
		if operation.Kind == routeros.AddRule {
			t.Fatal("replanning duplicated already prepared rules")
		}
	}
	if _, err := reopened.Confirm(context.Background(), oldStatus.TransactionID, oldStatus.PlanChecksum); err == nil {
		t.Fatal("old confirmation regained authority")
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"review-original-A", "review-replacement-B", "review-added-C"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("review returned a credential value")
		}
	}
	result.KnownState.Generations[0].Desired.ASN = 64599
	result.Snapshot.Generations[0].Rules[0].Comment = "mutated-copy"
	if _, err := reopened.Review(context.Background()); err != nil {
		t.Fatal("review result shared internal state", err)
	}
}

func TestReviewRolledBackRejectsFreshnessDriftCredentialsAndDiscardInProgress(t *testing.T) {
	for _, mode := range []string{"same-session", "closed-owner", "unknown-history", "nonzero-history", "foreign-owner", "external-drift", "credential-drift", "unknown-credential", "snapshot-identity", "session-changed", "discard-pending", "discard-already-started"} {
		t.Run(mode, func(t *testing.T) {
			s, r, c, _, _, _ := reviewManagedRollback(t)
			oldID := s.Status().SessionID
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			fresh := reviewFreshDevice(r)
			switch mode {
			case "same-session":
				fresh.owner.SessionID = oldID
			case "closed-owner":
				fresh.owner.Alive = false
			case "unknown-history":
				fresh.owner.HistoryKnown = false
			case "nonzero-history":
				fresh.owner.HistoryActions = 1
			case "foreign-owner":
				fresh.owner.Active = true
			case "external-drift":
				fresh.inv.Tables = append(fresh.inv.Tables, api.Record{".id": "*F000", "name": "external", "fib": "true", "disabled": "false", "dynamic": "false", "invalid": "false", "comment": "changed"})
			case "credential-drift":
				fresh.secrets["*1"] = "review-replacement-B"
			case "unknown-credential":
				fresh.opaque = map[string]api.Credential{} // actual getter unavailable
			case "snapshot-identity":
				fresh.wrongSnapshot = true
			case "session-changed":
				fresh.changeSessionAfterFirst = true
			}
			reopened, err := Open(c, fresh)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if mode == "discard-pending" {
				reopened.status.PreparedDiscard.Phase = PreparedDiscardPending
			}
			if mode == "discard-already-started" {
				reopened.preparedDiscard.Checksum = strings.Repeat("f", 64)
			}
			if _, err := reopened.Review(context.Background()); err == nil {
				t.Fatal("unverified rollback review accepted", mode)
			}
			if fresh.applyCount+fresh.takeCount+fresh.confirmCount+fresh.cancelCount+fresh.bindCount != 0 {
				t.Fatal("rejected review changed device")
			}
			if (mode == "same-session" || mode == "closed-owner" || mode == "unknown-history" || mode == "nonzero-history" || mode == "foreign-owner" || mode == "discard-pending" || mode == "discard-already-started") && fresh.reads != 0 {
				t.Fatal("freshness/discard failure still read credentials")
			}
		})
	}
}

// Model the separately controlled discard's completed physical state. This
// helper performs no controller operation and supplies no proof by itself:
// Review must independently compare the actual resulting inventory to Baseline.
func reviewDiscardedInventory(s *Supervisor, inv api.Inventory) api.Inventory {
	out := copyInventory(inv)
	for _, resource := range []api.Resource{api.RoutingTable, api.BGPInstance, api.BGPConnection, api.FilterRule} {
		rows := rowsFor(&out, resource)
		kept := []api.Record{}
		for _, row := range *rows {
			key := objectKey(resource, row[".id"])
			_, prepared := s.rollback.Objects[key]
			_, baseline := s.begin.Baseline.Objects[key]
			if !prepared || baseline {
				kept = append(kept, row)
			}
		}
		*rows = kept
	}
	return out
}

func TestReviewCompletedPreparedDiscardReturnsExactOriginalManifest(t *testing.T) {
	for _, mode := range []string{"complete", "not-actually-discarded", "wrong-base-checksum", "pending-complete"} {
		t.Run(mode, func(t *testing.T) {
			s, r, config, cancelled, _, _ := reviewManagedRollback(t)
			originalKnown := s.begin.Known
			actual := reviewDiscardedInventory(s, r.inv)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			fresh := reviewFreshDevice(r)
			if mode != "not-actually-discarded" {
				fresh.inv = actual
			}
			reopened, err := Open(config, fresh)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.status.PreparedDiscard.Phase = PreparedDiscardComplete
			reopened.current = cloneState(reopened.begin.Baseline)
			if mode == "wrong-base-checksum" {
				reopened.begin.Plan.BaseChecksum = strings.Repeat("0", 64)
			}
			if mode == "pending-complete" {
				reopened.preparedDiscard.Pending = &cleanupAttempt{}
			}
			result, err := reopened.Review(context.Background())
			if mode != "complete" {
				if err == nil {
					t.Fatal("discard flag substituted for exact baseline proof", mode)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(result.KnownState, originalKnown) || len(result.KnownState.Peers) != 1 || len(result.Snapshot.Connections) != 1 || result.Snapshot.ActiveGeneration != cancelled.Base.ActiveGeneration {
					t.Fatal("discarded preparation leaked desired peers/generations into baseline review")
				}
				raw, _ := json.Marshal(result.Snapshot)
				if routing.Checksum(string(raw)) != cancelled.BaseChecksum {
					t.Fatal("returned baseline snapshot checksum changed")
				}
			}
			if fresh.applyCount+fresh.takeCount+fresh.confirmCount+fresh.cancelCount+fresh.bindCount != 0 {
				t.Fatal("discard review mutated device")
			}
		})
	}
}

func TestReviewBootstrapRollbackRequiresVerifiedDiscardBeforeEmptyBaseline(t *testing.T) {
	r, request := fixtureRuntime(t)
	config := testConfig(t, r)
	s := openTest(t, config, r)
	st, err := s.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if st, err = s.Cancel(context.Background(), st.TransactionID); err != nil || st.Phase != RolledBack {
		t.Fatal(st, err)
	}
	prepared := copyInventory(r.inv)
	empty := reviewDiscardedInventory(s, prepared)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := &memoryRuntime{target: r.target, inv: prepared, gates: true, owner: Owner{Known: true, Alive: true, HistoryKnown: true, SessionID: "fresh-bootstrap-review-session-0000"}, byOperation: map[string]int{}}
	reopened := openTest(t, config, fresh)
	if _, err := reopened.Review(context.Background()); err == nil {
		t.Fatal("bootstrap reject roots were promoted to a strict planning snapshot")
	}
	fresh.mu.Lock()
	fresh.inv = empty
	fresh.mu.Unlock()
	reopened.mu.Lock()
	reopened.status.PreparedDiscard.Phase = PreparedDiscardComplete
	reopened.current = cloneState(reopened.begin.Baseline)
	reopened.mu.Unlock()
	result, err := reopened.Review(context.Background())
	if err != nil || !result.Snapshot.NamespaceEmpty || result.Snapshot.Managed || len(result.KnownState.Generations) != 0 || len(result.KnownState.Peers) != 0 {
		t.Fatal("completed discard did not return exact empty baseline", err)
	}
	if _, err := reopened.Confirm(context.Background(), st.TransactionID, st.PlanChecksum); err == nil {
		t.Fatal("discard review revived old confirmation")
	}
	if fresh.applyCount+fresh.takeCount+fresh.confirmCount+fresh.cancelCount+fresh.bindCount != 0 {
		t.Fatal("empty review changed device")
	}
}

func TestReviewRefusesDriftUnfinishedCleanupAndBusyTransactions(t *testing.T) {
	r, request := fixtureRuntime(t)
	s := openTest(t, testConfig(t, r), r)
	s.mu.Lock()
	start := time.Now()
	_, err := s.Review(context.Background())
	s.mu.Unlock()
	if err == nil || time.Since(start) > 100*time.Millisecond {
		t.Fatal("review blocked behind a transaction")
	}
	st, err := s.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Confirm(context.Background(), st.TransactionID, st.PlanChecksum); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.status.Cleanup.Phase = CleanupPending
	s.mu.Unlock()
	if _, err = s.Review(context.Background()); err == nil {
		t.Fatal("partial cleanup became a strict planning snapshot")
	}
	s.mu.Lock()
	s.status.Cleanup.Phase = CleanupComplete
	s.mu.Unlock()
	r.mu.Lock()
	r.inv.FilterRules[0]["rule"] = "reject;"
	r.mu.Unlock()
	if _, err = s.Review(context.Background()); err == nil {
		t.Fatal("changed device state inherited an old receipt")
	}
}
