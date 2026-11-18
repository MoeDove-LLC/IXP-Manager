package app

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

func init() {
	Schemas["members"]["peeringManagerEnabled"] = Field{Type: "boolean"}
	Schemas["members"]["peeringMatrixEnabled"] = Field{Type: "boolean"}
}

type PeeringEvent struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	At            time.Time `json:"at"`
	ActorID       string    `json:"actorId"`
	ActorMemberID string    `json:"actorMemberId,omitempty"`
	Message       string    `json:"message,omitempty"`
}
type PeeringRequest struct {
	FromASN        uint32         `json:"fromASN,omitempty"`
	ToASN          uint32         `json:"toASN,omitempty"`
	ID             string         `json:"id"`
	FromMemberID   string         `json:"fromMemberId"`
	FromMemberName string         `json:"fromMemberName"`
	ToMemberID     string         `json:"toMemberId"`
	ToMemberName   string         `json:"toMemberName"`
	VLANID         string         `json:"vlanId"`
	VLANName       string         `json:"vlanName"`
	Status         string         `json:"status"`
	Message        string         `json:"message,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	CreatedBy      string         `json:"createdBy"`
	Events         []PeeringEvent `json:"events"`
}

func activePeeringMember(s *State, id string) bool {
	m := find(s, "members", id)
	asn := number(m, "asn")
	return m != nil && str(m, "status") == "active" && asn > 0 && asn != 23456 && asn != 65535 && str(m, "type") != "associate" && str(m, "type") != "internal"
}
func peeringVLANVisible(s *State, vlan Record, matrix bool) bool {
	if vlan == nil || vlan["private"] == true {
		return false
	}
	if matrix {
		return vlan["peeringMatrix"] != false
	}
	return vlan["peeringManager"] != false
}
func activeMemberOnVLAN(s *State, memberID, vlanID string) bool {
	for _, c := range s.Resources["connections"] {
		if str(c, "memberId") == memberID && str(c, "vlanId") == vlanID && str(c, "status") == "active" {
			return true
		}
	}
	return false
}
func sharedPeeringVLANFor(s *State, from, to, vlanID string, matrix bool) bool {
	flag := "peeringManagerEnabled"
	if matrix {
		flag = "peeringMatrixEnabled"
	}
	return from != to && activePeeringMember(s, from) && activePeeringMember(s, to) && find(s, "members", from)[flag] != false && find(s, "members", to)[flag] != false && peeringVLANVisible(s, find(s, "vlans", vlanID), matrix) && activeMemberOnVLAN(s, from, vlanID) && activeMemberOnVLAN(s, to, vlanID)
}
func peeringTopologyValid(s *State, p PeeringRequest) bool {
	return p.FromASN > 0 && p.ToASN > 0 && number(find(s, "members", p.FromMemberID), "asn") == float64(p.FromASN) && number(find(s, "members", p.ToMemberID), "asn") == float64(p.ToASN) && sharedPeeringVLAN(s, p.FromMemberID, p.ToMemberID, p.VLANID)
}
func optionalASN(asn uint32) any {
	if asn == 0 {
		return nil
	}
	return asn
}
func sharedPeeringVLAN(s *State, from, to, vlanID string) bool {
	return sharedPeeringVLANFor(s, from, to, vlanID, false)
}
func sharedMatrixPartner(s *State, from, to string) bool {
	for _, vlan := range s.Resources["vlans"] {
		if sharedPeeringVLANFor(s, from, to, str(vlan, "id"), true) {
			return true
		}
	}
	return false
}
func sharedPeeringVLANs(s *State, from, to string) []Record {
	out := []Record{}
	for _, vlan := range s.Resources["vlans"] {
		id := str(vlan, "id")
		if sharedPeeringVLAN(s, from, to, id) {
			out = append(out, Record{"id": id, "name": str(vlan, "name"), "tag": number(vlan, "tag"), "fabricName": fabricName(s, fabricID(vlan))})
		}
	}
	return out
}
func memberPeeringAdmin(s *State, user Record, id string) bool {
	stored := find(s, "users", str(user, "id"))
	return str(stored, "role") == "member" && str(user, "role") == "member" && str(user, "memberId") == id && contains(membershipIDs(stored), id) && memberAdministrator(stored, id) && activePeeringMember(s, id)
}
func peeringCanDecide(s *State, p PeeringRequest, user Record) bool {
	return p.Status == "pending" && memberPeeringAdmin(s, user, p.ToMemberID) && peeringTopologyValid(s, p)
}
func peeringCanRevoke(s *State, p PeeringRequest, user Record) bool {
	if p.Status != "pending" && p.Status != "accepted" {
		return false
	}
	if str(user, "role") == "admin" && str(find(s, "users", str(user, "id")), "role") == "admin" {
		return true
	}
	if memberPeeringAdmin(s, user, p.FromMemberID) || memberPeeringAdmin(s, user, p.ToMemberID) {
		return true
	}
	return p.Status == "pending" && str(user, "role") == "member" && str(user, "memberId") == p.FromMemberID && str(user, "id") == p.CreatedBy && contains(membershipIDs(find(s, "users", str(user, "id"))), p.FromMemberID)
}
func peeringAddresses(s *State, memberID, vlanID string) []Record {
	out := []Record{}
	for _, c := range s.Resources["connections"] {
		if str(c, "memberId") == memberID && str(c, "vlanId") == vlanID && str(c, "status") == "active" {
			out = append(out, Record{"ipv4": str(c, "ipv4"), "ipv6": str(c, "ipv6")})
		}
	}
	return out
}
func peeringRequestView(s *State, p PeeringRequest, user Record) Record {
	events := []Record{}
	for _, e := range p.Events {
		entry := Record{"id": e.ID, "kind": e.Kind, "at": e.At, "actorMemberId": e.ActorMemberID, "message": e.Message}
		if str(user, "role") != "member" {
			entry["actorId"] = e.ActorID
		}
		events = append(events, entry)
	}
	valid := peeringTopologyValid(s, p)
	fromAddresses, toAddresses := []Record{}, []Record{}
	if valid {
		fromAddresses = peeringAddresses(s, p.FromMemberID, p.VLANID)
		toAddresses = peeringAddresses(s, p.ToMemberID, p.VLANID)
	}
	return Record{"id": p.ID, "fromASN": optionalASN(p.FromASN), "toASN": optionalASN(p.ToASN), "fromMemberId": p.FromMemberID, "fromMemberName": p.FromMemberName, "toMemberId": p.ToMemberID, "toMemberName": p.ToMemberName, "vlanId": p.VLANID, "vlanName": p.VLANName, "status": p.Status, "message": p.Message, "createdAt": p.CreatedAt, "events": events, "topologyValid": valid, "fromAddresses": fromAddresses, "toAddresses": toAddresses, "canDecide": peeringCanDecide(s, p, user), "canRevoke": peeringCanRevoke(s, p, user)}
}
func appendPeeringEvent(p *PeeringRequest, kind, message string, user Record) {
	at := time.Now().UTC()
	if len(p.Events) > 0 && !at.After(p.Events[len(p.Events)-1].At) {
		at = p.Events[len(p.Events)-1].At.Add(time.Nanosecond)
	}
	memberID := ""
	if str(user, "role") == "member" {
		memberID = str(user, "memberId")
	}
	p.Events = append(p.Events, PeeringEvent{ID: token(16), Kind: kind, At: at, ActorID: str(user, "id"), ActorMemberID: memberID, Message: message})
}
func validatePeering(s *State) error {
	seen := map[string]bool{}
	for _, p := range s.PeeringRequests {
		if !identifier.MatchString(p.ID) || seen[p.ID] || p.FromMemberID == p.ToMemberID || !identifier.MatchString(p.FromMemberID) || !identifier.MatchString(p.ToMemberID) || !identifier.MatchString(p.VLANID) || p.CreatedAt.IsZero() || len(p.Events) < 1 || len(p.Events) > 3 {
			return errors.New("invalid bilateral peering record")
		}
		seen[p.ID] = true
		last := ""
		var previous time.Time
		eventIDs := map[string]bool{}
		for n, e := range p.Events {
			if !identifier.MatchString(e.ID) || eventIDs[e.ID] || e.At.IsZero() || (!previous.IsZero() && !e.At.After(previous)) {
				return errors.New("invalid peering event history")
			}
			eventIDs[e.ID] = true
			if n == 0 && e.Kind != "proposed" {
				return errors.New("peering history must start with proposal")
			}
			if n > 0 {
				if last == "proposed" && (e.Kind != "accepted" && e.Kind != "rejected" && e.Kind != "revoked") {
					return errors.New("invalid peering transition")
				}
				if last == "accepted" && e.Kind != "revoked" {
					return errors.New("accepted peering can only be revoked")
				}
				if last == "rejected" || last == "revoked" {
					return errors.New("terminal peering history cannot be changed")
				}
			}
			last = e.Kind
			previous = e.At
		}
		expected := last
		if last == "proposed" {
			expected = "pending"
		}
		if p.Status != expected {
			return errors.New("peering status differs from immutable history")
		}
	}
	return nil
}
func (a *App) queuePeeringNotification(s *State, p PeeringRequest, recipientMemberID string) error {
	if !a.mailConfig.Enabled {
		return errors.New("mail delivery disabled; submit without the notification option")
	}
	last := p.Events[len(p.Events)-1]
	seen := map[string]bool{}
	queued := 0
	for _, u := range s.Resources["users"] {
		if str(u, "role") != "member" || !contains(membershipIDs(u), recipientMemberID) || !memberAdministrator(u, recipientMemberID) {
			continue
		}
		email := str(u, "email")
		if seen[email] {
			continue
		}
		seen[email] = true
		subject := "MoeDove IXP：双边互联请求更新"
		statusLabel := map[string]string{"pending": "待处理", "accepted": "已同意管理约定", "rejected": "已拒绝", "revoked": "已撤销"}[p.Status]
		text := fmt.Sprintf("成员“%s”与“%s”在 VLAN“%s”的双边互联请求发生了更新，当时状态为：%s。\n\n请登录控制台查看并处理：\n%s/#peering\n\n此状态仅表示管理约定，不代表 BGP 会话已建立。", p.FromMemberName, p.ToMemberName, p.VLANName, statusLabel, strings.TrimSuffix(a.mailConfig.BaseURL, "/"))
		key := "peering:" + p.ID + ":" + last.ID + ":" + recipientMemberID + ":" + digest(email)
		if _, err := s.Outbox.Enqueue(key, notify.Message{From: a.mailConfig.From, To: email, Subject: subject, Text: text}, a.store.securityKey, time.Now().UTC(), time.Now().UTC().Add(7*24*time.Hour)); err != nil {
			return err
		}
		queued++
	}
	if queued == 0 {
		return errors.New("target member has no administrator mailbox; submit without notification")
	}
	return nil
}
func (a *App) peering(w http.ResponseWriter, r *http.Request, user Record) {
	state := a.store.Snapshot()
	memberRole := str(user, "role") == "member"
	memberID := str(user, "memberId")
	switch r.URL.Path {
	case "/api/peering/partners":
		if r.Method != "GET" {
			problem(w, 405, "method not allowed")
			return
		}
		members := []Record{}
		if memberRole && activePeeringMember(&state, memberID) {
			for _, m := range state.Resources["members"] {
				id := str(m, "id")
				vlans := sharedPeeringVLANs(&state, memberID, id)
				if len(vlans) > 0 {
					members = append(members, Record{"id": id, "name": str(m, "name"), "asn": number(m, "asn"), "sharedVlans": vlans})
				}
			}
		}
		respond(w, 200, Record{"members": members, "mailEnabled": a.mailConfig.Enabled})
		return
	case "/api/peering/matrix":
		if r.Method != "GET" {
			problem(w, 405, "method not allowed")
			return
		}
		a.peeringMatrix(w, user, state)
		return
	case "/api/peering/requests":
		if r.Method == "GET" {
			items := []Record{}
			for i := len(state.PeeringRequests) - 1; i >= 0; i-- {
				p := state.PeeringRequests[i]
				if memberRole && p.FromMemberID != memberID && p.ToMemberID != memberID {
					continue
				}
				items = append(items, peeringRequestView(&state, p, user))
			}
			respond(w, 200, Record{"items": items})
			return
		}
	}
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if a.limited(r) {
		problem(w, 429, "too many peering actions; retry later")
		return
	}
	var body struct {
		ToMemberID string `json:"toMemberId"`
		VLANID     string `json:"vlanId"`
		Message    string `json:"message"`
		Decision   string `json:"decision"`
		Notify     bool   `json:"notify"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if len(body.Message) > 2000 || strings.ContainsAny(body.Message, "\x00\r") {
		problem(w, 400, "message must be at most 2000 bytes")
		return
	}
	if body.Notify && !a.mailConfig.Enabled {
		problem(w, 503, "mail delivery disabled; submit without notification")
		return
	}
	if r.URL.Path == "/api/peering/requests" {
		if !memberRole {
			problem(w, 403, "a member must propose its own bilateral peering request")
			return
		}
		var created PeeringRequest
		err := a.store.Update(func(s *State) error {
			stored := find(s, "users", str(user, "id"))
			if str(stored, "role") != "member" || !contains(membershipIDs(stored), memberID) || !sharedPeeringVLAN(s, memberID, body.ToMemberID, body.VLANID) {
				return errors.New("both active members must share the exact member-visible peering VLAN")
			}
			if len(s.PeeringRequests) >= 10000 {
				return errors.New("peering history capacity reached")
			}
			pending := 0
			for _, p := range s.PeeringRequests {
				if p.FromMemberID == memberID && p.Status == "pending" {
					pending++
				}
				if p.VLANID == body.VLANID && (p.Status == "pending" || p.Status == "accepted") && ((p.FromMemberID == memberID && p.ToMemberID == body.ToMemberID) || (p.ToMemberID == memberID && p.FromMemberID == body.ToMemberID)) {
					return errors.New("an active request or agreement already exists for this pair and VLAN")
				}
			}
			if pending >= 100 {
				return errors.New("too many outstanding requests for this member")
			}
			created = PeeringRequest{FromASN: uint32(number(find(s, "members", memberID), "asn")), ToASN: uint32(number(find(s, "members", body.ToMemberID), "asn")), ID: token(16), FromMemberID: memberID, FromMemberName: str(find(s, "members", memberID), "name"), ToMemberID: body.ToMemberID, ToMemberName: str(find(s, "members", body.ToMemberID), "name"), VLANID: body.VLANID, VLANName: str(find(s, "vlans", body.VLANID), "name"), Status: "pending", Message: body.Message, CreatedAt: time.Now().UTC(), CreatedBy: str(user, "id")}
			appendPeeringEvent(&created, "proposed", body.Message, user)
			if body.Notify {
				if err := a.queuePeeringNotification(s, created, body.ToMemberID); err != nil {
					return err
				}
			}
			s.PeeringRequests = append(s.PeeringRequests, created)
			audit(s, str(user, "id"), "request-bilateral-peering", "peering", created.ID)
			return validatePeering(s)
		})
		if err != nil {
			problem(w, 409, err.Error())
			return
		}
		fresh := a.store.Snapshot()
		respond(w, 201, peeringRequestView(&fresh, created, user))
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/peering/requests/"), "/")
	if len(parts) != 2 || (parts[1] != "decision" && parts[1] != "revoke") {
		problem(w, 404, "peering action not found")
		return
	}
	var result PeeringRequest
	err := a.store.Update(func(s *State) error {
		for index := range s.PeeringRequests {
			p := &s.PeeringRequests[index]
			if p.ID != parts[0] {
				continue
			}
			if memberRole && memberID != p.FromMemberID && memberID != p.ToMemberID {
				return errors.New("peering request not found")
			}
			target := ""
			if parts[1] == "decision" {
				if !peeringCanDecide(s, *p, user) {
					return errors.New("only the active recipient member administrator may decide on shared-VLAN requests")
				}
				switch body.Decision {
				case "accept":
					p.Status = "accepted"
				case "reject":
					p.Status = "rejected"
				default:
					return errors.New("decision must be accept or reject")
				}
				target = p.FromMemberID
				appendPeeringEvent(p, p.Status, body.Message, user)
			} else {
				if !peeringCanRevoke(s, *p, user) {
					return errors.New("peering revocation permission required")
				}
				p.Status = "revoked"
				target = p.ToMemberID
				if memberID == p.ToMemberID {
					target = p.FromMemberID
				}
				appendPeeringEvent(p, "revoked", body.Message, user)
			}
			if body.Notify {
				if err := a.queuePeeringNotification(s, *p, target); err != nil {
					return err
				}
				if parts[1] == "revoke" && str(user, "role") == "admin" && target != p.FromMemberID {
					if err := a.queuePeeringNotification(s, *p, p.FromMemberID); err != nil {
						return err
					}
				}
			}
			result = *p
			audit(s, str(user, "id"), "bilateral-"+p.Status, "peering", p.ID)
			return validatePeering(s)
		}
		return errors.New("peering request not found")
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	fresh := a.store.Snapshot()
	respond(w, 200, peeringRequestView(&fresh, result, user))
}
func (a *App) peeringMatrix(w http.ResponseWriter, user Record, s State) {
	memberRole := str(user, "role") == "member"
	selected := str(user, "memberId")
	allowedMembers := map[string]bool{}
	allowedVLANs := map[string]bool{}
	members := []Record{}
	vlans := []Record{}
	routeServer := []Record{}
	bilateral := []Record{}
	for _, m := range s.Resources["members"] {
		id := str(m, "id")
		visible := !memberRole
		if memberRole && activePeeringMember(&s, selected) && find(&s, "members", selected)["peeringMatrixEnabled"] != false {
			visible = id == selected || sharedMatrixPartner(&s, selected, id)
		}
		if visible {
			allowedMembers[id] = true
			members = append(members, Record{"id": id, "name": str(m, "name"), "asn": number(m, "asn")})
		}
	}
	for _, v := range s.Resources["vlans"] {
		id := str(v, "id")
		if !memberRole || (peeringVLANVisible(&s, v, true) && activePeeringMember(&s, selected) && find(&s, "members", selected)["peeringMatrixEnabled"] != false && activeMemberOnVLAN(&s, selected, id)) {
			allowedVLANs[id] = true
			vlans = append(vlans, Record{"id": id, "name": str(v, "name"), "tag": number(v, "tag"), "fabricName": fabricName(&s, fabricID(v))})
		}
	}
	for _, p := range s.Resources["peers"] {
		router := find(&s, "routers", str(p, "routerId"))
		if routerRole(router) != "route-server" {
			continue
		}
		if router == nil {
			continue
		}
		vlanID := str(router, "vlanId")
		if memberRole && (str(p, "memberId") != selected || !allowedVLANs[vlanID]) {
			continue
		}
		routeServer = append(routeServer, Record{"peerId": str(p, "id"), "memberId": str(p, "memberId"), "routerId": str(p, "routerId"), "routerName": str(router, "name"), "vlanId": vlanID, "address": str(p, "address"), "configuredEnabled": p["enabled"] == true})
	}
	for _, p := range s.PeeringRequests {
		if memberRole && (p.FromMemberID != selected && p.ToMemberID != selected) {
			continue
		}
		if memberRole && (!allowedMembers[p.FromMemberID] || !allowedMembers[p.ToMemberID] || !allowedVLANs[p.VLANID]) {
			continue
		}
		bilateral = append(bilateral, peeringRequestView(&s, p, user))
	}
	sort.Slice(members, func(i, j int) bool { return number(members[i], "asn") < number(members[j], "asn") })
	respond(w, 200, Record{"members": members, "vlans": vlans, "routeServer": routeServer, "bilateral": bilateral, "operationalStateSource": "/api/monitoring/summary", "agreementIsOperationalState": false})
}

func peeringMailApplicable(s *State, e notify.Entry, now time.Time) bool {
	parts := strings.Split(e.Key, ":")
	if len(parts) != 5 || parts[0] != "peering" || parts[4] != digest(e.To) {
		return false
	}
	for _, p := range s.PeeringRequests {
		if p.ID != parts[1] {
			continue
		}
		if len(p.Events) == 0 || p.Events[len(p.Events)-1].ID != parts[2] || (parts[3] != p.FromMemberID && parts[3] != p.ToMemberID) {
			return false
		}
		if p.Events[len(p.Events)-1].Kind != "revoked" && !peeringTopologyValid(s, p) {
			return false
		}
		for _, u := range s.Resources["users"] {
			if str(u, "role") == "member" && strings.EqualFold(str(u, "email"), e.To) && contains(membershipIDs(u), parts[3]) && memberAdministrator(u, parts[3]) {
				return true
			}
		}
		return false
	}
	return false
}
