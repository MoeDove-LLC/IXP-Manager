package app

import (
	"errors"
	"net/http"
	"time"
)

func (a *App) trafficSourceControl(s *State, p Record, editable bool) Record {
	choices := []Record{{"value": "auto", "label": "自动：优先已审核 SNMP，否则唯一的路由采集器"}, {"value": "snmp", "label": "只使用此端口的已审核 SNMP 映射"}, {"value": "none", "label": "停用此端口的计数器摄入"}}
	dependencies := []Record{}
	for _, id := range trafficRouterCandidates(s, p) {
		r := find(s, "routers", id)
		dependencies = append(dependencies, r)
		choices = append(choices, Record{"value": "router:" + id, "label": "路由采集器 · " + str(r, "name")})
	}
	connections := []Record{}
	for _, c := range s.Resources["connections"] {
		if contains(monitoringConnectionPorts(c), str(p, "id")) {
			connections = append(connections, c)
		}
	}
	revision := a.coreHMAC("traffic-source", Record{"port": p, "switch": find(s, "switches", str(p, "switchId")), "routers": dependencies, "connections": connections})
	selected, status := selectedCounterSource(s, p)
	return Record{"configured": normalizedCounterSource(p), "status": status, "selectedSourceId": selected, "revision": revision, "choices": choices, "editable": editable}
}
func (a *App) trafficSourceRoute(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if session.API || (str(user, "role") != "admin" && str(user, "role") != "operator") {
		problem(w, 403, "staff browser session required")
		return
	}
	var body struct {
		PortID           string `json:"portId"`
		Selection        string `json:"selection"`
		ExpectedRevision string `json:"expectedRevision"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !identifier.MatchString(body.PortID) || body.ExpectedRevision == "" {
		problem(w, 428, "review this port's current counter source before changing it")
		return
	}
	status := 400
	var out Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.atlasActor(s, user, session, "admin", "operator")
		if err != nil {
			status = 403
			return errors.New("source change authority expired")
		}
		old := find(s, "ports", body.PortID)
		if old == nil {
			status = 404
			return errors.New("port not found")
		}
		view := a.trafficSourceControl(s, old, true)
		if str(view, "revision") != body.ExpectedRevision {
			status = 409
			return errors.New("counter source or dependent inventory changed; inspect the new revision")
		}
		allowed := false
		for _, choice := range view["choices"].([]Record) {
			allowed = allowed || str(choice, "value") == body.Selection
		}
		if !allowed {
			return errors.New("counter source is not an authorized choice for this port")
		}
		if normalizedCounterSource(old) == body.Selection {
			out = view
			return nil
		}
		next := Record{}
		for key, value := range old {
			next[key] = value
		}
		next["counterSource"] = body.Selection
		now := time.Now().UTC().Format(time.RFC3339Nano)
		next["updatedAt"] = now
		setOwnershipTime("ports", old, next, now)
		for i, p := range s.Resources["ports"] {
			if str(p, "id") == body.PortID {
				s.Resources["ports"][i] = next
			}
		}
		if err := validateAll(s); err != nil {
			return err
		}
		audit(s, str(actor, "id"), "traffic-counter-source", "ports", body.PortID)
		out = a.trafficSourceControl(s, next, true)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, Record{"sourceControl": out})
}
