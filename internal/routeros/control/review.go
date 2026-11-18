package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

type reviewExpectation struct {
	known          routeros.KnownState
	state          deviceState
	active         string
	asn            uint32
	routerID       string
	managed, empty bool
	freshSession   string
	baseChecksum   string
	rollback       bool
}

// Review is a fresh read-only planning input, not an execution permit. It
// contains generation metadata and public credential revisions, never values.
type Review struct {
	KnownState routeros.KnownState `json:"knownState"`
	Snapshot   routeros.Snapshot   `json:"snapshot"`
	ObservedAt time.Time           `json:"observedAt"`
}

// Review exports the already authenticated manifest together with an actual
// strict snapshot, so an operator need not reconstruct generation JSON. It
// never changes the owner or adopts a partially prepared/cleaned namespace.
func (s *Supervisor) Review(ctx context.Context) (Review, error) {
	if !s.mu.TryLock() {
		return Review{}, failure("review-transaction-busy")
	}
	defer s.mu.Unlock()
	if s.closed || s.log.poisoned {
		return Review{}, failure("review-journal-unavailable")
	}
	if s.begin != nil && s.begin.Restore != nil && s.status.Phase != Confirmed {
		return Review{}, failure("restore-requires-resolution")
	}
	known := routeros.KnownState{Target: s.config.Target}
	if s.begin == nil {
		if s.status.Phase != Idle {
			return Review{}, failure("review-state-not-ready")
		}
		o, _, err := s.observe(ctx, ReadRequest{KnownState: known, Strict: true})
		if err != nil || o.Snapshot == nil || !o.Snapshot.NamespaceEmpty || !o.Owner.Known || o.Owner.Active {
			return Review{}, failure("review-empty-baseline-unverified")
		}
		return frozenReview(Review{KnownState: known, Snapshot: *o.Snapshot, ObservedAt: o.ObservedAt})
	}
	want := reviewExpectation{state: s.current, active: s.begin.Plan.Generation, asn: s.begin.Plan.Desired.ASN, routerID: s.begin.Plan.Desired.RouterID, managed: true}
	if s.status.Phase == RolledBack {
		if !s.reopened {
			return Review{}, failure("review-fresh-runtime-required")
		}
		if !s.status.RollbackVerified || s.rollback == nil {
			return Review{}, failure("review-rollback-baseline-unavailable")
		}
		want.rollback = true
		want.active, want.asn, want.routerID = s.begin.Plan.Base.ActiveGeneration, s.begin.Plan.Base.ASN, s.begin.Plan.Base.RouterID
		want.managed, want.empty = s.begin.Plan.Base.Managed, s.begin.Plan.Base.NamespaceEmpty
		switch s.status.PreparedDiscard.Phase {
		case PreparedDiscardNone:
			if s.preparedDiscard.Checksum != "" || s.preparedDiscard.Pending != nil {
				return Review{}, failure("review-state-not-ready")
			}
			// Bootstrap reject roots do not form a managed strict baseline.
			// They require the separately authorized discard lifecycle first.
			if !s.begin.Plan.Base.Managed || s.begin.Known.ActiveGeneration == "" || s.begin.Known.ActiveGeneration != s.begin.Plan.Base.ActiveGeneration {
				return Review{}, failure("review-rollback-baseline-unavailable")
			}
			var err error
			want.known, err = transactionKnown(s.begin, false, true)
			if err != nil {
				return Review{}, err
			}
			want.state = *s.rollback
		case PreparedDiscardComplete:
			if s.preparedDiscard.Pending != nil {
				return Review{}, failure("review-state-not-ready")
			}
			// transactionKnown(false,false) still adds desired peers. A fully
			// discarded preparation must use precisely the original manifest.
			raw, err := json.Marshal(s.begin.Known)
			if err != nil || json.Unmarshal(raw, &want.known) != nil {
				return Review{}, failure("review-encoding-failed")
			}
			want.state, want.baseChecksum = s.begin.Baseline, s.begin.Plan.BaseChecksum
		default:
			return Review{}, failure("review-state-not-ready")
		}
		call, cancel := s.operationContext(ctx)
		owner, err := s.runtime.Owner(call)
		cancel()
		if err != nil || !s.reviewFreshOwner(owner) {
			return Review{}, failure("review-fresh-runtime-required")
		}
		want.freshSession = owner.SessionID
	} else if s.status.Phase != Confirmed || !s.status.ConfirmationVerified || s.status.Cleanup.Phase != CleanupComplete || s.cleanup.Pending != nil {
		return Review{}, failure("review-state-not-ready")
	} else {
		want.known = s.cleanupKnown()
	}
	return s.reviewVerifiedState(ctx, want)
}

