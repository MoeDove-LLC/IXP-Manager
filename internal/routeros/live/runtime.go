package live

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

type apiChannel interface {
	Inventory(context.Context) (api.Inventory, error)
	ReadProperty(context.Context, api.Resource, string, string) (api.PropertyValue, error)
	ReadCredential(context.Context, string) (api.Credential, error)
	Apply(context.Context, api.Mutation) (api.Result, error)
	Close() error
}
type terminalChannel interface {
	Synchronize(context.Context) (terminal.Evidence, error)
	Query(context.Context, terminal.Query) (terminal.Result, error)
	OwnerEvidence(context.Context) (terminal.Evidence, error)
	EnterSafeMode(context.Context) (terminal.Evidence, error)
	ConfirmSafeMode(context.Context) (terminal.Confirmation, error)
	CancelSafeMode(context.Context) (terminal.Cancellation, error)
	Snapshot() terminal.Evidence
	Close() error
}

type ownedMutationChannel interface {
	ExecuteOwned(context.Context, api.Mutation) (terminal.OwnedReceipt, error)
}

// Runtime owns one authenticated API connection and one persistent SSH channel.
// Its methods serialize device I/O. Close interrupts in-flight calls. There is
// deliberately no reconnect/takeover of a lost Safe Mode owner.
type Runtime struct {
	objectEpoch         atomic.Uint64
	objectSession       *objectActivation
	writeEvidence       atomic.Pointer[observedWriteEvidence]
	removalEvidence     atomic.Pointer[observedRemovalEvidence]
	writeEvidenceEpoch  atomic.Uint64
	sshIdentityVerified bool
	credentialIDs       map[string]string
	config              Config
	target              routeros.Target
	profile             NativeCapabilityProfile
	api                 apiChannel
	terminal            terminalChannel
	gate                chan struct{}
	closed              atomic.Bool
	ownerClosed         atomic.Bool
	closeOnce           sync.Once
	cancel              context.CancelFunc
	privateDir          string
	files               map[string]string
	sessionID           string
	sessionExpiresAt    time.Time
	deadline            time.Time
	taken               bool
	bridge              *routeros.APIBridge
	boundPlan           *routeros.Plan
	operations          []routeros.APIOperation
	cleanup             *cleanupSession
	preparedDiscard     *preparedDiscardSession
	removalScope        *ruleRemovalScope
	removalTail         *pendingRemovalTail
	operationIndex      int
	poisoned            bool
	mu                  sync.Mutex
	ownerMu             sync.Mutex
	ownerEpoch          uint64
	dialSSH             func(context.Context) (terminalChannel, error)
	completed           bool
	status              Status
}

var _ control.Runtime = (*Runtime)(nil)

