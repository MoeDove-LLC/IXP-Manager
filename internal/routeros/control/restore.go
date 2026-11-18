package control

import (
	"context"
	"reflect"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// RestoreReview describes one new transaction that returns a retained,
// unconfirmed candidate to the previously verified managed policy.
type RestoreReview struct {
	TransactionID string        `json:"transactionId"`
	PlanChecksum  string        `json:"planChecksum"`
	Plan          routeros.Plan `json:"plan"`
	ObservedAt    time.Time     `json:"observedAt"`
}

type RestoreRequest struct {
	TransactionID, PlanChecksum, RestoreChecksum string
	Credentials, BaselineCredentials             routeros.Credentials
	Timeout                                      time.Duration
	ExclusiveAccessAcknowledged                  bool
}

type restoreCredential struct {
	ObjectID string `json:"objectId"`
	Digest   string `json:"digest"`
}

// Embedded in the next begin record so replacing a pending transaction is
// atomic with preserving the recovery obligation. A failed restore must never
// make the retained candidate an ordinary deployment baseline.
type restoreLink struct {
	TransactionID string                       `json:"transactionId"`
	PlanChecksum  string                       `json:"planChecksum"`
	Credentials   map[string]restoreCredential `json:"credentials"`
}

type restoreBasis struct {
	link         restoreLink
	state        deviceState
	known        routeros.KnownState
	desired      routeros.Desired
	generation   string
	policySHA256 string
	candidateASN uint32
	candidateRID string
}

// restoreBasisLocked derives the only allowed destination from the authenticated
// journal. It also works while replaying a restore begin, before live I/O.
func (s *Supervisor) restoreBasisLocked() (restoreBasis, error) {
	if s.begin == nil || s.log.poisoned || s.lastIntent != nil || s.batch != nil || s.removalBatch != nil ||
		s.status.PreparedDiscard.Phase != PreparedDiscardNone || s.cleanup.Pending != nil || s.status.ConfirmationVerified {
		return restoreBasis{}, failure("restore-state-not-ready")
	}
	basis := restoreBasis{link: restoreLink{TransactionID: s.status.TransactionID, PlanChecksum: s.status.PlanChecksum}}
	if s.begin.Restore != nil {
		// A failed restore returns to the same retained candidate. Its recorded
		// original policy and credential fingerprints still govern a retry.
		if s.status.Phase != PreparationFailed && (s.status.Phase != RolledBack || !s.status.RollbackVerified) {
			return restoreBasis{}, failure("restore-state-not-ready")
		}
		basis.state, basis.known = s.begin.Baseline, s.begin.Known
		basis.desired, basis.generation = s.begin.Plan.Desired, s.begin.Plan.Generation
		basis.policySHA256 = s.begin.Plan.PolicyChecksum
		basis.candidateASN, basis.candidateRID = s.begin.Plan.Base.ASN, s.begin.Plan.Base.RouterID
		basis.link.Credentials = s.begin.Restore.Credentials
		return basis, nil
	}
	if s.status.Phase != RollbackPending || s.status.LastCode != UnconfirmedCandidateRetainedCode ||
		s.rollback == nil || !s.desiredVerified || s.confirmIntent || s.status.RollbackVerified {
		return restoreBasis{}, failure("restore-state-not-ready")
	}
	base := s.begin.Plan.Base
	if !base.Managed || base.ActiveGeneration == "" || base.ActiveGeneration != s.begin.Known.ActiveGeneration {
		return restoreBasis{}, failure("restore-managed-baseline-required")
	}
	var prior *routeros.KnownGeneration
	for i := range s.begin.Known.Generations {
		g := &s.begin.Known.Generations[i]
		if g.Target == s.config.Target && len(g.PolicyChecksum) == 64 && g.PolicyChecksum[:16] == base.ActiveGeneration {
			if prior != nil {
				return restoreBasis{}, failure("restore-original-generation-ambiguous")
			}
			prior = g
		}
	}
	if prior == nil || prior.Desired.ASN != base.ASN || prior.Desired.RouterID != base.RouterID {
		return restoreBasis{}, failure("restore-original-generation-unavailable")
	}
	known, err := transactionKnown(s.begin, true, true)
	if err != nil {
		return restoreBasis{}, err
	}
	refs := make(map[string]restoreCredential, len(prior.Desired.Peers))
	byName := make(map[string]objectState, len(base.Connections))
	for _, object := range s.begin.Baseline.Objects {
		if object.Resource == api.BGPConnection {
			byName[object.Attributes["name"]] = object
		}
	}
	for _, peer := range prior.Desired.Peers {
		name, err := routeros.ManagedConnectionName(s.config.Target.RouterID, peer.ID)
		object := byName[name]
		if err != nil || !validObjectID(object.ID) || !object.CredentialKnown || len(object.CredentialDigest) != 64 {
			return restoreBasis{}, failure("restore-original-credential-unavailable")
		}
		refs[peer.ID] = restoreCredential{ObjectID: object.ID, Digest: object.CredentialDigest}
	}
	basis.state, basis.known = s.current, known
	basis.desired, basis.generation = prior.Desired, base.ActiveGeneration
	basis.policySHA256 = prior.PolicyChecksum
	basis.candidateASN, basis.candidateRID = s.begin.Plan.Desired.ASN, s.begin.Plan.Desired.RouterID
	basis.link.Credentials = refs
	return basis, nil
}

func validRestorePlan(plan routeros.Plan, basis restoreBasis) bool {
	if !plan.Base.Managed || plan.Base.TableFIB || plan.Base.ActiveGeneration != basis.known.ActiveGeneration ||
		plan.Generation != basis.generation || plan.PolicyChecksum != basis.policySHA256 || !reflect.DeepEqual(plan.Desired, basis.desired) {
		return false
	}
	activates := false
	for _, operation := range plan.Operations {
		if operation.Phase == routeros.Prepare {
			return false // The durable original generation and peers must be reusable.
		}
		activates = activates || operation.Phase == routeros.Activate
	}
	return activates
}

func (s *Supervisor) restoreReviewLocked(ctx context.Context, id, checksum string) (RestoreReview, restoreBasis, error) {
	if s.closed || !s.reopened {
		return RestoreReview{}, restoreBasis{}, failure("restore-fresh-runtime-required")
	}
	if id == "" || checksum == "" || id != s.status.TransactionID || checksum != s.status.PlanChecksum {
		return RestoreReview{}, restoreBasis{}, failure("restore-transaction-changed")
	}
	basis, err := s.restoreBasisLocked()
	if err != nil {
		return RestoreReview{}, restoreBasis{}, err
	}
	owner, err := s.owner(ctx)
	if err != nil || !s.reviewFreshOwner(owner) {
		return RestoreReview{}, restoreBasis{}, failure("restore-fresh-runtime-required")
	}
	review, err := s.reviewVerifiedState(ctx, reviewExpectation{
		state: basis.state, known: basis.known, active: basis.known.ActiveGeneration,
		asn: basis.candidateASN, routerID: basis.candidateRID, managed: true, freshSession: owner.SessionID,
	})
	if err != nil {
		return RestoreReview{}, restoreBasis{}, err
	}
	options := routeros.PlanOptions{AllowIdentityChange: basis.desired.ASN != basis.candidateASN || basis.desired.RouterID != basis.candidateRID}
	plan, err := routeros.BuildReferencedPlan(s.config.Target, basis.desired, review.Snapshot, options)
	if err != nil || !validRestorePlan(plan, basis) {
		return RestoreReview{}, restoreBasis{}, failure("restore-original-policy-cannot-be-reused")
	}
	return RestoreReview{TransactionID: id, PlanChecksum: checksum, Plan: plan, ObservedAt: review.ObservedAt}, basis, nil
}

func (s *Supervisor) ReviewRestore(ctx context.Context, id, checksum string) (RestoreReview, error) {
	if !s.mu.TryLock() {
		return RestoreReview{}, failure("review-transaction-busy")
	}
	defer s.mu.Unlock()
	review, _, err := s.restoreReviewLocked(ctx, id, checksum)
	return review, err
}

func (s *Supervisor) StageRestore(ctx context.Context, r RestoreRequest) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publishStatus()
	if !r.ExclusiveAccessAcknowledged {
		return s.status, failure("exclusive-access-precondition-required")
	}
	review, basis, err := s.restoreReviewLocked(ctx, r.TransactionID, r.PlanChecksum)
	if err != nil {
		return s.status, err
	}
	if review.Plan.Checksum != r.RestoreChecksum {
		return s.status, failure("restore-review-changed")
	}
	return s.stageLocked(ctx, Request{
		Plan: review.Plan, KnownState: basis.known, Credentials: r.Credentials, BaselineCredentials: r.BaselineCredentials,
		Timeout: r.Timeout, ExclusiveAccessAcknowledged: r.ExclusiveAccessAcknowledged,
	}, &basis)
}
