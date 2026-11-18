package app

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
	"github.com/moedove/ixp-manager/internal/telemetry"
)

type macLearningCandidate struct {
	ID                 string   `json:"id"`
	MAC                string   `json:"mac"`
	IfIndex            uint32   `json:"ifIndex"`
	BridgePort         uint32   `json:"bridgePort"`
	FDBID              uint32   `json:"fdbId"`
	VLANTags           []uint16 `json:"vlanTags"`
	PortID             string   `json:"portId,omitempty"`
	PortLabel          string   `json:"portLabel,omitempty"`
	VLANID             string   `json:"vlanId,omitempty"`
	VLANLabel          string   `json:"vlanLabel,omitempty"`
	ConnectionID       string   `json:"connectionId,omitempty"`
	ConnectionLabel    string   `json:"connectionLabel,omitempty"`
	MemberID           string   `json:"memberId,omitempty"`
	MemberLabel        string   `json:"memberLabel,omitempty"`
	RegistrationStatus string   `json:"registrationStatus"`
	Reasons            []string `json:"reasons"`
	RegisteredIDs      []string `json:"registeredIds"`
	CanReview          bool     `json:"canReview"`
}

func macLearningActor(s *State, user Record, session Session, switchID string, write bool) (Record, error) {
	actor, err := lookingGlassActor(s, user, session)
	if err != nil || !contains([]string{"admin", "operator", "viewer"}, str(actor, "role")) {
		return nil, errors.New("MAC learning requires current staff access")
	}
	if write && (session.API || str(actor, "role") == "viewer") {
		return nil, errors.New("MAC registration requires a staff writer browser session")
	}
	if session.API {
		allowed := false
		for _, t := range s.Security[str(actor, "id")].Tokens {
			allowed = allowed || t.ID == session.TokenID && time.Now().Before(t.ExpiresAt) && contains(t.Scopes, "devices:read") && contains(t.SwitchIDs, switchID)
		}
		if !allowed {
			return nil, errors.New("MAC learning is outside the token switch scope")
		}
	}
	return actor, nil
}

func (a *App) currentMACLearningActor(s *State, user Record, session Session, switchID string, write bool) (Record, error) {
	if _, err := a.atlasActor(s, user, session, "admin", "operator", "viewer"); err != nil {
		return nil, err
	}
	return macLearningActor(s, user, session, switchID, write)
}

func learnedMACUsable(value string) bool {
	mac, err := net.ParseMAC(value)
	return err == nil && len(mac) == 6 && mac[0]&1 == 0 && mac.String() != "00:00:00:00:00:00"
}

