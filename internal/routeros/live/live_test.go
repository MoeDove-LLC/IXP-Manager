package live

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
	"github.com/moedove/ixp-manager/internal/routing"
)

func syntheticKey() ([]byte, string) {
	kind := []byte("ssh-ed25519")
	raw := make([]byte, 4+len(kind)+4+32)
	binary.BigEndian.PutUint32(raw, uint32(len(kind)))
	copy(raw[4:], kind)
	binary.BigEndian.PutUint32(raw[4+len(kind):], 32)
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}
func fixedConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture.invalid"}, DNSNames: []string{"fixture.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	wire, pin := syntheticKey()
	files := map[string][]byte{"ca.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "known_hosts": []byte("[127.0.0.1]:2222 ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire) + "\n"), "identity": []byte("fixture-not-real-private-key"), "password": []byte("fixture-private-password")}
	for name, data := range files {
		if e = os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return Config{SchemaVersion: 1, RouterID: "router-a", Version: "7.24.6", DeviceID: "CHR1", IdentitySource: routeros.LicenseSystemID, API: APIConfig{Address: "127.0.0.1:8729", ServerName: "fixture.invalid", Username: "admin", CAFile: filepath.Join(dir, "ca.pem"), PasswordFile: filepath.Join(dir, "password"), SPKISHA256: strings.Repeat("a", 64)}, SSH: SSHConfig{Host: "127.0.0.1", Port: 2222, Username: "admin", IdentityFile: filepath.Join(dir, "identity"), KnownHostsFile: filepath.Join(dir, "known_hosts"), HostKeySHA256: pin}}
}
func TestFixedFileTrustAndPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if e := os.WriteFile(path, []byte("private-fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := readFixedFile(path, 128, true); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := readFixedFile(path, 128, true); e == nil {
		t.Fatal("public secret accepted")
	}
	if _, e := readFixedFile(path, 128, false); e != nil {
		t.Fatal(e)
	}
	_ = os.Chmod(path, 0600)
	link := filepath.Join(dir, "hardlink")
	if e := os.Link(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e := readFixedFile(path, 128, true); e == nil {
		t.Fatal("hardlinked secret accepted")
	}
	_ = os.Remove(link)
	symbolic := filepath.Join(dir, "alias")
	if e := os.Symlink(path, symbolic); e != nil {
		t.Fatal(e)
	}
	if _, e := readFixedFile(symbolic, 128, true); e == nil {
		t.Fatal("leaf symlink accepted")
	}
	parent := filepath.Join(t.TempDir(), "alias-parent")
	if e := os.Symlink(dir, parent); e != nil {
		t.Fatal(e)
	}
	if _, e := readFixedFile(filepath.Join(parent, "secret"), 128, true); e == nil {
		t.Fatal("ancestor symlink accepted")
	}
	if _, e := readFixedFile(path, 2, true); e == nil {
		t.Fatal("oversized file accepted")
	}
}
func TestKnownHostPinIsOneExactTarget(t *testing.T) {
	wire, pin := syntheticKey()
	body := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire)
	line := "[127.0.0.1]:2222 " + body + "\n"
	got, e := canonicalKnownHost([]byte("unrelated.invalid "+body+"\n"+line), "127.0.0.1", 2222, pin)
	if e != nil || string(got) != line {
		t.Fatal("canonical target was not isolated")
	}
	for name, raw := range map[string]string{"duplicate": line + line, "wrong target": "[127.0.0.2]:2222 " + body, "wildcard": "* " + body, "hashed": "|1|abc|def " + body, "authority": "@cert-authority [127.0.0.1]:2222 " + body, "multiple names": "[127.0.0.1]:2222,other " + body, "wrong algorithm": "[127.0.0.1]:2222 ssh-rsa " + base64.StdEncoding.EncodeToString(wire)} {
		t.Run(name, func(t *testing.T) {
			if _, e := canonicalKnownHost([]byte(raw), "127.0.0.1", 2222, pin); e == nil {
				t.Fatal("ambiguous host trust accepted")
			}
		})
	}
	if _, e := canonicalKnownHost([]byte(line), "127.0.0.1", 2222, strings.Repeat("b", 64)); e == nil {
		t.Fatal("wrong pin accepted")
	}
}
func TestTargetBindsActualPinsAndConfigCannotGrantNative(t *testing.T) {
	cfg := fixedConfig(t)
	first, e := Target(cfg)
	if e != nil {
		t.Fatal(e)
	}
	changed := cfg
	changed.API.Username = "other"
	next, e := Target(changed)
	if e != nil || next.TransportIdentitySHA256 == first.TransportIdentitySHA256 {
		t.Fatal("API user not bound")
	}
	changed = cfg
	changed.SSH.Username = "other"
	next, e = Target(changed)
	if e != nil || next.TransportIdentitySHA256 == first.TransportIdentitySHA256 {
		t.Fatal("SSH user not bound")
	}
	changed = cfg
	changed.IdentitySource = routeros.RouterBoardSerial
	next, e = Target(changed)
	if e != nil || next.TransportIdentitySHA256 == first.TransportIdentitySHA256 {
		t.Fatal("stable identity source not bound")
	}
	if e = os.WriteFile(cfg.API.PasswordFile, []byte("new-private-value"), 0600); e != nil {
		t.Fatal(e)
	}
	next, e = Target(cfg)
	if e != nil || next != first {
		t.Fatal("transport digest leaks mutable credential")
	}
	type plain Config
	raw, e := json.Marshal(plain(cfg))
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadConfig(path); e != nil {
		t.Fatal(e)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"preparationVerified":true}`)...)
	_ = os.WriteFile(path, raw, 0600)
	if _, e = LoadConfig(path); e == nil {
		t.Fatal("JSON granted native capability")
	}
	printed := fmt.Sprintf("%+v", cfg)
	jsonCfg, _ := json.Marshal(cfg)
	if strings.Contains(printed, cfg.API.PasswordFile) || strings.Contains(string(jsonCfg), cfg.API.Username) {
		t.Fatal("private configuration leaked")
	}
	p := CapabilityProfile()
	if p.PreparationVerified || p.ActivationVerified || p.MaxHistoryActions != 0 || !p.SSHOwnerNativeVerified || !p.ConfirmNativeVerified {
		t.Fatal("profile overclaimed or lost isolated native facts")
	}
}

type fakeAPI struct {
	mu              sync.Mutex
	inv             api.Inventory
	calls           int
	getters         int
	credentialCalls int
	writes          int
	change          func(int, *api.Inventory)
	properties      map[string]api.PropertyValue
	getterError     bool
	closed          chan struct{}
	block           bool
	writeError      error
	started         chan struct{}
}

func cloneInventory(inv api.Inventory) api.Inventory {
	raw, _ := json.Marshal(inv)
	var out api.Inventory
	_ = json.Unmarshal(raw, &out)
	return out
}
func (a *fakeAPI) Inventory(ctx context.Context) (api.Inventory, error) {
	if a.block {
		if a.started != nil {
			close(a.started)
		}
		select {
		case <-a.closed:
			return api.Inventory{}, errors.New("closed")
		case <-ctx.Done():
			return api.Inventory{}, ctx.Err()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	inv := cloneInventory(a.inv)
	if a.change != nil {
		a.change(a.calls, &inv)
	}
	return inv, nil
}
func (a *fakeAPI) ReadProperty(_ context.Context, r api.Resource, id, field string) (api.PropertyValue, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.getters++
	if a.getterError {
		return api.PropertyValue{}, errors.New("fixture-private-remote-error")
	}
	v, ok := a.properties[string(r)+"/"+id+"/"+field]
	if !ok && r == api.BGPConnection && (field == "as" || field == "output.default-prepend") {
		for _, row := range a.inv.Connections {
			if strings.EqualFold(row[".id"], id) {
				if value, present := row[field]; present {
					return api.PropertyValue{Known: true, Value: value}, nil
				}
			}
		}
	}
	if !ok {
		return api.PropertyValue{}, errors.New("missing fixture getter")
	}
	return v, nil
}
func (a *fakeAPI) ReadCredential(context.Context, string) (api.Credential, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.credentialCalls++
	return api.Credential{}, nil
}
func (a *fakeAPI) Apply(context.Context, api.Mutation) (api.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writes++
	if a.writeError != nil {
		return api.Result{}, a.writeError
	}
	return api.Result{Acknowledged: true, ID: "*99"}, nil
}

func TestCompiledMutationGateAndUncertainNeverRetry(t *testing.T) {
	for _, mode := range []string{"different attributes", "wrong operation", "uncertain response", "verified receipt"} {
		t.Run(mode, func(t *testing.T) {
			r, a, _, req := observerFixture()
			defer r.Close()
			observation, err := r.Observe(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := routeros.BuildPlan(r.target, routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}, []routing.Peer{{ID: "peer-a", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}}}, *observation.Snapshot, routeros.PlanOptions{Bootstrap: true})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.BindPlan(plan, nil); err != nil {
				t.Fatal(err)
			}
			// Unit-only injection exercises the driver; the exported profile and
			// configuration have no mechanism to grant this capability.
			r.profile.PreparationVerified = true
			bridge, err := routeros.NewAPIBridge(plan, nil)
			if err != nil {
				t.Fatal(err)
			}
			mutation, err := bridge.Mutation(bridge.Operations()[0].ID, observation.Resolver)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "different attributes":
				mutation.Request.Attributes[0].Value = "foreign-table"
			case "wrong operation":
				mutation.OperationID = "foreign-operation"
			case "uncertain response":
				a.writeError = &api.Error{Code: api.CodeTransport, Uncertain: true}
			}
			result, err := r.Apply(context.Background(), mutation)
			switch mode {
			case "different attributes", "wrong operation":
				if err == nil || a.writes != 0 {
					t.Fatal("uncompiled mutation reached API")
				}
			case "uncertain response":
				var failure *Failure
				if !errors.As(err, &failure) || !failure.Uncertain || a.writes != 1 {
					t.Fatal("uncertain receipt lost")
				}
				a.writeError = nil
				if _, err = r.Apply(context.Background(), mutation); err == nil || a.writes != 1 {
					t.Fatal("uncertain write retried")
				}
			case "verified receipt":
				if err != nil || !result.Acknowledged || a.writes != 1 {
					t.Fatal("exact compiled write did not reach fixed API")
				}
				if _, err = r.Apply(context.Background(), mutation); err == nil || a.writes != 1 {
					t.Fatal("successful operation duplicated")
				}
			}
		})
	}
}
func (a *fakeAPI) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed != nil {
		select {
		case <-a.closed:
		default:
			close(a.closed)
		}
	}
	return nil
}

type fakeTerminal struct {
	mu           sync.Mutex
	evidence     terminal.Evidence
	version      string
	fingerprint  string
	history      int
	historyKnown bool
	queries      int
}

func (t *fakeTerminal) Snapshot() terminal.Evidence {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.evidence
}
func (t *fakeTerminal) Synchronize(context.Context) (terminal.Evidence, error) {
	return t.Snapshot(), nil
}
func (t *fakeTerminal) OwnerEvidence(context.Context) (terminal.Evidence, error) {
	return t.Snapshot(), nil
}
func (t *fakeTerminal) Query(_ context.Context, q terminal.Query) (terminal.Result, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queries++
	if t.evidence.Closed {
		return terminal.Result{}, errors.New("closed")
	}
	r := terminal.Result{Query: q, Evidence: t.evidence}
	switch q {
	case terminal.Version:
		r.Version = t.version
	case terminal.LicenseSystemID:
		r.IdentifierFingerprint = t.fingerprint
	case terminal.FloatingUndoCount:
		r.HistoryKnown = t.historyKnown
		r.HistoryActions = t.history
	}
	return r, nil
}
func (t *fakeTerminal) EnterSafeMode(context.Context) (terminal.Evidence, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.evidence.Owner = terminal.OwnerThisSession
	t.evidence.Mode = terminal.SafeOwned
	return t.evidence, nil
}
func (t *fakeTerminal) ConfirmSafeMode(context.Context) (terminal.Confirmation, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.evidence.Owner = terminal.OwnerNone
	t.evidence.Mode = terminal.Released
	return terminal.Confirmation{Released: true, Evidence: t.evidence}, nil
}
func (t *fakeTerminal) CancelSafeMode(context.Context) (terminal.Cancellation, error) {
	_ = t.Close()
	return terminal.Cancellation{SessionEnded: true}, nil
}
func (t *fakeTerminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.evidence.Closed = true
	return nil
}
func observerFixture() (*Runtime, *fakeAPI, *fakeTerminal, control.ReadRequest) {
	target := routeros.Target{RouterID: "router-a", Version: "7.24.6", DeviceID: "CHR1", TransportIdentitySHA256: strings.Repeat("a", 64)}
	inv := api.Inventory{Resources: []api.Record{{"version": "7.24.6 (long-term)"}}, Licenses: []api.Record{{"system-id": "CHR1"}}, Capabilities: map[api.Resource]api.Capability{}, SafeMode: &api.SafeModeState{}}
	for _, resource := range []api.Resource{api.SystemResource, api.SystemLicense, api.RoutingTable, api.BGPInstance, api.BGPConnection, api.FilterRule} {
		inv.Capabilities[resource] = api.Supported
	}
	a := &fakeAPI{inv: inv, closed: make(chan struct{}), properties: map[string]api.PropertyValue{}}
	hash := sha256.Sum256([]byte("CHR1"))
	term := &fakeTerminal{evidence: terminal.Evidence{Mode: terminal.Ready, Owner: terminal.OwnerNone, OwnerQueryAvailable: true}, version: "7.24.6", fingerprint: hex.EncodeToString(hash[:]), historyKnown: true}
	cfg := Config{IdentitySource: routeros.LicenseSystemID, ObservationTimeoutSeconds: 2, OperationTimeoutSeconds: 1, SessionLifetimeSeconds: 120, MaxObjects: 100, MaxGetterCalls: 100}
	return newRuntime(cfg, target, a, term), a, term, control.ReadRequest{KnownState: routeros.KnownState{Target: target}, Strict: true}
}
func TestObserveRealEvidenceNormalizationAndStableRead(t *testing.T) {
	r, a, _, request := observerFixture()
	defer r.Close()
	a.inv.Tables = []api.Record{{".id": "*1", "name": "main", "disabled": "false", "invalid": "false", "comment": ""}}
	a.properties["routing-table/*1/fib"] = api.PropertyValue{}
	a.properties["routing-table/*1/dynamic"] = api.PropertyValue{}
	observed, e := r.Observe(context.Background(), request)
	if e != nil {
		t.Fatal(e)
	}
	if !observed.Snapshot.NamespaceEmpty || !observed.CrossChannelIdentityVerified || !observed.Owner.HistoryKnown || observed.Inventory.Tables[0]["fib"] != "false" || a.getters != 4 || a.calls != 2 {
		t.Fatal("actual getter/stable read missing")
	}
	if _, ok := a.inv.Tables[0]["fib"]; ok {
		t.Fatal("read input mutated")
	}
	first := r.Status()
	if len(first.Digest) != 64 || !first.StrictSnapshotVerified {
		t.Fatal("status missing evidence")
	}
	if e = r.CloseOwner(); e != nil {
		t.Fatal(e)
	}
	observed, e = r.Observe(context.Background(), request)
	if e != nil || observed.Owner.Alive || !observed.Owner.Known || observed.Owner.ThisSession {
		t.Fatal("same API recovery readback lost or owner fabricated")
	}
}
func TestObserveRejectsCrossChannelAndTornSnapshots(t *testing.T) {
	for _, name := range []string{"wrong SSH identity", "wrong API identity", "wrong version", "getter trap", "presence conflict", "configuration drift", "getter limit"} {
		t.Run(name, func(t *testing.T) {
			r, a, term, req := observerFixture()
			defer r.Close()
			a.inv.Tables = []api.Record{{".id": "*1", "name": "main", "fib": "true", "disabled": "false", "dynamic": "false", "invalid": "false", "comment": ""}}
			switch name {
			case "wrong SSH identity":
				term.fingerprint = strings.Repeat("b", 64)
			case "wrong API identity":
				a.inv.Licenses[0]["system-id"] = "Other1"
			case "wrong version":
				term.version = "7.24.7"
			case "getter trap":
				delete(a.inv.Tables[0], "fib")
				a.getterError = true
			case "presence conflict":
				a.inv.Tables[0]["fib"] = ""
				a.properties["routing-table/*1/fib"] = api.PropertyValue{Known: true, Value: "false"}
			case "configuration drift":
				a.change = func(n int, v *api.Inventory) {
					if n > 1 {
						v.Tables[0]["comment"] = "changed"
					}
				}
			case "getter limit":
				r.config.MaxGetterCalls = 1
				delete(a.inv.Tables[0], "fib")
				delete(a.inv.Tables[0], "dynamic")
				a.properties["routing-table/*1/fib"] = api.PropertyValue{}
				a.properties["routing-table/*1/dynamic"] = api.PropertyValue{}
			}
			if _, e := r.Observe(context.Background(), req); e == nil {
				t.Fatal("unproved observation accepted")
			}
			if r.Status().CrossChannelIdentityVerified {
				t.Fatal("failed observation retained current verified flag")
			}
			if a.credentialCalls != 0 || a.writes != 0 {
				t.Fatal("failure caused sensitive read or mutation")
			}
		})
	}
}
func TestPartialNamespaceDoesNotAuthorizeCredentialRead(t *testing.T) {
	r, a, _, req := observerFixture()
	defer r.Close()
	owner := routeros.OwnerName(r.target.RouterID)
	name, _ := routeros.ManagedConnectionName(r.target.RouterID, "peer-a")
	req.KnownState.Peers = []routeros.KnownPeer{{ID: "peer-a"}}
	row := api.Record{".id": "*7", "name": name}
	for _, field := range observedFields[api.BGPConnection] {
		row[field] = ""
	}
	for _, field := range []string{"disabled", "dynamic", "connect", "listen", "multihop", "output.remove-private-as", "output.as-override"} {
		row[field] = "false"
	}
	row["instance"] = owner
	row["routing-table"] = owner
	row["local.role"] = "ebgp-rs"
	a.inv.Connections = []api.Record{row}
	if _, e := r.Observe(context.Background(), req); e == nil {
		t.Fatal("strict unowned namespace accepted")
	}
	if a.credentialCalls != 0 {
		t.Fatal("foreign same-name password read before ownership verified")
	}
	req.Strict = false
	observed, e := r.Observe(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	if observed.Snapshot != nil || len(observed.Credentials) != 0 || a.credentialCalls != 0 {
		t.Fatal("partial namespace promoted password authority")
	}
}
func TestNativeGateAndCloseInterrupt(t *testing.T) {
	r, a, _, _ := observerFixture()
	if _, e := r.Apply(context.Background(), routeros.APIMutation{Phase: routeros.Prepare}); e == nil || a.writes != 0 {
		t.Fatal("unverified mutation reached API")
	}
	if _, e := r.Take(context.Background(), time.Now().Add(time.Minute)); e == nil {
		t.Fatal("unverified activation took owner")
	}
	_ = r.Close()
	r, a, _, req := observerFixture()
	a.block = true
	a.started = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, e := r.Observe(context.Background(), req); done <- e }()
	<-a.started
	if e := r.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("closed observation succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt I/O")
	}
	if _, e := r.Observe(context.Background(), req); e == nil {
		t.Fatal("closed runtime reused")
	}
}

func TestResetIdleIsNewAuthorityNotOwnerRecovery(t *testing.T) {
	for _, mode := range []string{"idle", "confirmed with cleanup remaining", "taken", "poisoned", "pending plan", "API owner active", "unknown API owner", "new SSH wrong identity"} {
		t.Run(mode, func(t *testing.T) {
			r, a, term, _ := observerFixture()
			defer r.Close()
			oldID := r.sessionID
			calls := 0
			r.dialSSH = func(context.Context) (terminalChannel, error) {
				calls++
				fingerprint := term.fingerprint
				if mode == "new SSH wrong identity" {
					fingerprint = strings.Repeat("b", 64)
				}
				return &fakeTerminal{evidence: terminal.Evidence{Mode: terminal.Ready, Owner: terminal.OwnerNone, OwnerQueryAvailable: true}, version: "7.24.6", fingerprint: fingerprint, historyKnown: true}, nil
			}
			switch mode {
			case "confirmed with cleanup remaining":
				r.bridge = &routeros.APIBridge{}
				r.completed = true
			case "taken":
				r.taken = true
			case "poisoned":
				r.poisoned = true
			case "pending plan":
				r.bridge = &routeros.APIBridge{}
			case "API owner active":
				a.inv.SafeMode.Enabled = true
			case "unknown API owner":
				a.inv.SafeMode = nil
			}
			e := r.ResetIdle(context.Background())
			if mode == "idle" || mode == "confirmed with cleanup remaining" {
				if e != nil || calls != 1 || r.sessionID == oldID || r.taken || r.bridge != nil || !term.Snapshot().Closed {
					t.Fatalf("fresh idle session failed: %v", e)
				}
			} else {
				if e == nil {
					t.Fatal("reset manufactured owner authority")
				}
				if mode != "new SSH wrong identity" && calls != 0 {
					t.Fatal("ineligible reset opened new channel")
				}
			}
		})
	}
}
func TestConcurrentCloseOwnerWinsIdleReset(t *testing.T) {
	r, _, term, _ := observerFixture()
	defer r.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	var candidate *fakeTerminal
	r.dialSSH = func(context.Context) (terminalChannel, error) {
		close(entered)
		<-release
		candidate = &fakeTerminal{evidence: terminal.Evidence{Mode: terminal.Ready, Owner: terminal.OwnerNone, OwnerQueryAvailable: true}, version: "7.24.6", fingerprint: term.fingerprint}
		return candidate, nil
	}
	done := make(chan error, 1)
	go func() { done <- r.ResetIdle(context.Background()) }()
	<-entered
	_ = r.CloseOwner()
	close(release)
	if e := <-done; e == nil {
		t.Fatal("concurrent owner close was overwritten")
	}
	if !candidate.Snapshot().Closed || !r.ownerClosed.Load() {
		t.Fatal("replacement channel remained alive")
	}
}

func TestDeviceIdentitySourceAlphabet(t *testing.T) {
	for _, value := range []string{"/ABCdef123+A", "+ABCD012345", "CHR1"} {
		if !validDeviceIdentifier(routeros.LicenseSystemID, value) {
			t.Fatal("opaque system-id rejected")
		}
	}
	for _, value := range []string{"", "A-B", "A_B", "A=B", "space value", "line\nbreak", "ctl\x00value", "$(command)", strings.Repeat("A", 129)} {
		if validDeviceIdentifier(routeros.LicenseSystemID, value) {
			t.Fatal("unbounded/control system-id accepted")
		}
	}
	for _, source := range []routeros.IdentitySource{routeros.LicenseSoftwareID, routeros.RouterBoardSerial} {
		if validDeviceIdentifier(source, "/ABC123") || validDeviceIdentifier(source, "+ABC123") || !validDeviceIdentifier(source, "ABCD-1234") {
			t.Fatal("system-id alphabet leaked into other source")
		}
	}
	cfg := fixedConfig(t)
	cfg.DeviceID = "/ABCdef123+A"
	if _, e := Target(cfg); e != nil {
		t.Fatal(e)
	}
}

func TestMissingConnectOrListenStaysUnknown(t *testing.T) {
	for _, field := range []string{"connect", "listen"} {
		t.Run(field, func(t *testing.T) {
			r, a, _, request := observerFixture()
			defer r.Close()
			request.Strict = false
			row := api.Record{".id": "*7", "name": "foreign"}
			for _, property := range observedFields[api.BGPConnection] {
				row[property] = ""
			}
			for _, property := range []string{"disabled", "dynamic", "connect", "listen", "multihop", "output.remove-private-as", "output.as-override"} {
				row[property] = "false"
			}
			delete(row, field)
			a.inv.Connections = []api.Record{row}
			a.properties["bgp-connection/*7/"+field] = api.PropertyValue{}
			_, err := r.Observe(context.Background(), request)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != "inspection-unknown-property-value-bgp-connection-"+field {
				t.Fatalf("unknown getter default was inferred: %v", err)
			}
			if a.credentialCalls != 0 || a.writes != 0 {
				t.Fatal("unknown property caused secret read or mutation")
			}
		})
	}
}

func TestTakeUsesRemainingSessionLifetime(t *testing.T) {
	r, _, term, _ := observerFixture()
	defer r.Close()
	r.profile.ActivationVerified = true
	r.profile.MaxHistoryActions = 80
	r.sessionExpiresAt = time.Now().Add(8 * time.Second)
	if _, e := r.Take(context.Background(), time.Now().Add(10*time.Second)); e == nil {
		t.Fatal("Take ignored elapsed SSH lifetime")
	}
	if term.Snapshot().Owner != terminal.OwnerNone || r.taken {
		t.Fatal("insufficient remaining lifetime took owner")
	}
	r.sessionExpiresAt = time.Now().Add(30 * time.Second)
	owner, e := r.Take(context.Background(), time.Now().Add(10*time.Second))
	if e != nil || !owner.ThisSession || !r.taken {
		t.Fatalf("bounded remaining session rejected: %v", e)
	}
}

func TestPrependGetterNeverInfersZeroFromUnknown(t *testing.T) {
	r, a, request := recoveryAuthorityFixture(t)
	defer r.Close()
	request.CredentialAuthority = nil
	a.properties["bgp-connection/*7/output.default-prepend"] = api.PropertyValue{}
	if _, e := r.Observe(context.Background(), request); e == nil {
		t.Fatal("print zero plus unknown getter became verified zero")
	}
	if a.credentialCalls != 0 {
		t.Fatal("unknown prepend triggered secret observation")
	}
}
