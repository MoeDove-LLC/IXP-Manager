package app

import (
	"net/http"

	"github.com/moedove/ixp-manager/internal/telemetry"
)

func monitoringRouterIdentity(router Record) telemetry.RouterIdentity {
	return telemetry.RouterIdentity{Platform: routerPlatform(router), RouterID: str(router, "routerId"), ASN: uint32(number(router, "asn"))}
}

func (a *App) monitoringBindings(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "GET" || !session.API || !contains(session.Scopes, "monitoring:write") || (str(user, "role") != "admin" && str(user, "role") != "operator") {
		problem(w, 403, "router-bound collector token required")
		return
	}
	id := r.URL.Query().Get("routerId")
	if !contains(session.RouterIDs, id) {
		problem(w, 403, "collector token does not own this router")
		return
	}
	s := a.store.Snapshot()
	router := find(&s, "routers", id)
	if router == nil {
		problem(w, 404, "router not found")
		return
	}
	result := telemetry.Bindings{RouterID: id, Ports: []telemetry.PortBinding{}, Sessions: []telemetry.SessionBinding{}}
	if requested, exists := r.URL.Query()["routerIdentity"]; exists {
		if len(requested) != 1 || requested[0] != "quagga" || routerPlatform(router) != "quagga" {
			problem(w, 422, "identity binding does not match the inventoried Quagga platform")
			return
		}
		identity := monitoringRouterIdentity(router)
		result.RouterIdentity = &identity
	}
	ports := map[string]bool{}
	for _, connection := range s.Resources["connections"] {
		if str(connection, "vlanId") != str(router, "vlanId") {
			continue
		}
		ids, _ := connectionPortIDs(connection)
		for _, portID := range ids {
			if ports[portID] {
				continue
			}
			ports[portID] = true
			if len(monitoringPortMembers(&s, portID)) != 1 {
				continue
			}
			result.Ports = append(result.Ports, telemetry.PortBinding{PortID: portID, OwnershipRevision: monitoringBindingRevision(&s, "port", portID, id)})
		}
	}
	for _, peer := range s.Resources["peers"] {
		if str(peer, "routerId") == id && peerObservationBound(&s, peer) {
			pid := str(peer, "id")
			result.Sessions = append(result.Sessions, telemetry.SessionBinding{PeerID: pid, OwnershipRevision: monitoringBindingRevision(&s, "peer", pid, id)})
		}
	}
	respond(w, 200, result)
}
