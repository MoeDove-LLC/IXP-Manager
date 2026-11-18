package app

import (
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

type peerReadinessView struct {
	PeerID                   string   `json:"peerId"`
	RouterID                 string   `json:"routerId"`
	Enabled                  bool     `json:"enabled"`
	CanGenerateConfiguration bool     `json:"canGenerateConfiguration"`
	Issues                   []string `json:"issues"`
	CheckedAt                string   `json:"checkedAt"`
}

// checkPeerReadiness uses a detached state snapshot. A disabled peer is only
// enabled in that copy for the same validation and rendering used by deployment.
func (a *App) checkPeerReadiness(s *State, peer Record) peerReadinessView {
	result := peerReadinessView{
		PeerID: str(peer, "id"), RouterID: str(peer, "routerId"), Enabled: peer["enabled"] == true,
		Issues: []string{}, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	router := find(s, "routers", result.RouterID)
	if router == nil {
		result.Issues = append(result.Issues, "router-missing")
	} else if !routing.RoleSupported(routerPlatform(router), routerRole(router)) {
		result.Issues = append(result.Issues, "router-role-unsupported")
	}
	member := find(s, "members", str(peer, "memberId"))
	if member == nil {
		result.Issues = append(result.Issues, "member-missing")
	} else {
		if str(member, "status") != "active" {
			result.Issues = append(result.Issues, "member-inactive")
		}
		if number(member, "asn") != number(peer, "asn") {
			result.Issues = append(result.Issues, "member-asn-mismatch")
		}
	}
	if router != nil {
		addr, err := netip.ParseAddr(str(peer, "address"))
		if err != nil || addr.Is4() && str(router, "localIpv4") == "" || addr.Is6() && str(router, "localIpv6") == "" {
			result.Issues = append(result.Issues, "local-address-family-missing")
		}
		if !peerHasActiveAttachment(s, router, peer) {
			result.Issues = append(result.Issues, "active-attachment-missing")
		}
		prefixes, _ := peer["allowedPrefixes"].([]any)
		origins, _ := peer["allowedRoutes"].([]any)
		if routerRole(router) == routing.RoleAS112 && len(prefixes)+len(origins) > 0 {
			result.Issues = append(result.Issues, "as112-has-inbound-policy")
		}
		if len(prefixes) == 0 && !peerCanObserveWithoutAllowlist(s, peer) {
			result.Issues = append(result.Issues, "allowlist-required")
		}
	}
	if len(result.Issues) > 0 {
		return result
	}
	peer["enabled"] = true
	if err := guardBIRD1ResourceMutation(s, "peers", "PUT", peer); err != nil {
		result.Issues = append(result.Issues, "peer-settings-invalid")
	} else if err := validate(s, "peers", peer); err != nil {
		result.Issues = append(result.Issues, "peer-settings-invalid")
	} else if _, _, err := a.peerAuthentication(s, peer); err != nil {
		result.Issues = append(result.Issues, "credential-invalid")
	} else if _, err := a.renderRouter(s, result.RouterID); err != nil {
		result.Issues = append(result.Issues, "router-render-failed")
	}
	result.CanGenerateConfiguration = len(result.Issues) == 0
	return result
}

func (a *App) peerReadiness(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if session.API || !noteStaff(user) {
		problem(w, 403, "peer readiness requires a staff browser session")
		return
	}
	if r.Method != "GET" {
		problem(w, 405, "peer readiness is read-only")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/peer-readiness/")
	if !identifier.MatchString(id) {
		problem(w, 404, "peer not found")
		return
	}
	snapshot := a.store.Snapshot()
	if _, err := a.atlasActor(&snapshot, user, session, "admin", "operator", "viewer"); err != nil {
		problem(w, 401, "peer readiness authority changed; sign in again")
		return
	}
	peer := find(&snapshot, "peers", id)
	if peer == nil {
		problem(w, 404, "peer not found")
		return
	}
	result := a.checkPeerReadiness(&snapshot, peer)
	if r.Context().Err() != nil {
		return
	}
	if _, err := a.atlasActorCurrent(user, session, "admin", "operator", "viewer"); err != nil {
		problem(w, 401, "peer readiness authority changed; sign in again")
		return
	}
	respond(w, 200, result)
}