// Open uses ctx only to bound startup; Close owns the persistent SSH lifetime.
// No command input or trust/native booleans are accepted from a caller.
func Open(ctx context.Context, cfg Config) (*Runtime, error) {
	p, e := validateConfig(cfg, true)
	if e != nil {
		return nil, e
	}
	defer clear(p.password)
	defer clear(p.identity)
	r := newRuntime(p.config, p.target, nil, nil)
	r.privateDir, e = os.MkdirTemp("", "ixp-routeros-live-")
	if e != nil {
		return nil, fail("private-runtime-directory")
	}
	good := false
	defer func() {
		if !good {
			_ = r.Close()
		}
	}()
	r.files = map[string]string{}
	for name, data := range map[string][]byte{"known_hosts": p.knownHostLine, "identity": p.identity} {
		path := filepath.Join(r.privateDir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fail("private-runtime-file")
		}
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return nil, fail("private-runtime-file")
		}
		sum := sha256.Sum256(data)
		r.files[path] = hex.EncodeToString(sum[:])
	}
	if e = r.verifyFiles(); e != nil {
		return nil, e
	}
	password := p.password
	if len(password) > 0 && password[len(password)-1] == '\n' {
		password = password[:len(password)-1]
		if len(password) > 0 && password[len(password)-1] == '\r' {
			password = password[:len(password)-1]
		}
	}
	if len(password) == 0 || strings.ContainsAny(string(password), "\x00\r\n") {
		return nil, fail("invalid-api-password-file")
	}
	r.api, e = api.Dial(ctx, api.Config{Address: cfg.API.Address, ServerName: cfg.API.ServerName, Username: cfg.API.Username, Password: password, RootCAs: p.roots, SPKISHA256: cfg.API.SPKISHA256, CommandTimeout: time.Duration(p.config.OperationTimeoutSeconds) * time.Second})
	if e != nil {
		return nil, fail("api-authentication-or-transport")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	limits := terminal.DefaultLimits()
	limits.Lifetime = time.Duration(p.config.SessionLifetimeSeconds) * time.Second
	limits.OperationTimeout = time.Duration(p.config.OperationTimeoutSeconds) * time.Second
	opts := terminal.SSHOptions{Host: cfg.SSH.Host, Port: cfg.SSH.Port, User: cfg.SSH.Username, KnownHosts: filepath.Join(r.privateDir, "known_hosts"), Identity: filepath.Join(r.privateDir, "identity")}
	r.dialSSH = func(ctx context.Context) (terminalChannel, error) { return terminal.DialSSH(ctx, opts, limits) }
	r.sessionExpiresAt = time.Now().Add(limits.Lifetime)
	r.status.SessionExpiresAt = r.sessionExpiresAt
	r.terminal, e = r.dialSSH(lifetime)
	if e != nil {
		return nil, fail("ssh-authentication-or-transport")
	}
	startup, cancelStartup := context.WithTimeout(ctx, time.Duration(p.config.ObservationTimeoutSeconds)*time.Second)
	defer cancelStartup()
	if _, e = r.terminal.Synchronize(startup); e != nil {
		return nil, fail("ssh-synchronization")
	}
	// Verify the cross-channel identity before returning a usable runtime. Full
	// controlled-state inspection follows in Observe with the private manifest.
	inv, e := r.api.Inventory(startup)
	if e != nil {
		return nil, fail("api-inventory")
	}
	if _, e = r.identity(startup, inv); e != nil {
		return nil, e
	}
	good = true
	return r, nil
}
func newRuntime(cfg Config, target routeros.Target, a apiChannel, t terminalChannel) *Runtime {
	nonce := make([]byte, 16)
	_, e := rand.Read(nonce)
	if e != nil {
		panic("system randomness unavailable")
	}
	p := CapabilityProfile()
	expires := time.Now().Add(time.Duration(cfg.SessionLifetimeSeconds) * time.Second)
	return &Runtime{config: cfg, target: target, profile: p, api: a, terminal: t, gate: make(chan struct{}, 1), sessionID: hex.EncodeToString(nonce), sessionExpiresAt: expires, status: Status{Target: target, Profile: p, SessionExpiresAt: expires}}
}
func (r *Runtime) Binding() control.Binding {
	return control.Binding{Target: r.target, PrepareVerified: r.profile.PreparationVerified, ActivationVerified: r.profile.ActivationVerified, ConfirmationVerified: r.profile.ConfirmNativeVerified, MaxHistoryActions: r.profile.MaxHistoryActions, RuleBatchVerified: r.profile.RuleBatchVerified, CleanupVerified: r.profile.CleanupVerified, ObjectObservationVerified: r.profile.ObjectObservationVerified, PreparedDiscardVerified: r.profile.PreparedDiscardVerified, RuleRemovalBatchVerified: r.profile.RuleRemovalBatchVerified, RuleRemovalGlobalTailVerified: r.profile.RuleRemovalGlobalTailVerified}
}
func (r *Runtime) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.status
	s.Profile.Blockers = append([]string(nil), s.Profile.Blockers...)
	return s
}
func (r *Runtime) recordFailure(code string) {
	r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.LastCode = code
	r.status.CrossChannelIdentityVerified = false
	r.status.StrictSnapshotVerified = false
}
func (r *Runtime) verifyFiles() error {
	for path, want := range r.files {
		raw, e := readFixedFile(path, 64<<10, true)
		if e != nil {
			return fail("runtime-pin-file-changed")
		}
		sum := sha256.Sum256(raw)
		clear(raw)
		if hex.EncodeToString(sum[:]) != want {
			return fail("runtime-pin-file-changed")
		}
	}
	return nil
}
func (r *Runtime) begin(ctx context.Context) (context.Context, func(), error) {
	bounded, cancel := context.WithTimeout(ctx, time.Duration(r.config.ObservationTimeoutSeconds)*time.Second)
	select {
	case r.gate <- struct{}{}:
	case <-bounded.Done():
		r.invalidateWriteEvidence()
		r.invalidateObjectActivation()
		cancel()
		return nil, nil, fail("operation-deadline")
	}
	done := func() { <-r.gate; cancel() }
	if r.closed.Load() {
		r.invalidateWriteEvidence()
		r.invalidateObjectActivation()
		done()
		return nil, nil, fail("runtime-closed")
	}
	if e := r.verifyFiles(); e != nil {
		r.invalidateWriteEvidence()
		r.invalidateObjectActivation()
		done()
		return nil, nil, e
	}
	return bounded, done, nil
}
func (r *Runtime) BindPlan(p routeros.Plan, credentials routeros.Credentials) error {
	r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	ctx, done, e := r.begin(context.Background())
	if e != nil {
		return e
	}
	defer done()
	defer r.invalidateWriteEvidence()
	defer r.invalidateObjectActivation()
	_ = ctx
	if r.poisoned || r.taken || r.removalTail != nil || !r.cleanupFinished() || !r.preparedDiscardFinished() {
		return fail("runtime-not-bindable")
	}
	if p.Target != r.target {
		return fail("plan-target-mismatch")
	}
	b, e := routeros.NewAPIBridge(p, credentials)
	if e != nil {
		return fail("invalid-canonical-plan")
	}
	data, e := json.Marshal(p)
	if e != nil {
		return fail("invalid-canonical-plan")
	}
	var frozen routeros.Plan
	if json.Unmarshal(data, &frozen) != nil {
		return fail("invalid-canonical-plan")
	}
	r.bridge = b
	r.boundPlan = &frozen
	r.operations = b.Operations()
	r.operationIndex = 0
	r.completed = false
	r.cleanup = nil
	r.preparedDiscard = nil
	r.removalScope = nil
	return nil
}
func (r *Runtime) Apply(ctx context.Context, mutation routeros.APIMutation) (api.Result, error) {
	r.invalidateObjectActivation()
	ctx, done, e := r.begin(ctx)
	if e != nil {
		return api.Result{}, e
	}
	defer done()
	defer r.invalidateWriteEvidence()
	defer r.invalidateObjectActivation()
	cached := r.detachWriteEvidence()
	if !r.profile.PreparationVerified || mutation.Phase == routeros.Activate && !r.profile.ActivationVerified {
		return api.Result{}, fail("native-mutation-profile-unverified")
	}
	if r.poisoned || r.removalTail != nil || r.bridge == nil {
		return api.Result{}, fail("mutation-not-bound")
	}
	operations := r.operations
	if r.operationIndex >= len(operations) || operations[r.operationIndex].ID != mutation.OperationID {
		return api.Result{}, fail("operation-order-mismatch")
	}
	owner, e := r.owner(ctx)
	if e != nil {
		return api.Result{}, e
	}
	if mutation.Phase == routeros.Prepare {
		if !owner.Known || owner.Active || r.taken {
			return api.Result{}, fail("prepare-owner-conflict")
		}
	} else if mutation.Phase == routeros.Activate {
		if !r.taken || !owner.Alive || !owner.Known || !owner.ThisSession || !owner.Active || !owner.HistoryKnown || !time.Now().Before(r.deadline) || r.profile.MaxHistoryActions <= 0 || owner.HistoryActions >= r.profile.MaxHistoryActions {
			return api.Result{}, fail("activation-owner-or-budget-unknown")
		}
	} else {
		return api.Result{}, fail("unsupported-operation-phase")
	}
	var expected routeros.APIMutation
	for {
		var raw api.Inventory
		var evidence routeros.TargetEvidence
		usingEvidence := r.usableWriteEvidence(cached)
		if usingEvidence {
			raw, evidence = cached.raw, cached.evidence
		} else {
			raw, evidence, _, _, e = r.readState(ctx)
			if e != nil {
				return api.Result{}, e
			}
		}
		resolver, err := routeros.NewAPIResolver(raw, r.target, evidence)
		if err != nil {
			return api.Result{}, fail("mutation-reference-unverified")
		}
		expected, e = r.bridge.Mutation(mutation.OperationID, resolver)
		if e != nil || !reflect.DeepEqual(expected, mutation) {
			return api.Result{}, fail("mutation-does-not-match-compiled-plan")
		}
		// Check the same local channel immediately before writing; never acquire
		// a replacement owner. The controller still performs complete read-back.
		localOwner := r.terminal.Snapshot()
		if r.ownerClosed.Load() || localOwner.Closed || mutation.Phase == routeros.Activate && (!time.Now().Before(r.deadline) || localOwner.Mode != terminal.SafeOwned || localOwner.Owner != terminal.OwnerThisSession) {
			return api.Result{}, fail("owner-channel-ended")
		}
		if usingEvidence && !r.usableWriteEvidence(cached) {
			// Expiry during local reconstruction gets exactly one original full
			// read, not a failure or a second attempt to reuse the consumed copy.
			cached = nil
			continue
		}
		break
	}
	var result api.Result
	if mutation.Phase == routeros.Activate {
		writer, ok := r.terminal.(ownedMutationChannel)
		if !ok {
			return api.Result{}, fail("owned-mutation-channel-unavailable")
		}
		// Activation and cancellation share this exact owner stream. A write
		// queued on an independent API connection could otherwise arrive only
		// after Safe Mode had already rolled back and released its owner.
		receipt, writeErr := writer.ExecuteOwned(ctx, expected.Request)
		if writeErr != nil || !receipt.Completed || receipt.Evidence.Closed || receipt.Evidence.Mode != terminal.SafeOwned || receipt.Evidence.Owner != terminal.OwnerThisSession || !receipt.Evidence.OwnerQueryAvailable {
			r.poisoned = true
			return api.Result{}, &Failure{Code: "owned-mutation-outcome-unknown", Uncertain: true}
		}
		result = api.Result{Acknowledged: true}
	} else {
		result, e = r.api.Apply(ctx, expected.Request)
		if e != nil {
			r.poisoned = true
			return api.Result{}, &Failure{Code: "api-mutation-failed", Uncertain: api.IsUncertain(e)}
		}
	}
	if !result.Acknowledged || expected.Request.Action == api.Add && !nativeID.MatchString(result.ID) {
		r.poisoned = true
		return api.Result{}, &Failure{Code: "mutation-receipt-incomplete", Uncertain: true}
	}
	if expected.Request.Resource == api.BGPConnection {
		if expected.Request.Action == api.Add && result.Acknowledged && result.ID != "" {
			for _, a := range expected.Request.Attributes {
				if a.Name == "name" {
					if r.credentialIDs == nil {
						r.credentialIDs = map[string]string{}
					}
					r.credentialIDs[strings.ToUpper(result.ID)] = a.Value
				}
			}
		}
		if expected.Request.Action == api.Remove {
			delete(r.credentialIDs, strings.ToUpper(expected.Request.ID))
		}
	}
	r.operationIndex++
	return result, nil
}
func (r *Runtime) Take(ctx context.Context, deadline time.Time) (control.Owner, error) {
	r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	ctx, done, e := r.begin(ctx)
	if e != nil {
		return control.Owner{}, e
	}
	defer done()
	defer r.invalidateWriteEvidence()
	defer r.invalidateObjectActivation()
	if !r.profile.ActivationVerified || !r.profile.SSHOwnerNativeVerified || r.profile.MaxHistoryActions <= 0 {
		return control.Owner{}, fail("native-activation-profile-unverified")
	}
	if r.poisoned || r.taken || r.removalTail != nil || r.preparedDiscard != nil || r.ownerClosed.Load() || deadline.Before(time.Now().Add(5*time.Second)) || r.sessionExpiresAt.IsZero() || deadline.After(r.sessionExpiresAt.Add(-time.Duration(r.config.OperationTimeoutSeconds)*time.Second)) {
		return control.Owner{}, fail("owner-take-not-permitted")
	}
	o, e := r.owner(ctx)
	if e != nil || !o.Known || o.Active {
		return control.Owner{}, fail("owner-conflict-or-unknown")
	}
	_, e = r.terminal.EnterSafeMode(ctx)
	if e != nil {
		r.poisoned = true
		return control.Owner{}, fail("owner-take-uncertain")
	}
	r.taken = true
	r.deadline = deadline
	o, e = r.owner(ctx)
	if e != nil || !o.ThisSession || !o.Active || !o.HistoryKnown {
		r.poisoned = true
		return o, fail("owner-take-not-verified")
	}
	return o, nil
}
func (r *Runtime) Owner(ctx context.Context) (control.Owner, error) {
	ctx, done, e := r.begin(ctx)
	if e != nil {
		return control.Owner{}, e
	}
	defer done()
	owner, err := r.owner(ctx)
	if r.removalTail != nil && (err != nil || !owner.Known || !owner.HistoryKnown || !owner.Alive || owner.Active || owner.ThisSession || owner.HistoryActions != 0) {
		r.poisoned = true
	}
	if err != nil || !owner.Known || !owner.HistoryKnown || !owner.Alive {
		r.invalidateWriteEvidence()
		r.invalidateObjectActivation()
	} else if r.removalEvidence.Load() != nil && (owner.Active || owner.ThisSession || owner.HistoryActions != 0) {
		// A later idle observation cannot make a pre-existing full read safe
		// again after a known intervening owner/history change.
		r.invalidateWriteEvidence()
	}
	return owner, err
}
func (r *Runtime) owner(ctx context.Context) (control.Owner, error) {
	if r.ownerClosed.Load() || r.terminal == nil || r.terminal.Snapshot().Closed {
		return control.Owner{SessionID: r.sessionID}, nil
	}
	evidence, e := r.terminal.OwnerEvidence(ctx)
	if e != nil {
		return control.Owner{SessionID: r.sessionID, Alive: !r.terminal.Snapshot().Closed}, fail("owner-query-failed")
	}
	o := control.Owner{SessionID: r.sessionID, Alive: !evidence.Closed, Known: evidence.OwnerQueryAvailable, Active: evidence.Owner == terminal.OwnerThisSession || evidence.Owner == terminal.OwnerOther, ThisSession: evidence.Owner == terminal.OwnerThisSession}
	if o.ThisSession && !r.taken {
		o.Known = false
		return o, fail("owner-not-taken-by-runtime")
	}
	result, e := r.terminal.Query(ctx, terminal.FloatingUndoCount)
	if e == nil {
		o.HistoryKnown = result.HistoryKnown
		o.HistoryActions = result.HistoryActions
	}
	if r.taken && (!o.Known || !o.ThisSession || !o.Active) {
		r.poisoned = true
	}
	return o, nil
}
func (r *Runtime) Confirm(ctx context.Context) (control.Owner, error) {
	r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	ctx, done, e := r.begin(ctx)
	if e != nil {
		return control.Owner{}, e
	}
	defer done()
	defer r.invalidateWriteEvidence()
	defer r.invalidateObjectActivation()
	if !r.profile.ActivationVerified || !r.profile.ConfirmNativeVerified || !r.taken || r.poisoned || !time.Now().Before(r.deadline) {
		return control.Owner{}, fail("confirmation-not-permitted")
	}
	o, e := r.owner(ctx)
	if e != nil || !o.Known || !o.ThisSession || !o.Active || !o.HistoryKnown {
		return o, fail("confirmation-owner-unknown")
	}
	result, e := r.terminal.ConfirmSafeMode(ctx)
	if e != nil || !result.Released {
		r.poisoned = true
		return control.Owner{}, fail("confirmation-release-uncertain")
	}
	r.taken = false
	r.completed = true
	return r.owner(ctx)
}
func (r *Runtime) Cancel(ctx context.Context) (control.Owner, error) {
	r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	ctx, done, e := r.begin(ctx)
	if e != nil {
		return control.Owner{}, e
	}
	defer done()
	defer r.invalidateWriteEvidence()
	defer r.invalidateObjectActivation()
	if !r.taken || r.ownerClosed.Load() {
		return control.Owner{}, fail("original-owner-unavailable")
	}
	result, e := r.terminal.CancelSafeMode(ctx)
	r.ownerClosed.Store(true)
	r.poisoned = true
	if e != nil || !result.SessionEnded {
		return control.Owner{SessionID: r.sessionID}, fail("cancellation-unverified")
	}
	return control.Owner{SessionID: r.sessionID}, nil // No rollback assertion without independent API read-back.
}
func (r *Runtime) CloseOwner() error {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	return r.closeOwnerLocked()
}
func (r *Runtime) closeOwnerLocked() error {
	r.invalidateObjectActivation()
	defer r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	defer r.invalidateWriteEvidence()
	r.ownerEpoch++
	if r.ownerClosed.CompareAndSwap(false, true) && r.terminal != nil {
		if r.cancel != nil {
			r.cancel()
		}
		return r.terminal.Close()
	}
	return nil
}
func (r *Runtime) Close() error {
	var result error
	r.closeOnce.Do(func() {
		r.invalidateWriteEvidence()
		r.closed.Store(true)
		if r.api != nil {
			result = errors.Join(result, r.api.Close())
		}
		result = errors.Join(result, r.CloseOwner())
		if r.privateDir != "" {
			if e := os.RemoveAll(r.privateDir); e != nil {
				result = fail("private-runtime-cleanup")
			}
		}
	})
	if result != nil {
		return fail("runtime-close-failed")
	}
	return nil
}