// FDB evidence is a device observation, never authoritative registration. Only
// a unique, current physical port/VLAN/connection can become a review candidate.
func macLearningProjection(s *State, o deviceObservation, writable bool, now time.Time) ([]Record, []macLearningCandidate) {
	registered := []Record{}
	rows := []macLearningCandidate{}
	ports := map[uint32][]Record{}
	portIDs := map[string]bool{}
	for _, p := range s.Resources["ports"] {
		if str(p, "switchId") == o.SwitchID {
			portIDs[str(p, "id")] = true
			if number(p, "snmpIfIndex") > 0 {
				ports[uint32(number(p, "snmpIfIndex"))] = append(ports[uint32(number(p, "snmpIfIndex"))], p)
			}
		}
	}
	connections := map[string]Record{}
	byPortVLAN := map[string][]Record{}
	for _, c := range s.Resources["connections"] {
		connections[str(c, "id")] = c
		v := find(s, "vlans", str(c, "vlanId"))
		for _, pid := range monitoringConnectionPorts(c) {
			if portIDs[pid] {
				key := pid + "/" + strconv.Itoa(int(number(v, "tag")))
				byPortVLAN[key] = append(byPortVLAN[key], c)
			}
		}
	}
	byMAC := map[string][]Record{}
	for _, mac := range s.Resources["mac-addresses"] {
		c := connections[str(mac, "connectionId")]
		byMAC[str(c, "vlanId")+"/"+canonicalDeviceMAC(str(mac, "address"))] = append(byMAC[str(c, "vlanId")+"/"+canonicalDeviceMAC(str(mac, "address"))], mac)
		for _, pid := range monitoringConnectionPorts(c) {
			if portIDs[pid] {
				registered = append(registered, Record{"id": mac["id"], "name": mac["name"], "address": canonicalDeviceMAC(str(mac, "address")), "connectionId": c["id"], "connectionLabel": c["name"], "memberId": c["memberId"], "memberLabel": str(find(s, "members", str(c, "memberId")), "name"), "vlanId": c["vlanId"], "vlanLabel": str(find(s, "vlans", str(c, "vlanId")), "name"), "observed": false})
				break
			}
		}
	}
	if o.LastSuccess == nil {
		return registered, rows
	}
	snapshot := o.LastSuccess
	interfaces := map[uint32][]devicesnmp.Interface{}
	for _, iface := range snapshot.Interfaces {
		interfaces[iface.IfIndex] = append(interfaces[iface.IfIndex], iface)
	}
	currentPorts := map[string]bool{}
	for index, matches := range ports {
		if len(matches) != 1 || len(interfaces[index]) != 1 {
			continue
		}
		port, iface := matches[0], interfaces[index][0]
		id := str(port, "id")
		currentPorts[id] = iface.PortID == id && iface.OwnershipRevision == deviceBindingRevision(s, port) && deviceIdentityMatches(port, iface, *snapshot) && !snapshot.ObservedAt.Before(deviceOwnershipSince(s, port))
	}
	for index, entry := range snapshot.FDB {
		row := macLearningCandidate{ID: digest(strconv.Itoa(index) + "/" + patchHash(entry)), MAC: canonicalDeviceMAC(entry.MAC), IfIndex: entry.IfIndex, BridgePort: entry.BridgePort, FDBID: entry.FDBID, VLANTags: append([]uint16{}, entry.VLANIDs...), RegistrationStatus: "candidate", Reasons: []string{}, RegisteredIDs: []string{}}
		block := func(status, reason string) {
			if row.RegistrationStatus == "candidate" {
				row.RegistrationStatus = status
			}
			row.Reasons = append(row.Reasons, reason)
		}
		if freshness(snapshot.ObservedAt, now) != "fresh" || o.ErrorCode != "" || snapshot.ObservedAt.After(now.Add(30*time.Second)) {
			block("stale", "观测已过期或最近采集失败，请重新采集。")
		}
		if entry.Status != "learned" || !learnedMACUsable(row.MAC) {
			block("ineligible", "仅动态学习到的有效单播 MAC 可申请登记。")
		}
		if len(entry.VLANIDs) != 1 {
			block("ambiguous", "FDB 未提供唯一 VLAN，不能推测地址所属网络。")
		}
		var port Record
		if len(ports[entry.IfIndex]) != 1 || entry.IfIndex == 0 {
			block("unmapped", "观测接口尚未映射为唯一库存端口。")
		} else {
			port = ports[entry.IfIndex][0]
			row.PortID, row.PortLabel = str(port, "id"), str(port, "name")
			if port["coreReserved"] == true || corePortBinding(s, row.PortID) != "" || number(port, "snmpIfType") != 6 || str(port, "status") != "active" {
				block("ineligible", "需要启用的成员物理端口，核心或聚合接口不能自动归属。")
			}
			if !currentPorts[row.PortID] {
				block("stale", "物理接口或归属已改变，请按当前绑定重新采集。")
			}
		}
		if port != nil && len(entry.VLANIDs) == 1 {
			matches := byPortVLAN[row.PortID+"/"+strconv.Itoa(int(entry.VLANIDs[0]))]
			if len(matches) != 1 {
				block("ambiguous", "该物理端口与 VLAN 没有唯一成员接入。")
			} else {
				c := matches[0]
				v, m := find(s, "vlans", str(c, "vlanId")), find(s, "members", str(c, "memberId"))
				row.ConnectionID, row.ConnectionLabel = str(c, "id"), str(c, "name")
				row.VLANID, row.VLANLabel = str(v, "id"), str(v, "name")
				row.MemberID, row.MemberLabel = str(m, "id"), str(m, "name")
				created, err := time.Parse(time.RFC3339Nano, str(m, "createdAt"))
				if str(c, "status") != "active" || str(m, "status") != "active" || err != nil || snapshot.ObservedAt.Before(created) || fabricID(v) != fabricID(find(s, "switches", o.SwitchID)) {
					block("ineligible", "成员、接入或网络身份已变化，请核对当前库存。")
				}
				for _, mac := range byMAC[row.VLANID+"/"+row.MAC] {
					row.RegisteredIDs = append(row.RegisteredIDs, str(mac, "id"))
					if str(mac, "connectionId") != row.ConnectionID {
						block("conflict", "同 VLAN 的 MAC 已登记到另一接入，不能覆盖或抢占。")
					}
				}
				if row.RegistrationStatus == "candidate" && len(row.RegisteredIDs) > 0 {
					row.RegistrationStatus = "registered"
				}
			}
		}
		rows = append(rows, row)
	}
	// A MAC appearing at distinct connections in the same snapshot cannot be
	// adopted merely by selecting the preferred row. LAG member ports belonging
	// to the same unique connection remain a single ownership candidate.
	seen := map[string][]int{}
	for i, row := range rows {
		for _, tag := range row.VLANTags {
			key := strconv.Itoa(int(tag)) + "/" + row.MAC
			seen[key] = append(seen[key], i)
		}
	}
	observedRegistrations := map[string]bool{}
	for i := range rows {
		row := &rows[i]
		if row.ConnectionID == "" || len(row.VLANTags) != 1 {
			continue
		}
		for _, other := range seen[strconv.Itoa(int(row.VLANTags[0]))+"/"+row.MAC] {
			if rows[other].IfIndex != row.IfIndex && (rows[other].ConnectionID == "" || rows[other].ConnectionID != row.ConnectionID) {
				row.RegistrationStatus = "conflict"
				row.Reasons = append(row.Reasons, "同一次观测在不同接入发现此 MAC，请先排查移动或冲突。")
				break
			}
		}
		row.CanReview = writable && row.RegistrationStatus == "candidate"
		if row.RegistrationStatus == "registered" {
			observedRegistrations[row.ConnectionID+"/"+row.MAC] = true
		}
	}
	for _, item := range registered {
		item["observed"] = observedRegistrations[str(item, "connectionId")+"/"+str(item, "address")]
	}
	sort.Slice(registered, func(i, j int) bool {
		return str(registered[i], "address")+str(registered[i], "id") < str(registered[j], "address")+str(registered[j], "id")
	})
	return registered, rows
}

