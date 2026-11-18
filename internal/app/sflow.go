package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
)

func (a *App) initSFlow() error {
	store, err := sflow.Open(filepath.Join(filepath.Dir(a.store.path), "sflow"))
	if err != nil {
		return err
	}
	a.sflow = store
	return nil
}
func (a *App) sflowActor(s *State, user Record, session Session, write bool) (Record, []string, error) {
	roles := []string{"admin", "operator", "viewer", "member"}
	scope := "sflow:read"
	if write {
		roles = []string{"admin", "operator"}
		scope = "sflow:write"
	}
	actor, err := a.atlasActor(s, user, session, roles...)
	if err != nil {
		return nil, nil, errors.New("sFlow access changed")
	}
	if !session.API {
		if write {
			return nil, nil, errors.New("switch-bound collector token required")
		}
		return actor, nil, nil
	}
	for _, t := range s.Security[str(actor, "id")].Tokens {
		if t.ID == session.TokenID && time.Now().Before(t.ExpiresAt) && contains(t.Scopes, scope) {
			if str(actor, "role") == "member" && !write {
				return actor, nil, nil
			}
			if len(t.SwitchIDs) > 0 {
				return actor, append([]string(nil), t.SwitchIDs...), nil
			}
		}
	}
	return nil, nil, errors.New("sFlow token scope or switch bindings are unavailable")
}
func flowSwitchAllowed(ids []string, id string) bool { return len(ids) == 0 || contains(ids, id) }

func (a *App) acquireSFlowUpload(tokenID string) bool {
	a.sflowAdmissionMu.Lock()
	defer a.sflowAdmissionMu.Unlock()
	if a.sflowInFlight >= 16 || a.sflowReaders[tokenID] >= 2 {
		return false
	}
	if a.sflowReaders == nil {
		a.sflowReaders = map[string]int{}
	}
	a.sflowReaders[tokenID]++
	a.sflowInFlight++
	return true
}
func (a *App) releaseSFlowUpload(tokenID string) {
	a.sflowAdmissionMu.Lock()
	defer a.sflowAdmissionMu.Unlock()
	a.sflowReaders[tokenID]--
	if a.sflowReaders[tokenID] == 0 {
		delete(a.sflowReaders, tokenID)
	}
	a.sflowInFlight--
}