func (s *Supervisor) reviewFreshOwner(owner Owner) bool {
	return owner.Known && owner.Alive && !owner.Active && !owner.ThisSession && owner.HistoryKnown && owner.HistoryActions == 0 && validSessionID(owner.SessionID) && owner.SessionID != s.status.SessionID
}

func (s *Supervisor) reviewVerifiedState(ctx context.Context, want reviewExpectation) (Review, error) {
	known := want.known
	authority := authorityFor(want.state)
	o, state, err := s.observe(ctx, ReadRequest{KnownState: known, CredentialAuthority: authority})
	if err != nil || !o.Owner.Known || o.Owner.Active || !completeCredentials(state) || stateMAC(s.log.key, state) != stateMAC(s.log.key, want.state) {
		return Review{}, failure("review-journal-state-changed")
	}
	if want.freshSession != "" && (!s.reviewFreshOwner(o.Owner) || o.Owner.SessionID != want.freshSession) {
		return Review{}, failure("review-fresh-runtime-required")
	}
	expected := routeros.Credentials{}
	defer clear(expected)
	byName := map[string]objectState{}
	for _, object := range state.Objects {
		if object.Resource == api.BGPConnection {
			byName[object.Attributes["name"]] = object
		}
	}
	for _, peer := range known.Peers {
		name, e := routeros.ManagedConnectionName(s.config.Target.RouterID, peer.ID)
		object, present := byName[name]
		credential, observed := o.Credentials[object.ID]
		if e != nil || !present || !observed || !credential.Known {
			return Review{}, failure("review-credential-unverified")
		}
		raw := credential.Value.CopyBytes()
		if credentialMAC(s.log.key, object.ID, raw) != object.CredentialDigest {
			clear(raw)
			return Review{}, failure("review-credential-unverified")
		}
		expected[peer.ID] = routeros.Credential{Revision: peer.Authentication.Revision, Password: string(raw)}
		clear(raw)
	}
	final, finalState, err := s.observe(ctx, ReadRequest{KnownState: known, BaselineCredentials: expected, CredentialAuthority: authority, Strict: true})
	if err != nil || final.Snapshot == nil || !final.Owner.Known || final.Owner.Active || stateMAC(s.log.key, finalState) != stateMAC(s.log.key, state) {
		return Review{}, failure("review-strict-state-unverified")
	}
	if want.freshSession != "" && (!s.reviewFreshOwner(final.Owner) || final.Owner.SessionID != want.freshSession) {
		return Review{}, failure("review-fresh-runtime-required")
	}
	if final.Snapshot.Target != s.config.Target || final.Snapshot.Owner != routeros.OwnerName(s.config.Target.RouterID) || !final.Snapshot.CrossChannelIdentityVerified || final.Snapshot.TableFIB || final.Snapshot.ActiveGeneration != want.active || final.Snapshot.ASN != want.asn || final.Snapshot.RouterID != want.routerID || final.Snapshot.Managed != want.managed || final.Snapshot.NamespaceEmpty != want.empty {
		if want.rollback {
			return Review{}, failure("review-rollback-identity-changed")
		}
		return Review{}, failure("review-strict-state-unverified")
	}
	if want.baseChecksum != "" {
		raw, err := json.Marshal(final.Snapshot)
		if err != nil {
			return Review{}, failure("review-encoding-failed")
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != want.baseChecksum {
			return Review{}, failure("review-rollback-identity-changed")
		}
	}
	return frozenReview(Review{KnownState: known, Snapshot: *final.Snapshot, ObservedAt: final.ObservedAt})
}

func frozenReview(value Review) (Review, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Review{}, failure("review-encoding-failed")
	}
	var result Review
	if json.Unmarshal(data, &result) != nil {
		return Review{}, failure("review-encoding-failed")
	}
	return result, nil
}
