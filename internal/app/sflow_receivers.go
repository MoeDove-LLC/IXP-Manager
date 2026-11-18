package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
)

type SFlowReceiver struct {
	Name             string         `json:"name"`
	Spec             sflow.Receiver `json:"spec"`
	MemberRevision   string         `json:"memberRevision"`
	Status           string         `json:"status"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
	ApprovedAt       time.Time      `json:"approvedAt,omitempty"`
	ApprovedBy       string         `json:"approvedBy,omitempty"`
	ApprovedChecksum string         `json:"approvedChecksum,omitempty"`
}

type SFlowReceiverImport struct {
	Name string         `json:"name"`
	Spec sflow.Receiver `json:"spec"`
}

func exportSFlowReceivers(s *State) []SFlowReceiverImport {
	out := []SFlowReceiverImport{}
	for _, r := range s.SFlowReceivers {
		spec := r.Spec
		spec.Revision = ""
		out = append(out, SFlowReceiverImport{Name: r.Name, Spec: spec})
	}
	return out
}
func (a *App) importSFlowReceivers(s *State, items []SFlowReceiverImport) error {
	if len(items)+len(s.SFlowReceivers) > 512 {
		return errors.New("receiver import exceeds limit")
	}
	for _, item := range items {
		spec := item.Spec
		if !identifier.MatchString(spec.ID) || receiverByID(s, spec.ID) != nil {
			return errors.New("invalid or duplicate receiver import ID")
		}
		spec.Revision = digest(token(32))
		if err := validateReceiverSpec(s, spec, false); err != nil {
			return err
		}
		now := time.Now().UTC()
		status := "disabled"
		if spec.Enabled {
			status = "draft"
		}
		s.SFlowReceivers = append(s.SFlowReceivers, SFlowReceiver{Name: strings.TrimSpace(item.Name), Spec: spec, MemberRevision: a.trafficMemberRevision(s, spec.MemberID), Status: status, CreatedAt: now, UpdatedAt: now})
	}
	if len(items) > 0 {
		if err := a.bindObservationKey(s); err != nil {
			return err
		}
	}
	return validateSFlowReceivers(s)
}

func receiverByID(s *State, id string) *SFlowReceiver {
	for i := range s.SFlowReceivers {
		if s.SFlowReceivers[i].Spec.ID == id {
			return &s.SFlowReceivers[i]
		}
	}
	return nil
}
func receiverWritable(user Record) bool {
	return str(user, "role") == "admin" || str(user, "role") == "operator" || str(user, "role") == "member" && str(user, "memberRole") == "member-admin"
}
func (a *App) receiverVisible(s *State, r SFlowReceiver, user Record, scope []string) bool {
	if str(user, "role") == "member" {
		return r.Spec.MemberID == str(user, "memberId") && r.MemberRevision == a.trafficMemberRevision(s, str(user, "memberId"))
	}
	if len(scope) == 0 {
		return true
	}
	for _, id := range r.Spec.ConnectionIDs {
		c := find(s, "connections", id)
		if c == nil {
			return false
		}
		for _, pid := range monitoringConnectionPorts(c) {
			if !contains(scope, str(find(s, "ports", pid), "switchId")) {
				return false
			}
		}
	}
	return true
}
func validateReceiverSpec(s *State, r sflow.Receiver, requireActive bool) error {
	if err := sflow.ValidateReceiver(r); err != nil {
		return err
	}
	m := find(s, "members", r.MemberID)
	if m == nil {
		return errors.New("receiver member does not exist")
	}
	if requireActive && str(m, "status") != "active" {
		return errors.New("receiver requires an active member")
	}
	vlans := map[string]bool{}
	for _, id := range r.ConnectionIDs {
		c := find(s, "connections", id)
		if c == nil || str(c, "memberId") != r.MemberID {
			return errors.New("receiver connection is not owned by this member")
		}
		if requireActive && str(c, "status") != "active" {
			return errors.New("receiver connection must be active")
		}
		if !contains(r.VLANIDs, str(c, "vlanId")) {
			return errors.New("receiver VLAN scope must include each selected connection")
		}
		vlans[str(c, "vlanId")] = true
	}
	for _, id := range r.VLANIDs {
		if !vlans[id] {
			return errors.New("receiver VLAN has no selected member connection")
		}
	}
	return nil
}
func validateSFlowReceivers(s *State) error {
	if len(s.SFlowReceivers) > 512 {
		return errors.New("receiver quota exceeded")
	}
	ids := map[string]bool{}
	for _, r := range s.SFlowReceivers {
		if err := sflow.ValidateReceiver(r.Spec); err != nil {
			return err
		}
		if ids[r.Spec.ID] || !externalDigest(r.MemberRevision) || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) || strings.TrimSpace(r.Name) == "" || len(r.Name) > 200 || strings.ContainsAny(r.Name, "\x00\r\n") {
			return errors.New("invalid receiver metadata")
		}
		ids[r.Spec.ID] = true
		if r.Status != "draft" && r.Status != "approved" && r.Status != "disabled" {
			return errors.New("invalid receiver lifecycle")
		}
		if r.Spec.Enabled == (r.Status == "disabled") {
			return errors.New("receiver desired state and lifecycle disagree")
		}
		if r.Status == "approved" && (!externalDigest(r.ApprovedChecksum) || r.ApprovedAt.IsZero() || r.ApprovedBy == "") {
			return errors.New("receiver approval metadata missing")
		}
		// Disabled historical plans may retain retired references. An active
		// plan with changed inventory becomes stale rather than blocking repair.
	}
	return nil
}
func (a *App) receiverPlan(s *State, r SFlowReceiver, now time.Time) (sflow.ReceiverPlan, string, error) {
	if !r.Spec.Enabled {
		if err := sflow.ValidateReceiver(r.Spec); err != nil {
			return sflow.ReceiverPlan{}, "", err
		}
		plan := sflow.ReceiverPlan{Version: 1, Receiver: r.Spec, Bindings: []sflow.FilterBinding{}, GeneratedAt: now, ExpiresAt: now.Add(time.Minute), RequiresCurrentOwnershipCheck: true}
		spec := r.Spec
		spec.Revision = ""
		return plan, a.coreHMAC("sflow-receiver-disabled", Record{"name": r.Name, "spec": spec, "memberRevision": r.MemberRevision}), nil
	}
	if r.MemberRevision != a.trafficMemberRevision(s, r.Spec.MemberID) {
		return sflow.ReceiverPlan{}, "", errors.New("receiver member entity changed; create a new receiver")
	}
	if err := validateReceiverSpec(s, r.Spec, r.Spec.Enabled); err != nil {
		return sflow.ReceiverPlan{}, "", err
	}
	bindings := []sflow.FilterBinding{}
	dependencies := []any{find(s, "members", r.Spec.MemberID)}
	for _, id := range r.Spec.ConnectionIDs {
		c := find(s, "connections", id)
		dependencies = append(dependencies, c)
		for _, pid := range monitoringConnectionPorts(c) {
			p := find(s, "ports", pid)
			dependencies = append(dependencies, p)
			if number(p, "snmpIfIndex") == 0 || number(p, "snmpIfType") != 6 {
				continue
			}
			for _, e := range s.Resources[sflowExporterKind] {
				if e["enabled"] != true || str(e, "switchId") != str(p, "switchId") || !contains(stringList(e["vlanIds"]), str(c, "vlanId")) {
					continue
				}
				mapping, err := a.flowMapping(s, e)
				if err != nil {
					return sflow.ReceiverPlan{}, "", err
				}
				dependencies = append(dependencies, e)
				for _, mac := range s.Resources["mac-addresses"] {
					if str(mac, "connectionId") != id {
						continue
					}
					dependencies = append(dependencies, mac)
					for _, direction := range []string{"source", "destination"} {
						bindings = append(bindings, sflow.FilterBinding{ExporterID: str(e, "id"), SwitchID: str(e, "switchId"), InfrastructureID: mapping.InfrastructureID, VLANID: str(c, "vlanId"), VLANTag: uint16(number(find(s, "vlans", str(c, "vlanId")), "tag")), AgentAddress: flowIP(str(e, "agentAddress")), SubAgentID: uint32(number(e, "subAgentId")), IfIndex: uint32(number(p, "snmpIfIndex")), MAC: canonicalDeviceMAC(str(mac, "address")), Direction: direction, ConnectionID: id, MemberID: r.Spec.MemberID, MappingRevision: mapping.Revision})
					}
				}
			}
		}
	}
	var plan sflow.ReceiverPlan
	var err error
	plan, err = sflow.BuildReceiverPlan(r.Spec, bindings, now)
	if err != nil {
		return plan, "", err
	}
	// Global mapping epochs also include unrelated tenants. Approval freezes
	// this member's own dependencies; every delivered packet still needs the
	// current global mapping and a new short authorization.
	proofBindings := append([]sflow.FilterBinding(nil), bindings...)
	for i := range proofBindings {
		proofBindings[i].MappingRevision = ""
	}
	spec := r.Spec
	spec.Revision = ""
	checksum := a.coreHMAC("sflow-receiver-review", Record{"name": r.Name, "spec": spec, "memberRevision": r.MemberRevision, "dependencies": dependencies, "bindings": proofBindings})
	return plan, checksum, nil
}
func (a *App) receiverView(s *State, r SFlowReceiver, user Record, session Session) Record {
	status := r.Status
	reason := ""
	if r.Spec.Enabled {
		_, sum, err := a.receiverPlan(s, r, time.Now().UTC())
		if err != nil {
			status = "stale"
			reason = err.Error()
		} else if status == "approved" && sum != r.ApprovedChecksum {
			status = "stale"
			reason = "receiver scope or inventory changed; review again"
		}
	}
	label := "历史成员"
	if r.MemberRevision == a.trafficMemberRevision(s, r.Spec.MemberID) {
		label = str(find(s, "members", r.Spec.MemberID), "name")
	}
	editable := !session.API && receiverWritable(user)
	out := Record{"id": r.Spec.ID, "name": r.Name, "spec": r.Spec, "memberLabel": label, "revision": r.Spec.Revision, "status": status, "createdAt": r.CreatedAt, "updatedAt": r.UpdatedAt, "approvedAt": optionalTime(r.ApprovedAt), "approvedBy": r.ApprovedBy, "canEdit": editable, "canApprove": !session.API && str(user, "role") == "admin" && r.Spec.Enabled && status != "approved", "canDelete": editable && !r.Spec.Enabled, "liveForwarding": true, "deliveryNotice": "审批和逐包授权不证明外部目标已收到；本机仍需显式允许。停用立即停止签发新授权，已有不超过5秒的授权和在途UDP不能撤回。", "staleReason": reason}
	for key, value := range a.deliveryView(r.Spec.ID, r.Spec.Revision) {
		out[key] = value
	}
	return out
}
func (a *App) sflowReceiverRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	if r.URL.Path != "/api/sflow/receivers" && !strings.HasPrefix(r.URL.Path, "/api/sflow/receivers/") {
		return false
	}
	s := a.store.Snapshot()
	actor, scope, err := a.sflowActor(&s, user, session, false)
	if err != nil {
		problem(w, 403, "receiver access denied")
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/sflow/receivers"), "/")
	id, action := "", ""
	if len(parts) > 1 {
		id = parts[1]
	}
	if len(parts) > 2 {
		action = parts[2]
	}
	if len(parts) > 3 {
		problem(w, 404, "receiver route not found")
		return true
	}
	current := receiverByID(&s, id)
	if id != "" && (current == nil || !a.receiverVisible(&s, *current, actor, scope)) {
		problem(w, 404, "receiver not found")
		return true
	}
	if r.Method == "GET" {
		if id == "" {
			items := []Record{}
			for _, v := range s.SFlowReceivers {
				if a.receiverVisible(&s, v, actor, scope) {
					items = append(items, a.receiverView(&s, v, actor, session))
				}
			}
			respond(w, 200, Record{"items": items})
			return true
		}
		if action == "" {
			respond(w, 200, a.receiverView(&s, *current, actor, session))
			return true
		}
		if action == "plan" || action == "pretag" {
			plan, sum, e := a.receiverPlan(&s, *current, time.Now().UTC())
			if e != nil || current.Spec.Enabled && (current.Status != "approved" || current.ApprovedChecksum != sum) {
				problem(w, 409, "receiver plan is not currently approved")
				return true
			}
			data, e := json.MarshalIndent(plan, "", "  ")
			if action == "pretag" {
				var text string
				if !plan.Receiver.Enabled {
					text = "! Disabled receiver; no forwarding rules.\n"
				} else {
					text, e = sflow.RenderPretag(plan)
				}
				data = []byte(text)
			}
			if e != nil {
				problem(w, 422, "receiver plan failed validation")
				return true
			}
			w.Header().Set("X-SFlow-Receiver-ID", id)
			w.Header().Set("X-SFlow-Receiver-Revision", current.Spec.Revision)
			w.Header().Set("X-Config-SHA256", digest(string(data)))
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if action == "pretag" {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			}
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="receiver-%s.%s"`, id, map[bool]string{true: "map", false: "json"}[action == "pretag"]))
			w.Write(data)
			return true
		}
		problem(w, 404, "receiver route not found")
		return true
	}
	if session.API || !receiverWritable(actor) {
		problem(w, 403, "receiver changes require a staff or member-administrator browser session")
		return true
	}
	if action == "review" && r.Method == "POST" {
		var body struct {
			ExpectedRevision string `json:"expectedRevision"`
		}
		if !decode(w, r, &body) {
			return true
		}
		if body.ExpectedRevision != current.Spec.Revision {
			problem(w, 409, "receiver changed")
			return true
		}
		plan, sum, e := a.receiverPlan(&s, *current, time.Now().UTC())
		if e != nil {
			problem(w, 422, e.Error())
			return true
		}
		respond(w, 200, Record{"receiver": a.receiverView(&s, *current, actor, session), "reviewChecksum": sum, "bindings": plan.Bindings, "disclosure": []string{"接收器将获得其获准连接的以太网/IP/传输层包头，包括IP地址与端口；应用载荷不会转发。", "采样估算与本地socket提交不证明目标收到；每包重新授权，本机另有限定目的网段、端口与限额。"}, "notices": []string{"静态plan/pretag不能授予实时转发权限。"}})
		return true
	}
	if action == "approve" && r.Method == "POST" {
		a.approveSFlowReceiver(w, r, actor, session, id)
		return true
	}
	var body struct {
		Name             string         `json:"name"`
		Spec             sflow.Receiver `json:"spec"`
		ExpectedRevision string         `json:"expectedRevision"`
		Reason           string         `json:"reason"`
	}
	if !decode(w, r, &body) {
		return true
	}
	create := id == "" && r.Method == "POST"
	update := id != "" && action == "" && r.Method == "PUT"
	disable := id != "" && action == "disable" && r.Method == "POST"
	remove := id != "" && action == "" && r.Method == "DELETE"
	if !create && !update && !disable && !remove {
		problem(w, 405, "method not allowed")
		return true
	}
	code := 422
	var result SFlowReceiver
	err = a.store.Update(func(s *State) error {
		actor, _, e := a.sflowActor(s, user, session, false)
		if e != nil || !receiverWritable(actor) {
			code = 403
			return errors.New("receiver authority changed")
		}
		old := receiverByID(s, id)
		if !create && (old == nil || !a.receiverVisible(s, *old, actor, nil)) {
			code = 404
			return errors.New("receiver not found")
		}
		if !create && (body.ExpectedRevision == "" || body.ExpectedRevision != old.Spec.Revision) {
			code = 409
			return errors.New("receiver changed; inspect its current revision")
		}
		if remove {
			if old.Spec.Enabled {
				return errors.New("disable the receiver before deleting it")
			}
			if strings.TrimSpace(body.Reason) == "" || len(body.Reason) > 1000 {
				return errors.New("deletion reason required")
			}
			for i := range s.SFlowReceivers {
				if s.SFlowReceivers[i].Spec.ID == id {
					s.SFlowReceivers = append(s.SFlowReceivers[:i], s.SFlowReceivers[i+1:]...)
					break
				}
			}
			audit(s, str(actor, "id"), "sflow-receiver-delete", "sflow-receivers", id)
			return nil
		}
		now := time.Now().UTC()
		if create {
			if len(s.SFlowReceivers) >= 512 {
				return errors.New("receiver quota reached")
			}
			result = SFlowReceiver{CreatedAt: now}
			id = token(16)
		} else {
			result = *old
		}
		if disable {
			result.Spec.Enabled = false
		} else {
			if body.Spec.ID != "" && body.Spec.ID != id {
				return errors.New("receiver identity is immutable")
			}
			if body.Spec.Revision != "" && (create || body.Spec.Revision != old.Spec.Revision) {
				return errors.New("receiver revision is read-only")
			}
			if str(actor, "role") == "member" {
				if body.Spec.MemberID != "" && body.Spec.MemberID != str(actor, "memberId") {
					code = 403
					return errors.New("receiver must belong to current member")
				}
				body.Spec.MemberID = str(actor, "memberId")
			}
			result.Name = strings.TrimSpace(body.Name)
			result.Spec = body.Spec
			result.Spec.ID = id
			result.MemberRevision = a.trafficMemberRevision(s, result.Spec.MemberID)
		}
		result.Spec.Revision = digest(token(32))
		result.UpdatedAt = now
		result.Status = "disabled"
		if result.Spec.Enabled {
			result.Status = "draft"
		}
		result.ApprovedChecksum = ""
		result.ApprovedBy = ""
		result.ApprovedAt = time.Time{}
		if disable {
			if err := sflow.ValidateReceiver(result.Spec); err != nil {
				return err
			}
		} else if err := validateReceiverSpec(s, result.Spec, result.Spec.Enabled); err != nil {
			return err
		}
		if err := a.bindObservationKey(s); err != nil {
			return err
		}
		if create {
			s.SFlowReceivers = append(s.SFlowReceivers, result)
		} else {
			*old = result
		}
		if err := validateSFlowReceivers(s); err != nil {
			return err
		}
		audit(s, str(actor, "id"), map[bool]string{true: "sflow-receiver-disable", false: "sflow-receiver-save"}[disable], "sflow-receivers", id)
		return nil
	})
	if err != nil {
		problem(w, code, err.Error())
		return true
	}
	if remove {
		respond(w, 200, Record{"ok": true})
		return true
	}
	next := a.store.Snapshot()
	respond(w, map[bool]int{true: 201, false: 200}[create], a.receiverView(&next, result, actor, session))
	return true
}
func (a *App) approveSFlowReceiver(w http.ResponseWriter, r *http.Request, user Record, session Session, id string) {
	if str(user, "role") != "admin" || session.API {
		problem(w, 403, "administrator approval required")
		return
	}
	if a.limited(r) {
		problem(w, 429, "too many authentication attempts")
		return
	}
	var body struct {
		ExpectedRevision string `json:"expectedRevision"`
		ReviewChecksum   string `json:"reviewChecksum"`
		Acknowledge      bool   `json:"acknowledgeHeaderData"`
		CurrentPassword  string `json:"currentPassword"`
		Code             string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	code := 403
	err := a.reauth(str(user, "id"), body.CurrentPassword, body.Code, func(s *State, _ *SecurityUser) error {
		actor, _, err := a.sflowActor(s, user, session, false)
		if err != nil || str(actor, "role") != "admin" {
			return errors.New("approval authority changed")
		}
		code = 409
		current := receiverByID(s, id)
		if current == nil || current.Spec.Revision != body.ExpectedRevision || !current.Spec.Enabled {
			return errors.New("receiver revision or desired state changed")
		}
		if !body.Acknowledge {
			return errors.New("explicit header-data disclosure acknowledgement required")
		}
		_, sum, err := a.receiverPlan(s, *current, time.Now().UTC())
		if err != nil {
			return err
		}
		if body.ReviewChecksum != sum {
			return errors.New("receiver destination or binding changed since review")
		}
		current.Status = "approved"
		current.ApprovedChecksum = sum
		current.ApprovedAt = time.Now().UTC()
		current.ApprovedBy = str(actor, "id")
		current.UpdatedAt = current.ApprovedAt
		current.Spec.Revision = digest(token(32))
		auditDetails(s, str(actor, "id"), "sflow-receiver-approve", "sflow-receivers", id, Record{"reviewChecksum": sum, "headerDataDisclosure": true})
		return nil
	})
	if err != nil {
		problem(w, code, err.Error())
		return
	}
	s := a.store.Snapshot()
	respond(w, 200, a.receiverView(&s, *receiverByID(&s, id), user, session))
}

func sortedReceiverIDs(s *State) []string {
	ids := []string{}
	for _, r := range s.SFlowReceivers {
		ids = append(ids, r.Spec.ID)
	}
	sort.Strings(ids)
	return ids
}