func (a *App) sflowVisibleMembers(s *State, scope []string) (map[string]bool, error) {
	allowed := map[string]bool{}
	if len(scope) == 0 {
		for _, m := range s.Resources["members"] {
			allowed[str(m, "id")] = true
		}
		return allowed, nil
	}
	for _, p := range s.Resources["ports"] {
		if contains(scope, str(p, "switchId")) && str(p, "memberId") != "" {
			allowed[str(p, "memberId")] = true
		}
	}
	for _, c := range s.Resources["connections"] {
		for _, id := range monitoringConnectionPorts(c) {
			if contains(scope, str(find(s, "ports", id), "switchId")) {
				allowed[str(c, "memberId")] = true
				break
			}
		}
	}
	history, err := a.sflow.Members(scope)
	if err != nil {
		return nil, err
	}
	for _, m := range history {
		if m.Revision != "" && a.trafficMemberRevision(s, m.ID) == m.Revision {
			allowed[m.ID] = true
		}
	}
	return allowed, nil
}
func (a *App) sflowRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if a.sflow == nil {
		problem(w, 503, "sFlow observation store unavailable")
		return
	}
	if a.sflowReceiverRoutes(w, r, user, session) {
		return
	}
	if r.URL.Path == "/api/sflow/delivery-receipts" {
		a.sflowDeliveryReport(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/sflow/bindings" || r.URL.Path == "/api/sflow/ingest" {
		a.sflowCollect(w, r, user, session)
		return
	}
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return
	}
	s := a.store.Snapshot()
	actor, scope, err := a.sflowActor(&s, user, session, false)
	if err != nil {
		problem(w, 403, "sFlow access denied")
		return
	}
	member := str(actor, "role") == "member"
	switch r.URL.Path {
	case "/api/sflow":
		memberScope, err := a.sflowVisibleMembers(&s, scope)
		if err != nil {
			problem(w, 503, "sFlow directory unavailable")
			return
		}
		statuses, err := a.sflow.Status()
		if err != nil {
			problem(w, 503, "sFlow status unavailable")
			return
		}
		exporters := []Record{}
		switches, vlans, members, connections := []Record{}, []Record{}, []Record{}, []Record{}
		if !member {
			for _, e := range s.Resources[sflowExporterKind] {
				if !flowSwitchAllowed(scope, str(e, "switchId")) {
					continue
				}
				view := a.resourceView(&s, sflowExporterKind, clean(e))
				view["receiveStatus"] = "unknown"
				for _, status := range statuses {
					if status.ExporterID == str(e, "id") && status.SwitchID == str(e, "switchId") {
						view["lastReceivedAt"] = status.LastReceivedAt
						view["receiveStatus"] = freshness(status.LastReceivedAt, time.Now())
						view["sequenceState"] = status.SequenceState
						view["pendingRestart"] = status.PendingRestart
					}
				}
				exporters = append(exporters, view)
			}
			for _, sw := range s.Resources["switches"] {
				if flowSwitchAllowed(scope, str(sw, "id")) {
					switches = append(switches, Record{"id": sw["id"], "name": sw["name"], "infrastructureId": fabricID(sw)})
				}
			}
		}
		for _, v := range s.Resources["vlans"] {
			allowed := !member && len(scope) == 0 || member && activeMemberOnVLAN(&s, str(actor, "memberId"), str(v, "id"))
			if !member && len(scope) > 0 {
				for _, sw := range switches {
					allowed = allowed || str(sw, "infrastructureId") == fabricID(v)
				}
			}
			if allowed {
				vlans = append(vlans, Record{"id": v["id"], "name": v["name"], "tag": v["tag"]})
			}
		}
		for _, m := range s.Resources["members"] {
			if !member && memberScope[str(m, "id")] || member && str(m, "id") == str(actor, "memberId") {
				members = append(members, Record{"id": m["id"], "name": m["name"]})
			}
		}
		for _, c := range s.Resources["connections"] {
			if member && str(c, "memberId") != str(actor, "memberId") {
				continue
			}
			allowed := member || len(scope) == 0
			for _, id := range monitoringConnectionPorts(c) {
				allowed = allowed || flowSwitchAllowed(scope, str(find(&s, "ports", id), "switchId"))
			}
			if allowed {
				connections = append(connections, Record{"id": c["id"], "name": c["name"], "memberId": c["memberId"], "vlanId": c["vlanId"]})
			}
		}
		usage := any(nil)
		if !member && (!session.API || len(scope) == 0) {
			usage = a.sflow.Usage()
		}
		receivers := []Record{}
		for _, receiver := range s.SFlowReceivers {
			if a.receiverVisible(&s, receiver, actor, scope) {
				receivers = append(receivers, a.receiverView(&s, receiver, actor, session))
			}
		}
		respond(w, 200, Record{"sourceKind": "sampled", "exporters": exporters, "receivers": receivers, "selectors": Record{"switches": switches, "vlans": vlans, "members": members, "connections": connections}, "usage": usage, "staleAfterSeconds": 120, "observedAt": time.Now().UTC(), "capabilities": Record{"staff": !member, "canManageExporters": !session.API && (str(actor, "role") == "admin" || str(actor, "role") == "operator"), "canManageReceivers": !session.API && receiverWritable(actor), "canApproveReceivers": !session.API && str(actor, "role") == "admin", "liveForwarding": true}, "notices": []string{"这是按接收时绑定归属的抽样估算，不是完整接口计数，也不证明最终转发或 BGP Established。", "只归属可唯一确定的成员入口采样点；核心/出口重复观测和未知 VLAN/接口不会冒充成员流量。", "UDP 源允许列表不提供密码学来源认证；需要受信任的监测网络。", "成员转发需要准确范围审批、逐包授权和collector本地显式允许；配置或socket提交不证明目标收到。"}})
	case "/api/sflow/query":
		a.sflowQuery(w, r, &s, actor, session, scope)
	default:
		problem(w, 404, "sFlow route not found")
	}
}
func (a *App) sflowCollect(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.URL.Path == "/api/sflow/bindings" {
		if r.Method != "GET" {
			problem(w, 405, "method not allowed")
			return
		}
		a.sflowMu.Lock()
		defer a.sflowMu.Unlock()
		mapping, status, err := a.currentFlowMapping(user, session, r.URL.Query().Get("exporterId"))
		if err != nil {
			problem(w, status, err.Error())
			return
		}
		e := mapping.Exporter
		issued := time.Now().UTC()
		a.store.mu.RLock()
		forward := a.canSFlowForward(&a.store.state, user, session, str(e, "switchId"))
		a.store.mu.RUnlock()
		respond(w, 200, sflow.Binding{ExporterID: str(e, "id"), SwitchID: str(e, "switchId"), MappingRevision: mapping.Revision, IssuedAt: issued, ExpiresAt: issued.Add(time.Minute), ForwardingAllowed: forward})
		return
	}
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if !session.API || !contains(session.Scopes, "sflow:write") || (str(user, "role") != "admin" && str(user, "role") != "operator") {
		problem(w, 403, "collector token required")
		return
	}
	if !a.acquireSFlowUpload(session.TokenID) {
		w.Header().Set("Retry-After", "1")
		problem(w, 429, "sFlow ingest is busy; discard this batch before rebinding")
		return
	}
	defer a.releaseSFlowUpload(session.TokenID)
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	var batch sflow.Batch
	if !decode(w, r, &batch) {
		return
	}
	if err := sflow.ValidateBatch(batch); err != nil {
		problem(w, 422, "invalid bounded sFlow metadata")
		return
	}
	batch.Datagram.AgentAddress = flowIP(batch.Datagram.AgentAddress)
	if batch.Datagram.Flows == nil {
		batch.Datagram.Flows = []sflow.Flow{}
	}
	if batch.Datagram.Counters == nil {
		batch.Datagram.Counters = []sflow.CounterSample{}
	}
	a.sflowMu.Lock()
	defer a.sflowMu.Unlock()
	mapping, status, err := a.currentFlowMapping(user, session, batch.ExporterID)
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	e := mapping.Exporter
	if batch.SwitchID != str(e, "switchId") {
		problem(w, 403, "exporter is outside collector switch scope")
		return
	}
	if flowIP(batch.TransportSource) != flowIP(str(e, "sourceAddress")) || flowIP(batch.Datagram.AgentAddress) != flowIP(str(e, "agentAddress")) || batch.Datagram.SubAgentID != uint32(number(e, "subAgentId")) {
		problem(w, 422, "transport/agent/sub-agent identity does not match registered exporter")
		return
	}
	if batch.MappingRevision != mapping.Revision {
		problem(w, 409, "sFlow mapping changed; discard buffered packets and fetch bindings before receiving again")
		return
	}
	if batch.ReceivedAt.Before(mapping.NotBefore) {
		problem(w, 409, "packet predates this mapping; discard old samples before acquiring new bindings")
		return
	}
	dg, _ := json.Marshal(batch.Datagram)
	sum := digest(string(dg))
	if batch.DatagramDigest != "" && batch.DatagramDigest != sum {
		problem(w, 422, "safe datagram digest mismatch")
		return
	}
	if batch.RequestDeliveries {
		if !deliveryNoncePattern.MatchString(batch.RequestNonce) || batch.DatagramDigest != sum {
			problem(w, 422, "forwarding needs a unique request nonce and exact safe datagram digest")
			return
		}
		a.store.mu.RLock()
		allowed := a.canSFlowForward(&a.store.state, user, session, batch.SwitchID)
		a.store.mu.RUnlock()
		if !allowed {
			problem(w, 403, "sflow:forward collector scope required")
			return
		}
	}
	id := digest(batch.ExporterID + "\x00" + batch.MappingRevision + "\x00" + strconv.FormatUint(uint64(batch.Datagram.Sequence), 10) + "\x00" + strconv.FormatUint(uint64(batch.Datagram.UptimeMS), 10) + "\x00" + sum)
	ready := sflow.AttributedBatch{ID: id, ReceivedAt: batch.ReceivedAt, ExporterID: batch.ExporterID, SwitchID: batch.SwitchID, InfrastructureID: mapping.InfrastructureID, MappingRevision: mapping.Revision, Sequence: batch.Datagram.Sequence, UptimeMS: batch.Datagram.UptimeMS, DatagramDigest: sum, Quality: batch.Quality, Flows: []sflow.AttributedFlow{}}
	unattributed := 0
	for _, flow := range batch.Datagram.Flows {
		mapped := mapping.attribute(flow)
		if mapped.UnattributedReason != "" {
			unattributed++
		}
		ready.Flows = append(ready.Flows, mapped)
	}
	if err := a.ensureObservationKey(); err != nil {
		problem(w, 500, "could not bind observation history key")
		return
	}
	classification, err := a.sflow.IngestWithResult(ready)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	accepted := 0
	if classification.AcceptFlows {
		accepted = len(ready.Flows)
	}
	response := Record{"ok": true, "acceptedFlows": accepted, "unattributedFlows": unattributed, "classification": classification, "memberForwarding": false}
	if batch.RequestDeliveries && classification.AcceptFlows {
		grant, err := a.authorizeSFlowDelivery(user, session, batch, ready.Flows)
		if err != nil {
			response["deliveryNotice"] = "observations saved, but forwarding authorization is unavailable"
		} else {
			response["deliveryEnvelope"] = grant
			response["memberForwarding"] = len(grant.Deliveries) > 0
		}
	}
	respond(w, 200, response)
}
func (a *App) sflowQuery(w http.ResponseWriter, r *http.Request, s *State, user Record, session Session, scope []string) {
	values := r.URL.Query()
	now := time.Now().UTC()
	q := sflow.Query{From: now.Add(-time.Hour), To: now, Resolution: values.Get("resolution"), ExporterID: values.Get("exporterId"), VLANID: values.Get("vlanId"), MemberID: values.Get("memberId"), SwitchIDs: scope, Limit: 1000}
	if q.Resolution == "" {
		q.Resolution = "5m"
	}
	if raw := values.Get("family"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			problem(w, 400, "invalid address family")
			return
		}
		q.Family = n
	}
	for _, field := range []struct {
		name string
		dest *time.Time
	}{{"from", &q.From}, {"to", &q.To}} {
		if raw := values.Get(field.name); raw != "" {
			at, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				problem(w, 400, "query times must be RFC3339")
				return
			}
			*field.dest = at.UTC()
		}
	}
	if q.To.After(now.Add(30 * time.Second)) {
		problem(w, 400, "sFlow query cannot select future time")
		return
	}
	if str(user, "role") == "member" {
		if q.ExporterID != "" {
			problem(w, 403, "member reports cannot select private exporter identities")
			return
		}
		if q.MemberID != "" && q.MemberID != str(user, "memberId") {
			problem(w, 403, "only this member's captured history is available")
			return
		}
		q.MemberID = str(user, "memberId")
	}
	if q.MemberID != "" {
		if str(user, "role") != "member" && len(scope) > 0 {
			visible, err := a.sflowVisibleMembers(s, scope)
			if err != nil {
				problem(w, 503, "sFlow directory unavailable")
				return
			}
			if !visible[q.MemberID] {
				problem(w, 404, "member not found in this observation scope")
				return
			}
		}
		q.MemberRevision = a.trafficMemberRevision(s, q.MemberID)
		if q.MemberRevision == "" {
			problem(w, 404, "member not found")
			return
		}
	}
	if q.ExporterID != "" {
		e := find(s, sflowExporterKind, q.ExporterID)
		if e == nil || !flowSwitchAllowed(scope, str(e, "switchId")) {
			problem(w, 404, "exporter not found")
			return
		}
	}
	result, err := a.sflow.Query(q)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	latest := a.store.Snapshot()
	current, currentScope, err := a.sflowActor(&latest, user, session, false)
	if err != nil || str(current, "role") != str(user, "role") || patchHash(scope) != patchHash(currentScope) {
		problem(w, 403, "sFlow history access changed")
		return
	}
	if str(user, "role") == "member" && (str(current, "memberId") != q.MemberID || a.trafficMemberRevision(&latest, q.MemberID) != q.MemberRevision) {
		problem(w, 403, "member entity changed")
		return
	}
	data, _ := json.Marshal(result)
	out := Record{}
	_ = json.Unmarshal(data, &out)
	items := []Record{}
	for _, row := range result.Items {
		items = append(items, a.sflowBucketView(&latest, row, current))
	}
	out["items"] = items
	out["alignmentNotice"] = "按完整5分钟或UTC日收包窗口汇总，实际边界以from/to为准；质量统计按整UTC日，不是精确网络丢包率。"
	respond(w, 200, out)
}
func (a *App) sflowBucketView(s *State, row sflow.Bucket, user Record) Record {
	data, _ := json.Marshal(row)
	out := Record{}
	_ = json.Unmarshal(data, &out)
	member := str(user, "role") == "member"
	self := str(user, "memberId")
	for _, side := range []struct{ prefix, id, revision, connection string }{{"source", row.SourceMemberID, row.SourceMemberRevision, row.SourceConnectionID}, {"destination", row.DestinationMemberID, row.DestinationMemberRevision, row.DestinationConnectionID}} {
		known := side.id != "" && side.revision != "" && a.trafficMemberRevision(s, side.id) == side.revision
		visible := !member || side.id == self || known && sharedPeeringVLANFor(s, self, side.id, row.VLANID, true)
		label := "未归属"
		if side.id != "" {
			label = "历史成员 · " + side.id
			if known {
				label = str(find(s, "members", side.id), "name")
			}
		}
		if !visible {
			alias := "peer-" + a.coreHMAC("flow-peer", Record{"viewer": self, "peer": side.id, "revision": side.revision})[:16]
			out[side.prefix+"MemberId"] = alias
			label = "受限对端 · " + alias[len("peer-"):]
			delete(out, side.prefix+"ConnectionId")
		}
		out[side.prefix+"MemberLabel"] = label
		connectionLabel := "接入未唯一确定"
		if !visible {
			connectionLabel = "接入信息受限"
		}
		if side.connection != "" && visible {
			connectionLabel = "历史接入"
			c := find(s, "connections", side.connection)
			if c != nil && (!member || str(c, "memberId") == self) {
				connectionLabel = str(c, "name")
			}
		}
		out[side.prefix+"ConnectionLabel"] = connectionLabel
		if member {
			delete(out, side.prefix+"MemberRevision")
			if side.id != self {
				delete(out, side.prefix+"ConnectionId")
			}
		}
	}
	if member {
		delete(out, "exporterId")
		delete(out, "switchId")
		delete(out, "infrastructureId")
		out["mappingRevision"] = a.coreHMAC("flow-member-mapping", Record{"member": self, "mapping": row.MappingRevision})
	}
	return out
}
