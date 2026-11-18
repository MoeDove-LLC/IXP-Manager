package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/traffic"
)

type trafficTarget struct {
	Scope          string              `json:"scope"`
	ID             string              `json:"id"`
	Key            string              `json:"key"`
	Label          string              `json:"label"`
	Notice         string              `json:"notice,omitempty"`
	RequiresSide   bool                `json:"requiresSide,omitempty"`
	MemberRevision string              `json:"memberRevision,omitempty"`
	SourceControl  Record              `json:"sourceControl,omitempty"`
	Ports          []string            `json:"-"`
	SidePorts      map[string][]string `json:"-"`
}

func trafficScopeKey(scope, id, revision string) string { return scope + "/" + id + "/" + revision }
func (a *App) trafficActor(s *State, user Record, session Session) (Record, error) {
	actor, err := a.atlasActor(s, user, session, "admin", "operator", "viewer", "member")
	if err != nil {
		return nil, errors.New("traffic access changed")
	}
	if session.API && !contains(session.Scopes, "traffic:read") {
		return nil, errors.New("traffic:read scope required")
	}
	if str(actor, "role") == "member" && find(s, "members", str(actor, "memberId")) == nil {
		return nil, errors.New("member access changed")
	}
	return actor, nil
}
func addTrafficPort(target *trafficTarget, id, side string) {
	if id != "" && !contains(target.Ports, id) {
		target.Ports = append(target.Ports, id)
	}
	if side != "" {
		if target.SidePorts == nil {
			target.SidePorts = map[string][]string{}
		}
		if !contains(target.SidePorts[side], id) {
			target.SidePorts[side] = append(target.SidePorts[side], id)
		}
	}
}
func trafficResourceKind(scope string) string {
	return map[string]string{"port": "ports", "member": "members", "switch": "switches", "rack": "racks", "location": "locations", "infrastructure": "infrastructures", "connection": "connections"}[scope]
}
func (a *App) trafficTargets(s *State, user Record, now time.Time) ([]trafficTarget, error) {
	member := str(user, "role") == "member"
	memberID := ""
	memberRevision := ""
	if member {
		memberID = str(user, "memberId")
		memberRevision = a.trafficMemberRevision(s, memberID)
	}
	archive, err := a.telemetry.TrafficTargets(traffic.TargetQuery{MemberID: memberID, MemberRevision: memberRevision}, now)
	if err != nil {
		return nil, err
	}
	if archive.Truncated {
		return nil, errors.New("traffic attribution directory exceeds its bound; narrow the report before aggregation")
	}
	items := map[string]*trafficTarget{}
	ensure := func(scope, id, revision, label string) *trafficTarget {
		key := trafficScopeKey(scope, id, revision)
		if current := items[key]; current != nil {
			return current
		}
		t := &trafficTarget{Scope: scope, ID: id, Key: key, Label: label, MemberRevision: revision, RequiresSide: scope == "core-bundle", SidePorts: map[string][]string{}}
		items[key] = t
		return t
	}
	for _, old := range archive.Targets {
		if old.Scope == "member" && old.MemberRevision == "" {
			continue
		}
		if member && old.Scope != "member" && old.Scope != "connection" {
			continue
		}
		revision := ""
		if old.Scope == "member" {
			revision = old.MemberRevision
		}
		label := "历史 " + old.Scope + " · " + old.ID
		if current := find(s, trafficResourceKind(old.Scope), old.ID); current != nil && (old.Scope != "member" || revision == a.trafficMemberRevision(s, old.ID)) && (!member || old.Scope == "member" || str(current, "memberId") == memberID) {
			label = str(current, "name")
		}
		if old.Scope == "core-bundle" {
			if b := coreBundleByID(s, old.ID); b != nil {
				label = b.Name
			}
		}
		if member && old.Scope == "member" {
			label = "本成员历史（包括曾用端口）"
		}
		t := ensure(old.Scope, old.ID, revision, label)
		for _, id := range old.PortIDs {
			addTrafficPort(t, id, old.CoreSide)
		}
	}
	if member {
		ensure("member", memberID, memberRevision, "本成员历史（包括曾用端口）")
	}
	for _, p := range s.Resources["ports"] {
		id := str(p, "id")
		if member && !monitoringPortVisible(s, id, user) {
			continue
		}
		port := ensure("port", id, "", str(p, "name"))
		addTrafficPort(port, id, "")
		if !member {
			port.SourceControl = a.trafficSourceControl(s, p, str(user, "role") == "admin" || str(user, "role") == "operator")
		}
		_, sourceState := selectedCounterSource(s, p)
		port.Notice = map[string]string{"ambiguous": "多个路由采集器可能提供此端口计数，请在端口设置明确 counterSource；当前不混合这些来源。", "disabled": "此端口已停用计数器统计。", "mapping-required": "需先审核 SNMP 接口映射。", "unavailable": "尚无唯一可用计数器来源。"}[sourceState]
		if member {
			port.Notice += " 端口详情只显示当前归属的数据；历史总量请使用本成员历史。"
		}
		owners := monitoringPortMembers(s, id)
		if len(owners) == 1 {
			owner := owners[0]
			revision := a.trafficMemberRevision(s, owner)
			if !member || owner == memberID {
				m := find(s, "members", owner)
				t := ensure("member", owner, revision, str(m, "name"))
				addTrafficPort(t, id, "")
			}
		}
		d, _ := a.freezeTrafficDimensions(s, p, "")
		if member {
			for _, cid := range d.ConnectionIDs {
				c := find(s, "connections", cid)
				if str(c, "memberId") != memberID {
					continue
				}
				t := ensure("connection", cid, "", str(c, "name"))
				t.Notice = "此接入历史仅包含本成员代次的物理端口计数，可能包括同端口的其它 VLAN。"
				addTrafficPort(t, id, "")
			}
			continue
		}
		for _, ref := range []struct{ scope, id string }{{"switch", d.SwitchID}, {"rack", d.RackID}, {"location", d.LocationID}, {"infrastructure", d.InfrastructureID}} {
			if ref.id == "" {
				continue
			}
			label := str(find(s, trafficResourceKind(ref.scope), ref.id), "name")
			if label == "" {
				label = ref.id
			}
			addTrafficPort(ensure(ref.scope, ref.id, "", label), id, "")
		}
		if d.CoreBundleID != "" {
			label := d.CoreBundleID
			if b := coreBundleByID(s, d.CoreBundleID); b != nil {
				label = b.Name
			}
			addTrafficPort(ensure("core-bundle", d.CoreBundleID, "", label), id, d.CoreSide)
		}
		for _, cid := range d.ConnectionIDs {
			t := ensure("connection", cid, "", str(find(s, "connections", cid), "name"))
			t.Notice = "这是关联物理端口的计数，可能包含同端口上的其它 VLAN；不能当作独立 VLAN 流量。"
			addTrafficPort(t, id, "")
		}
	}
	out := []trafficTarget{}
	for _, t := range items {
		sort.Strings(t.Ports)
		for side := range t.SidePorts {
			sort.Strings(t.SidePorts[side])
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func (a *App) trafficRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if a.legacyTrafficRoutes(w, r, user, session) {
		return
	}
	if r.URL.Path == "/api/traffic/source" {
		a.trafficSourceRoute(w, r, user, session)
		return
	}
	if r.Method != "GET" {
		problem(w, 405, "traffic observations are read-only")
		return
	}
	if a.telemetry == nil {
		problem(w, 503, "traffic store unavailable")
		return
	}
	s := a.store.Snapshot()
	actor, err := a.trafficActor(&s, user, session)
	if err != nil {
		problem(w, 403, "traffic access denied")
		return
	}
	targets, err := a.trafficTargets(&s, actor, time.Now().UTC())
	if err != nil {
		problem(w, 503, "traffic archive could not be read")
		return
	}
	if session.API {
		for i := range targets {
			if targets[i].SourceControl != nil {
				targets[i].SourceControl["editable"] = false
			}
		}
	}
	if r.URL.Path == "/api/traffic/catalog" {
		metrics := []Record{}
		for _, m := range []struct{ id, label, total, rate string }{{"octets", "接口字节", "bytes", "bits/s"}, {"unicast", "单播包", "packets", "packets/s"}, {"multicast", "组播包", "packets", "packets/s"}, {"broadcast", "广播包", "packets", "packets/s"}, {"errors", "接口错误", "events", "events/s"}, {"discards", "接口丢弃", "events", "events/s"}} {
			metrics = append(metrics, Record{"id": m.id, "label": m.label, "totalUnit": m.total, "rateUnit": m.rate})
		}
		respond(w, 200, Record{"sourceKind": "counter", "timezone": "UTC", "targets": targets, "metrics": metrics, "resolutions": []Record{{"id": "5m", "label": "5 分钟", "retentionDays": 90}, {"id": "1h", "label": "小时", "retentionDays": 400}, {"id": "1d", "label": "UTC 日", "retentionDays": 3653}}, "defaults": Record{"metric": "octets", "resolution": "5m", "hours": 24}, "storage": Record{"archiveQuotaBytes": 2 << 30, "publishedArchiveBudgetBytes": 768 << 20, "rawRetentionHours": 24}, "notice": "保留期为上限，实际可用范围受配额与采集覆盖影响。端口计数无法拆分地址族、VLAN 或成员对。"})
		return
	}
	if r.URL.Path != "/api/traffic/query" {
		problem(w, 404, "traffic route not found")
		return
	}
	values := r.URL.Query()
	q := traffic.Query{Scope: values.Get("scope"), ID: values.Get("id"), Metric: values.Get("metric"), Resolution: values.Get("resolution"), CoreSide: values.Get("side")}
	if q.Metric == "" {
		q.Metric = "octets"
	}
	if q.Resolution == "" {
		q.Resolution = "5m"
	}
	revision := values.Get("memberRevision")
	if str(actor, "role") == "member" {
		revision = a.trafficMemberRevision(&s, str(actor, "memberId"))
	} else if q.Scope == "member" && revision == "" {
		revision = a.trafficMemberRevision(&s, q.ID)
	}
	var chosen *trafficTarget
	for i := range targets {
		t := &targets[i]
		if t.Scope == q.Scope && t.ID == q.ID && (q.Scope != "member" || t.MemberRevision == revision) {
			chosen = t
			break
		}
	}
	if chosen == nil {
		problem(w, 404, "traffic target not found")
		return
	}
	q.ExpectedPortIDs = append([]string(nil), chosen.Ports...)
	if q.Scope == "member" {
		q.MemberRevision = chosen.MemberRevision
	}
	if q.Scope == "core-bundle" {
		if q.CoreSide != "a" && q.CoreSide != "b" {
			problem(w, 400, "choose core side a or b")
			return
		}
		q.ExpectedPortIDs = append([]string(nil), chosen.SidePorts[q.CoreSide]...)
	} else if q.CoreSide != "" {
		problem(w, 400, "side applies only to core-bundle")
		return
	}
	if str(actor, "role") == "member" {
		q.MemberID = str(actor, "memberId")
		q.MemberRevision = revision
		if q.Scope == "port" {
			p := find(&s, "ports", q.ID)
			source, _ := selectedCounterSource(&s, p)
			if source == deviceCounterSource(str(p, "switchId")) {
				q.OwnershipRevision = deviceBindingRevision(&s, p)
			} else if source != "" {
				q.OwnershipRevision = monitoringBindingRevision(&s, "port", q.ID, source)
			} else {
				q.OwnershipRevision = strings.Repeat("0", 64)
			}
		}
	}
	now := time.Now().UTC()
	q.From, q.To = now.Add(-24*time.Hour), now
	for _, field := range []struct {
		name string
		dest *time.Time
	}{{"from", &q.From}, {"to", &q.To}} {
		if raw := values.Get(field.name); raw != "" {
			at, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				problem(w, 400, "traffic times must be RFC3339")
				return
			}
			*field.dest = at.UTC()
		}
	}
	if !q.From.Before(q.To) || q.To.After(now.Add(30*time.Second)) {
		problem(w, 400, "traffic time range must be ordered and not in the future")
		return
	}
	step := traffic.Duration(q.Resolution)
	if step <= 0 {
		problem(w, 400, "unknown traffic resolution")
		return
	}
	requestedFrom, requestedTo := q.From, q.To
	q.From = q.From.Truncate(step)
	q.To = q.To.Truncate(step)
	if q.To.Before(requestedTo) {
		q.To = q.To.Add(step)
	}
	result, err := a.telemetry.QueryTraffic(q, now)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	// Archive I/O must not outlive an authorization or current-port allocation.
	latest := a.store.Snapshot()
	current, authErr := a.trafficActor(&latest, user, session)
	if authErr != nil || str(current, "role") != str(actor, "role") {
		problem(w, 403, "traffic access changed")
		return
	}
	if str(actor, "role") == "member" {
		if str(current, "memberId") != q.MemberID || a.trafficMemberRevision(&latest, q.MemberID) != q.MemberRevision {
			problem(w, 403, "member history access changed")
			return
		}
		if q.Scope == "port" {
			p := find(&latest, "ports", q.ID)
			source, _ := selectedCounterSource(&latest, p)
			expected := strings.Repeat("0", 64)
			if source == deviceCounterSource(str(p, "switchId")) {
				expected = deviceBindingRevision(&latest, p)
			} else if source != "" {
				expected = monitoringBindingRevision(&latest, "port", q.ID, source)
			}
			if !monitoringPortVisible(&latest, q.ID, current) || expected != q.OwnershipRevision {
				problem(w, 409, "port ownership changed; reload traffic targets")
				return
			}
		}
	}
	encoded, _ := json.Marshal(result)
	out := Record{}
	_ = json.Unmarshal(encoded, &out)
	out["targetLabel"], out["scopeNotice"], out["side"] = chosen.Label, chosen.Notice, q.CoreSide
	out["requestedFrom"], out["requestedTo"] = requestedFrom, requestedTo
	out["alignmentNotice"] = "结果覆盖完整 UTC 时间桶；窗口边界已按所选分辨率对齐。"
	respond(w, 200, out)
}
