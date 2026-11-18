package control

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/moedove/ixp-manager/internal/routeros"
)

// transactionKnown retains old generations/connections until an explicitly
// reviewed cleanup. Existing peer authentication stays baseline in prepared
// state; only desired state promotes changed revisions. New prepared disabled
// connections already have their desired authentication initialized.
func transactionKnown(begin *beginRecord, desired, prepared bool) (routeros.KnownState, error) {
	raw, _ := json.Marshal(begin.Known)
	var known routeros.KnownState
	_ = json.Unmarshal(raw, &known)
	peers := map[string]routeros.KnownPeer{}
	for _, peer := range known.Peers {
		peers[peer.ID] = peer
	}
	for _, peer := range begin.Plan.Desired.Peers {
		if _, exists := peers[peer.ID]; desired || !exists {
			peers[peer.ID] = routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication}
		}
	}
	known.Peers = []routeros.KnownPeer{}
	for _, peer := range peers {
		known.Peers = append(known.Peers, peer)
	}
	sort.Slice(known.Peers, func(i, j int) bool { return known.Peers[i].ID < known.Peers[j].ID })
	if prepared || desired {
		exists := false
		for _, g := range known.Generations {
			if g.PolicyChecksum == begin.Plan.PolicyChecksum {
				exists = true
			}
		}
		if !exists {
			known.Generations = append(known.Generations, routeros.KnownGeneration{Target: begin.Plan.Target, Desired: begin.Plan.Desired, PolicyChecksum: begin.Plan.PolicyChecksum})
		}
		if len(known.Generations) > 64 {
			return routeros.KnownState{}, failure("retained-generation-limit")
		}
		sort.Slice(known.Generations, func(i, j int) bool { return known.Generations[i].PolicyChecksum < known.Generations[j].PolicyChecksum })
	}
	if desired {
		known.ActiveGeneration = begin.Plan.Generation
	}
	return known, nil
}
func desiredCredentials(request Request, baseline routeros.Credentials) (routeros.Credentials, error) {
	all := cloneCredentials(baseline)
	for _, peer := range request.Plan.Desired.Peers {
		if !peer.Authentication.Enabled {
			all[peer.ID] = routeros.Credential{Revision: peer.Authentication.Revision}
			continue
		}
		if value, ok := request.Credentials[peer.ID]; ok {
			all[peer.ID] = value
		}
		value, ok := all[peer.ID]
		if !ok || value.Revision != peer.Authentication.Revision || value.Password == "" {
			return nil, failure("desired-credential-revision-unavailable")
		}
	}
	return all, nil
}
func (s *Supervisor) readDesired(ctx context.Context) (Observation, deviceState, error) {
	known, err := transactionKnown(s.begin, true, true)
	if err != nil {
		return Observation{}, deviceState{}, err
	}
	o, state, err := s.observe(ctx, ReadRequest{KnownState: known, BaselineCredentials: s.desiredCredentials, Strict: true})
	if err != nil {
		return o, state, err
	}
	p := s.begin.Plan
	snapshot := o.Snapshot
	if snapshot == nil || snapshot.Target != p.Target || !snapshot.Managed || snapshot.TableFIB || !snapshot.CrossChannelIdentityVerified || snapshot.ActiveGeneration != p.Generation || snapshot.ASN != p.Desired.ASN || snapshot.RouterID != p.Desired.RouterID {
		return o, state, failure("desired-strict-snapshot-unverified")
	}
	if !completeCredentials(state) {
		return o, state, failure("desired-credentials-unverified")
	}
	return o, state, nil
}

// A fresh runtime must not infer sensitive getter authority from a name. Try
// the authenticated journal's current state, then its prepared rollback state.
// These are bounded read-only observations, not mutation retries or owner reset.
func (s *Supervisor) readRecovery(ctx context.Context) (Observation, deviceState, error) {
	desired, err := transactionKnown(s.begin, true, s.rollback != nil)
	if err != nil {
		return Observation{}, deviceState{}, err
	}
	o, state, firstErr := s.observe(ctx, ReadRequest{KnownState: desired, CredentialAuthority: authorityFor(s.current)})
	if firstErr == nil && completeCredentials(state) {
		return o, state, nil
	}
	if s.rollback == nil {
		return o, state, firstErr
	}
	prepared, err := transactionKnown(s.begin, false, true)
	if err != nil {
		return Observation{}, deviceState{}, err
	}
	next, nextState, nextErr := s.observe(ctx, ReadRequest{KnownState: prepared, CredentialAuthority: authorityFor(*s.rollback)})
	if nextErr == nil {
		return next, nextState, nil
	}
	if firstErr == nil {
		return o, state, nil
	}
	return Observation{}, deviceState{}, firstErr
}
