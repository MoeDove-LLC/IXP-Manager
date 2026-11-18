package app

import (
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/telemetry"
)

func (a *App) initMonitoring() error {
	store, err := telemetry.Open(filepath.Join(filepath.Dir(a.store.path), "telemetry"))
	if err != nil {
		return err
	}
	a.telemetry = store
	return nil
}
func monitoringConnectionPorts(c Record) []string { ids, _ := connectionPortIDs(c); return ids }

func monitoringPortMembers(s *State, id string) []string {
	members := []string{}
	if owner := str(find(s, "ports", id), "memberId"); owner != "" {
		members = append(members, owner)
	}
	for _, c := range s.Resources["connections"] {
		for _, portID := range monitoringConnectionPorts(c) {
			if portID == id && !contains(members, str(c, "memberId")) {
				members = append(members, str(c, "memberId"))
			}
		}
	}
	return members
}
func monitoringPortVisible(s *State, id string, user Record) bool {
	if str(user, "role") != "member" {
		return true
	}
	return contains(monitoringPortMembers(s, id), str(user, "memberId"))
}
func freshness(at, now time.Time) string {
	if at.IsZero() {
		return "unknown"
	}
	if now.Sub(at) > telemetry.StaleAfter {
		return "stale"
	}
	return "fresh"
}
func (a *App) monitoring(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if a.telemetry == nil {
		problem(w, 503, "telemetry store unavailable")
		return
	}
	now := time.Now().UTC()
	state := a.store.Snapshot()
	switch r.URL.Path {
	case "/api/monitoring/bindings":
		a.monitoringBindings(w, r, user, session)
		return
	case "/api/monitoring/ingest":
		if r.Method != "POST" {
			problem(w, 405, "method not allowed")
			return
		}
		if !session.API || !contains(session.Scopes, "monitoring:write") || (str(user, "role") != "admin" && str(user, "role") != "operator") {
			problem(w, 403, "a scoped collector token is required")
			return
		}
		var batch telemetry.Batch
		if !decode(w, r, &batch) {
			return
		}
		// Read authority after the full request body; a slow upload must not use
		// the allocation or account generation that existed before body parsing.
		state = a.store.Snapshot()
		actor, err := lookingGlassActor(&state, user, session)
		if err != nil || (str(actor, "role") != "admin" && str(actor, "role") != "operator") {
			problem(w, 403, "collector authority changed")
			return
		}
		if !contains(session.RouterIDs, batch.RouterID) {
			problem(w, 403, "collector token is not bound to this router")
			return
		}
		if batch.ObservedAt.IsZero() || batch.ObservedAt.After(now.Add(30*time.Second)) || batch.ObservedAt.Before(now.Add(-10*time.Minute)) || len(batch.Ports)+len(batch.Sessions) == 0 || len(batch.Ports)+len(batch.Sessions) > 2048 {
			problem(w, 422, "invalid observation time or batch size")
			return
		}
		router := find(&state, "routers", batch.RouterID)
		if router == nil {
			problem(w, 400, "router does not exist")
			return
		}
		if len(batch.Sessions) > 0 && (routerPlatform(router) == "quagga" || batch.RouterIdentity != nil) &&
			(batch.RouterIdentity == nil || *batch.RouterIdentity != monitoringRouterIdentity(router)) {
			problem(w, 409, "Quagga local BGP identity is missing or no longer matches this router; fetch bindings and observe again")
			return
		}
		validPorts := []telemetry.PortSample{}
		validSessions := []telemetry.SessionSample{}
		skipped := 0
		identityMismatches := 0
		ignoredSources := 0
		for _, p := range batch.Ports {
			port := find(&state, "ports", p.PortID)
			if port == nil {
				problem(w, 400, "port does not exist")
				return
			}
			related := false
			for _, c := range state.Resources["connections"] {
				if str(c, "vlanId") == str(router, "vlanId") && contains(monitoringConnectionPorts(c), p.PortID) {
					related = true
					break
				}
			}
			if !related {
				problem(w, 403, "port is not connected to this router's VLAN")
				return
			}
			members := monitoringPortMembers(&state, p.PortID)
			if len(members) != 1 {
				problem(w, 403, "port requires an unambiguous member owner")
				return
			}
			if len(p.CounterEpoch) > 128 || strings.ContainsAny(p.CounterEpoch, "\x00\r\n") {
				problem(w, 422, "invalid counter epoch")
				return
			}
			expectedRevision := monitoringBindingRevision(&state, "port", p.PortID, batch.RouterID)
			if p.OwnershipRevision != expectedRevision || batch.ObservedAt.Before(observationOwnershipSince(&state, port)) {
				skipped++
				continue
			}
			p.CounterEpoch = digest(p.CounterEpoch + ":" + expectedRevision)
			p.MemberID = members[0]
			p.LinkSpeedMbps = uint64(number(port, "speed"))
			if !trafficSourceAllowed(&state, port, batch.RouterID) {
				ignoredSources++
				continue
			}
			if err := a.freezeTrafficSample(&state, port, &p); err != nil {
				problem(w, 422, err.Error())
				return
			}
			validPorts = append(validPorts, p)
		}
		for _, p := range batch.Sessions {
			if err := telemetry.ValidateSessionSample(p, false); err != nil {
				problem(w, 422, err.Error())
				return
			}
			if p.NeighborAddress != "" {
				p.NeighborAddress, _ = telemetry.NormalizeNeighborAddress(p.NeighborAddress)
			}
			peer := find(&state, "peers", p.PeerID)
			if peer == nil || str(peer, "routerId") != batch.RouterID {
				problem(w, 403, "peer does not belong to this router")
				return
			}
			if p.OwnershipRevision != monitoringBindingRevision(&state, "peer", p.PeerID, batch.RouterID) || !peerObservationBound(&state, peer) || batch.ObservedAt.Before(peerOwnershipSince(&state, peer)) {
				skipped++
				continue
			}
			if !observedPeerIdentityMatches(peer, p) {
				if p.State != "unknown" || p.NeighborAddress != "" || p.NeighborASN != 0 {
					identityMismatches++
				}
				p.State = "unknown"
				p.ReceivedPrefixes = nil
			}
			// Preserve the validated revision so future-skew old samples cannot be
			// rebound after a rapid same-member reallocation.
			p.MemberID = str(peer, "memberId")
			validSessions = append(validSessions, p)
		}
		batch.Ports = validPorts
		batch.Sessions = validSessions
		if len(validPorts)+len(validSessions) == 0 {
			respond(w, 200, Record{"ok": true, "acceptedPorts": 0, "acceptedSessions": 0, "skippedBeforeOwnership": skipped, "identityMismatches": identityMismatches, "ignoredCounterSources": ignoredSources})
			return
		}
		if len(validPorts) > 0 {
			if err := a.ensureObservationKey(); err != nil {
				problem(w, 500, "observation history key unavailable")
				return
			}
		}
		if err := a.telemetry.Ingest(batch, now); err != nil {
			problem(w, 422, err.Error())
			return
		}
		respond(w, 200, Record{"ok": true, "acceptedAt": now, "acceptedPorts": len(validPorts), "acceptedSessions": len(validSessions), "skippedBeforeOwnership": skipped, "identityMismatches": identityMismatches, "ignoredCounterSources": ignoredSources})
		return
	case "/api/monitoring/summary":
		if r.Method != "GET" {
			problem(w, 405, "method not allowed")
			return
		}
		observed := a.telemetry.Snapshot(now)
		ports := []Record{}
		sessions := []Record{}
		for _, p := range state.Resources["ports"] {
			id := str(p, "id")
			if !monitoringPortVisible(&state, id, user) {
				continue
			}
			members := monitoringPortMembers(&state, id)
			if str(user, "role") == "member" {
				members = []string{str(user, "memberId")}
			}
			item := Record{"portId": id, "name": str(p, "name"), "memberIds": members, "operStatus": "unknown", "status": "unknown", "inBps": nil, "outBps": nil}
			if point, ok := observed.Ports[id]; ok && currentPortObservation(&state, p, point) && (str(user, "role") != "member" || point.MemberID == str(user, "memberId")) {
				status := freshness(point.ObservedAt, now)
				item["observedAt"] = point.ObservedAt
				if str(user, "role") != "member" {
					item["inOctets"] = point.InOctets
					item["outOctets"] = point.OutOctets
				}
				item["operStatus"] = point.OperStatus
				item["status"] = status
				item["counterReset"] = point.CounterReset
				item["counterWrapped"] = point.CounterWrapped
				if status == "fresh" {
					item["inBps"] = point.InBps
					item["outBps"] = point.OutBps
				}
			}
			ports = append(ports, item)
		}
		for _, p := range state.Resources["peers"] {
			if str(user, "role") == "member" && str(p, "memberId") != str(user, "memberId") {
				continue
			}
			id := str(p, "id")
			item := Record{"peerId": id, "routerId": str(p, "routerId"), "name": str(p, "name"), "address": str(p, "address"), "asn": number(p, "asn"), "memberId": str(p, "memberId"), "configuredEnabled": p["enabled"] == true, "monitor": p["monitor"] != false, "state": "unknown", "status": "unknown", "identityVerified": false}
			if point, ok := observed.Sessions[id]; ok && point.RouterID == str(p, "routerId") && point.MemberID == str(p, "memberId") && point.OwnershipRevision == monitoringBindingRevision(&state, "peer", id, str(p, "routerId")) && peerObservationBound(&state, p) && !point.ObservedAt.Before(peerOwnershipSince(&state, p)) {
				item["observedAt"] = point.ObservedAt
				if observedPeerIdentityMatches(p, point.SessionSample) {
					item["identityVerified"] = true
					item["observedNeighborAddress"] = point.NeighborAddress
					item["observedNeighborAsn"] = point.NeighborASN
					item["state"] = point.State
					if point.ReceivedPrefixes != nil {
						item["receivedPrefixes"] = *point.ReceivedPrefixes
					}
					item["status"] = freshness(point.ObservedAt, now)
				} else {
					item["identityMismatch"] = point.NeighborAddress != "" || point.NeighborASN != 0
					if str(user, "role") != "member" && point.NeighborAddress != "" && point.NeighborASN != 0 {
						item["observedNeighborAddress"] = point.NeighborAddress
						item["observedNeighborAsn"] = point.NeighborASN
					}
				}
			}
			sessions = append(sessions, item)
		}
		respond(w, 200, Record{"observedAt": now, "staleAfterSeconds": int(telemetry.StaleAfter.Seconds()), "ports": ports, "sessions": sessions})
		return
	case "/api/monitoring/series":
		if r.Method != "GET" {
			problem(w, 405, "method not allowed")
			return
		}
		id := r.URL.Query().Get("portId")
		if find(&state, "ports", id) == nil || !monitoringPortVisible(&state, id, user) {
			problem(w, 404, "port not found")
			return
		}
		from, to := now.Add(-time.Hour), now
		var err error
		if value := r.URL.Query().Get("from"); value != "" {
			from, err = time.Parse(time.RFC3339Nano, value)
			if err != nil {
				problem(w, 400, "from must be RFC3339 timestamp")
				return
			}
		}
		if value := r.URL.Query().Get("to"); value != "" {
			to, err = time.Parse(time.RFC3339Nano, value)
			if err != nil {
				problem(w, 400, "to must be RFC3339 timestamp")
				return
			}
		}
		if from.After(to) || from.Before(now.Add(-telemetry.Retention-time.Minute)) || to.After(now.Add(30*time.Second)) {
			problem(w, 400, "series range must be ordered and within retention")
			return
		}
		if from.Before(now.Add(-telemetry.Retention)) {
			from = now.Add(-telemetry.Retention)
		}
		points := a.telemetry.Series(id, from, to)
		if str(user, "role") == "member" {
			items := []Record{}
			for _, point := range points {
				if point.MemberID == str(user, "memberId") && currentPortObservation(&state, find(&state, "ports", id), point) {
					items = append(items, Record{"observedAt": point.ObservedAt, "inBps": point.InBps, "outBps": point.OutBps, "operStatus": point.OperStatus, "counterReset": point.CounterReset, "counterWrapped": point.CounterWrapped})
				}
			}
			respond(w, 200, Record{"portId": id, "points": items})
		} else {
			respond(w, 200, Record{"portId": id, "points": points})
		}
		return
	}
	problem(w, 404, "monitoring route not found")
}

// A protocol ID is not evidence of its current neighbor identity: a daemon may
// still run an earlier configuration, or a local collector mapping may be wrong.
func observedPeerIdentityMatches(peer Record, sample telemetry.SessionSample) bool {
	if sample.NeighborAddress == "" || sample.NeighborASN == 0 {
		return false
	}
	address, err := telemetry.NormalizeNeighborAddress(sample.NeighborAddress)
	return err == nil && address == str(peer, "address") && float64(sample.NeighborASN) == number(peer, "asn")
}

func currentPortObservation(s *State, port Record, point telemetry.PortPoint) bool {
	if port == nil || point.OwnershipRevision == "" || point.ObservedAt.Before(observationOwnershipSince(s, port)) || !trafficSourceAllowed(s, port, point.RouterID) {
		return false
	}
	var expected string
	if point.RouterID == deviceCounterSource(str(port, "switchId")) {
		expected = deviceBindingRevision(s, port)
	} else {
		expected = monitoringBindingRevision(s, "port", str(port, "id"), point.RouterID)
	}
	return expected != "" && point.OwnershipRevision == expected
}
