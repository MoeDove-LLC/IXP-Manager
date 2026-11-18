package routeros

import "github.com/moedove/ixp-manager/internal/routing"

// BuildReferencedPlan builds the same canonical plan using credential revision
// references rather than plaintext passwords. Actual credentials are still
// required by Compile whenever a new key must be written. This is structural
// planning only; Stage must independently re-read and verify the baseline.
func BuildReferencedPlan(target Target, desired Desired, current Snapshot, options PlanOptions) (Plan, error) {
	if err := validateTarget(target); err != nil {
		return Plan{}, err
	}
	router := routing.Router{ID: target.RouterID, ASN: desired.ASN, RouterID: desired.RouterID, LocalIPv4: desired.LocalIPv4, LocalIPv6: desired.LocalIPv6}
	peers := make([]routing.Peer, 0, len(desired.Peers))
	for _, peer := range desired.Peers {
		password := ""
		if peer.Authentication.Enabled {
			// The shared validator uses presence to validate the revision. The
			// placeholder is never stored in a Plan or passed to a transport.
			password = "structuralplaceholder"
		}
		peers = append(peers, routing.Peer{ID: peer.ID, ASN: peer.ASN, Address: peer.Address, Enabled: true, MaxPrefixes: peer.MaxPrefixes, AllowedPrefixes: peer.AllowedPrefixes, AllowedRoutes: peer.AllowedRoutes, AuthenticationRevision: peer.Authentication.Revision, Password: password})
	}
	canonical, err := normalizeDesired(router, peers)
	if err != nil {
		return Plan{}, err
	}
	return build(target, canonical, current, options)
}