func (a *App) macLearningChecksum(s *State, o deviceObservation, row macLearningCandidate, expires time.Time) string {
	return a.coreHMAC("mac-learning-review", Record{"observation": o.fileChecksum, "inventory": deviceBaseChecksum(s, o.SwitchID), "candidate": row, "member": find(s, "members", row.MemberID), "vlan": find(s, "vlans", row.VLANID), "expiresAt": expires})
}

func (a *App) macLearning(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	if r.URL.Path != "/api/devices/mac-learning" && !strings.HasPrefix(r.URL.Path, "/api/devices/mac-learning/") {
		return false
	}
	if r.Method != "GET" && r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return true
	}
	var body struct {
		SwitchID            string    `json:"switchId"`
		ObservationChecksum string    `json:"observationChecksum"`
		CandidateID         string    `json:"candidateId"`
		ReviewChecksum      string    `json:"reviewChecksum"`
		ExpiresAt           time.Time `json:"expiresAt"`
		Name                string    `json:"name"`
		Acknowledge         bool      `json:"acknowledge"`
	}
	read := r.Method == "GET" && r.URL.Path == "/api/devices/mac-learning"
	review := r.Method == "POST" && r.URL.Path == "/api/devices/mac-learning/review"
	apply := r.Method == "POST" && r.URL.Path == "/api/devices/mac-learning/apply"
	if !read && !review && !apply {
		problem(w, 405, "method not allowed")
		return true
	}
	if read {
		body.SwitchID = r.URL.Query().Get("switchId")
	} else if !decode(w, r, &body) {
		return true
	}
	if !identifier.MatchString(body.SwitchID) {
		problem(w, 400, "explicit switchId required")
		return true
	}
	a.deviceMu.Lock()
	defer a.deviceMu.Unlock()
	s := a.store.Snapshot()
	actor, err := a.currentMACLearningActor(&s, user, session, body.SwitchID, !read)
	if err != nil {
		problem(w, 403, err.Error())
		return true
	}
	sw := find(&s, "switches", body.SwitchID)
	if sw == nil {
		problem(w, 404, "switch not found")
		return true
	}
	o, err := a.latestDevice(body.SwitchID)
	if err != nil {
		problem(w, 503, "MAC observation integrity check failed")
		return true
	}
	now := time.Now().UTC()
	writable := !session.API && contains([]string{"admin", "operator"}, str(actor, "role"))
	registered, rows := macLearningProjection(&s, o, writable, now)
	if read {
		offset, limit := 0, 100
		for _, field := range []struct {
			name string
			dest *int
		}{{"offset", &offset}, {"limit", &limit}} {
			if raw := r.URL.Query().Get(field.name); raw != "" {
				value, e := strconv.Atoi(raw)
				if e != nil {
					problem(w, 400, "invalid pagination")
					return true
				}
				*field.dest = value
			}
		}
		if offset < 0 || offset > 20000 || limit < 1 || limit > 500 {
			problem(w, 400, "bounded pagination required")
			return true
		}
		if candidateID := r.URL.Query().Get("candidateId"); candidateID != "" {
			found := false
			for i, row := range rows {
				if row.ID == candidateID {
					offset = (i / limit) * limit
					found = true
					break
				}
			}
			if !found {
				problem(w, 404, "MAC observation changed or candidate no longer exists")
				return true
			}
		}
		total := len(rows)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		observation := Record{"checksum": o.fileChecksum, "lastAttemptAt": optionalTime(o.LastAttemptAt), "status": "unknown", "security": o.LastSecurity, "staleAfterSeconds": int(telemetry.StaleAfter.Seconds())}
		if o.LastSuccess != nil {
			observation["observedAt"] = o.LastSuccess.ObservedAt
			observation["status"] = freshness(o.LastSuccess.ObservedAt, now)
		}
		if o.ErrorCode != "" {
			observation["status"] = "error"
		}
		latest := a.store.Snapshot()
		if _, err := a.currentMACLearningActor(&latest, user, session, body.SwitchID, false); err != nil {
			problem(w, 403, "MAC learning access changed")
			return true
		}
		registeredTotal := len(registered)
		if len(registered) > 2000 {
			registered = registered[:2000]
		}
		respond(w, 200, Record{"switch": Record{"id": body.SwitchID, "name": sw["name"]}, "observation": observation, "registered": registered, "registeredTotal": registeredTotal, "registeredTruncated": registeredTotal > len(registered), "items": rows[offset:end], "totalRows": total, "offset": offset, "limit": limit, "truncated": end < total, "capabilities": Record{"canReview": writable, "canApply": writable}, "notices": []string{"学习记录是交换机报告的观测，未经人工核对不会加入已登记 MAC。", "本次未见不等于设备离线；FDB可能有保留、容量或采集范围限制。"}})
		return true
	}
	if o.fileChecksum == "" || body.ObservationChecksum != o.fileChecksum {
		problem(w, 409, "observation changed; refresh and review again")
		return true
	}
	var candidate *macLearningCandidate
	for i := range rows {
		if rows[i].ID == body.CandidateID {
			candidate = &rows[i]
			break
		}
	}
	if candidate == nil || !candidate.CanReview {
		problem(w, 409, "candidate is no longer eligible; refresh evidence")
		return true
	}
	if review {
		expires := now.Add(time.Minute)
		if bound := o.LastSuccess.ObservedAt.Add(telemetry.StaleAfter); bound.Before(expires) {
			expires = bound
		}
		respond(w, 200, Record{"candidate": candidate, "reviewChecksum": a.macLearningChecksum(&s, o, *candidate, expires), "expiresAt": expires, "consequences": []string{"将该 MAC 登记到显示的成员接入；不会改变设备配置或接入归属。", "登记会影响依赖 MAC 的采样归属，并使相关 sFlow 接收器旧审批失效，需要重新审阅。"}})
		return true
	}
	body.Name = strings.TrimSpace(body.Name)
	if !body.Acknowledge || body.Name == "" || len(body.Name) > 200 || strings.ContainsAny(body.Name, "\x00\r\n") {
		problem(w, 400, "registration name and explicit acknowledgement required")
		return true
	}
	if !now.Before(body.ExpiresAt) || body.ReviewChecksum != a.macLearningChecksum(&s, o, *candidate, body.ExpiresAt) {
		problem(w, 409, "review expired or changed; review current evidence again")
		return true
	}
	var record Record
	err = a.store.Update(func(current *State) error {
		actor, err := a.currentMACLearningActor(current, user, session, body.SwitchID, true)
		if err != nil {
			return err
		}
		_, candidates := macLearningProjection(current, o, true, time.Now().UTC())
		var row *macLearningCandidate
		for i := range candidates {
			if candidates[i].ID == body.CandidateID {
				row = &candidates[i]
				break
			}
		}
		if row == nil || !row.CanReview || !time.Now().Before(body.ExpiresAt) || body.ReviewChecksum != a.macLearningChecksum(current, o, *row, body.ExpiresAt) {
			return errors.New("current ownership or registration changed; refresh the review")
		}
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		record = Record{"id": token(16), "name": body.Name, "connectionId": row.ConnectionID, "address": row.MAC, "createdAt": stamp, "updatedAt": stamp}
		if err := validate(current, "mac-addresses", record); err != nil {
			return err
		}
		current.Resources["mac-addresses"] = append(current.Resources["mac-addresses"], record)
		auditDetails(current, str(actor, "id"), "register-learned-mac", "mac-addresses", str(record, "id"), Record{"switchId": body.SwitchID, "portId": row.PortID, "connectionId": row.ConnectionID, "vlanId": row.VLANID, "observationChecksum": o.fileChecksum, "observedAt": o.LastSuccess.ObservedAt, "reviewChecksum": body.ReviewChecksum})
		return nil
	})
	if err != nil {
		problem(w, 409, fmt.Sprintf("MAC registration rejected: %s", err))
		return true
	}
	respond(w, 201, Record{"registered": record, "receiverReviewRequired": true})
	return true
}
