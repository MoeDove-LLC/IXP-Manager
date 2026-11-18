package app

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

func init() {
	fields := Schema{
		"type":  enum("full", "probono", "internal", "associate", "reseller"),
		"phone": textField(false), "address": textField(false), "city": textField(false), "country": textField(false), "postalCode": textField(false),
		"registrationNumber": textField(false), "vatNumber": textField(false), "billingName": textField(false), "billingEmail": {Type: "email"},
		"billingAddress": textField(false), "joinedAt": {Type: "date"}, "leftAt": {Type: "date"}, "statusReason": textField(false),
	}
	for key, field := range fields {
		Schemas["members"][key] = field
	}
	Schemas["members"]["status"] = enum("active", "pending", "suspended", "closed")
	Schemas["users"]["memberIds"] = Field{Type: "references", Ref: "members"}
	Schemas["users"]["memberAdminIds"] = Field{Type: "references", Ref: "members"}
	Schemas["contact-groups"] = Schema{"name": textField(true), "description": textField(false)}
	Schemas["contact-group-memberships"] = Schema{"name": textField(true), "contactId": ref("contacts", true), "groupId": ref("contact-groups", true)}
}
func membershipIDs(user Record) []string {
	ids := []string{}
	if values, ok := user["memberIds"].([]any); ok {
		for _, value := range values {
			if id, ok := value.(string); ok && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		return ids
	}
	if legacy := str(user, "memberId"); len(ids) == 0 && legacy != "" {
		ids = append(ids, legacy)
	}
	return ids
}
func memberAdministrator(user Record, memberID string) bool {
	values, _ := user["memberAdminIds"].([]any)
	for _, value := range values {
		if value == memberID {
			return true
		}
	}
	return false
}
func selectedMember(user Record, preferred string) string {
	ids := membershipIDs(user)
	if slices.Contains(ids, preferred) {
		return preferred
	}
	if id := str(user, "memberId"); slices.Contains(ids, id) {
		return id
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}
func withMember(user Record, memberID string) Record {
	result := clean(user)
	if str(user, "role") != "member" {
		return result
	}
	result["memberId"] = memberID
	result["memberRole"] = "member"
	if memberAdministrator(user, memberID) {
		result["memberRole"] = "member-admin"
	}
	return result
}
func validateBusiness(s *State, kind string, r Record) error {
	if kind == "peers" {
		if err := validateOriginBindings(r); err != nil {
			return err
		}
	}
	if kind == "users" {
		ids := membershipIDs(r)
		if legacy := str(r, "memberId"); legacy != "" && !slices.Contains(ids, legacy) {
			return fmt.Errorf("default memberId must belong to memberIds")
		}
		if str(r, "role") == "member" && len(ids) == 0 {
			return fmt.Errorf("member account requires at least one membership")
		}
		values, _ := r["memberAdminIds"].([]any)
		for _, value := range values {
			if !slices.Contains(ids, value.(string)) {
				return fmt.Errorf("memberAdminIds must be a subset of assigned memberships")
			}
		}
	}
	if kind == "routers" && str(r, "rpkiPolicy") == "require-valid" {
		if str(r, "rpkiHost") == "" {
			return fmt.Errorf("RPKI requires a configured cache IP")
		}
		platform := routerPlatform(r)
		if platform != "bird2" && platform != "bird3" && platform != "frr" {
			return fmt.Errorf("runtime RPKI is not supported for this platform")
		}
		if (platform == "frr" || platform == "bird3") && number(r, "rpkiExpireSeconds") > 0 && number(r, "rpkiExpireSeconds") < 600 {
			return fmt.Errorf("FRR and BIRD 3 RPKI expiry must be at least 600 seconds")
		}
	}
	if kind == "members" {
		if str(r, "leftAt") != "" && str(r, "joinedAt") != "" && str(r, "leftAt") < str(r, "joinedAt") {
			return fmt.Errorf("departure date cannot precede joining date")
		}
		if len(str(r, "phone")) > 100 || len(str(r, "country")) > 100 {
			return fmt.Errorf("phone or country is too long")
		}
	}
	if kind == "contact-group-memberships" {
		for _, old := range s.Resources[kind] {
			if str(old, "id") != str(r, "id") && str(old, "contactId") == str(r, "contactId") && str(old, "groupId") == str(r, "groupId") {
				return fmt.Errorf("contact already belongs to group")
			}
		}
	}
	return nil
}
func (a *App) memberships(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if session.API || str(user, "role") != "member" {
		problem(w, 403, "member browser account required")
		return
	}
	state := a.store.Snapshot()
	stored := find(&state, "users", str(user, "id"))
	if stored == nil {
		problem(w, 401, "sign in required")
		return
	}
	if r.URL.Path == "/api/memberships" && r.Method == "GET" {
		items := []Record{}
		for _, id := range membershipIDs(stored) {
			if member := find(&state, "members", id); member != nil {
				role := "member"
				if memberAdministrator(stored, id) {
					role = "member-admin"
				}
				items = append(items, Record{"id": id, "name": member["name"], "role": role})
			}
		}
		respond(w, 200, Record{"items": items, "activeMemberId": user["memberId"]})
		return
	}
	if r.URL.Path != "/api/memberships/select" || r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	var body struct {
		MemberID string `json:"memberId"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !slices.Contains(membershipIDs(stored), body.MemberID) {
		problem(w, 403, "membership is not assigned to this account")
		return
	}
	cookie, err := r.Cookie("moedove_session")
	if err != nil {
		problem(w, 401, "sign in required")
		return
	}
	// Record the selection before changing the browser's active tenant. A failed
	// audit leaves its current session untouched.
	if err = a.store.Update(func(s *State) error {
		current := find(s, "users", str(user, "id"))
		if current == nil || !slices.Contains(membershipIDs(current), body.MemberID) {
			return fmt.Errorf("membership changed; sign in again")
		}
		audit(s, str(user, "id"), "select-member", "members", body.MemberID)
		return nil
	}); err != nil {
		problem(w, 409, err.Error())
		return
	}
	a.mu.Lock()
	current, ok := a.sessions[cookie.Value]
	if ok && current.UserID == session.UserID && current.Generation == session.Generation {
		if current.MemberID != body.MemberID {
			current.CSRF = token(32)
		}
		current.MemberID = body.MemberID
		a.sessions[cookie.Value] = current
	} else {
		ok = false
	}
	a.mu.Unlock()
	if !ok {
		problem(w, 401, "session changed; sign in again")
		return
	}
	respond(w, 200, Record{"user": withMember(stored, body.MemberID), "csrfToken": current.CSRF})
}

var memberEditable = []string{"name", "email", "website", "phone", "address", "city", "country", "postalCode", "registrationNumber", "vatNumber"}

func (a *App) memberService(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if session.API || str(user, "role") != "member" {
		problem(w, 403, "member browser account required")
		return
	}
	memberID := str(user, "memberId")
	if memberID == "" {
		problem(w, 403, "select an assigned member")
		return
	}
	if r.Method != "GET" && str(user, "memberRole") != "member-admin" {
		problem(w, 403, "member administrator permission required")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/member/")
	state := a.store.Snapshot()
	if path == "profile" {
		if r.Method == "GET" {
			m := find(&state, "members", memberID)
			if m == nil {
				problem(w, 404, "member not found")
				return
			}
			respond(w, 200, clean(m))
			return
		}
		if r.Method != "PUT" {
			problem(w, 405, "method not allowed")
			return
		}
		var input Record
		if !decode(w, r, &input) {
			return
		}
		if input == nil {
			problem(w, 400, "object required")
			return
		}
		for key := range input {
			if !slices.Contains(memberEditable, key) {
				problem(w, 400, "field is staff-managed: "+key)
				return
			}
		}
		var result Record
		err := a.store.Update(func(s *State) error {
			current := find(s, "users", str(user, "id"))
			if !memberAdministrator(current, memberID) || !slices.Contains(membershipIDs(current), memberID) {
				return fmt.Errorf("membership permission changed")
			}
			member := find(s, "members", memberID)
			if member == nil {
				return fmt.Errorf("member not found")
			}
			for key, value := range input {
				member[key] = value
			}
			member["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
			if err := validateAll(s); err != nil {
				return err
			}
			audit(s, str(user, "id"), "update-profile", "members", memberID)
			result = clean(member)
			return nil
		})
		if err != nil {
			problem(w, 400, err.Error())
			return
		}
		respond(w, 200, result)
		return
	}
	if path != "contacts" && !strings.HasPrefix(path, "contacts/") {
		problem(w, 404, "member endpoint not found")
		return
	}
	id := strings.TrimPrefix(path, "contacts")
	if id != "" {
		if strings.Count(id, "/") != 1 || !identifier.MatchString(id[1:]) {
			problem(w, 404, "contact not found")
			return
		}
		id = id[1:]
	}
	if r.Method == "GET" && id == "" {
		respond(w, 200, Record{"items": visible(&state, "contacts", user)})
		return
	}
	if (r.Method == "POST" && id != "") || ((r.Method == "PUT" || r.Method == "DELETE") && id == "") || !slices.Contains([]string{"POST", "PUT", "DELETE"}, r.Method) {
		problem(w, 405, "method not allowed")
		return
	}
	var input struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Phone string `json:"phone"`
		Role  string `json:"role"`
	}
	if r.Method != "DELETE" && !decode(w, r, &input) {
		return
	}
	var result Record
	status := 200
	err := a.store.Update(func(s *State) error {
		current := find(s, "users", str(user, "id"))
		if !memberAdministrator(current, memberID) || !slices.Contains(membershipIDs(current), memberID) {
			status = 403
			return fmt.Errorf("membership permission changed")
		}
		old := find(s, "contacts", id)
		if r.Method != "POST" && (old == nil || str(old, "memberId") != memberID) {
			status = 404
			return fmt.Errorf("contact not found")
		}
		if r.Method == "DELETE" {
			for _, link := range s.Resources["contact-group-memberships"] {
				if str(link, "contactId") == id {
					return fmt.Errorf("contact is assigned to a group; ask staff to remove that assignment first")
				}
			}
			s.Resources["contacts"] = slices.DeleteFunc(s.Resources["contacts"], func(r Record) bool { return str(r, "id") == id })
			audit(s, str(user, "id"), "delete-contact", "contacts", id)
			return nil
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result = Record{"id": id, "name": input.Name, "email": input.Email, "phone": input.Phone, "role": input.Role, "memberId": memberID, "createdAt": now, "updatedAt": now}
		if old == nil {
			result["id"] = token(16)
			s.Resources["contacts"] = append(s.Resources["contacts"], result)
		} else {
			result["createdAt"] = old["createdAt"]
			for i, v := range s.Resources["contacts"] {
				if str(v, "id") == id {
					s.Resources["contacts"][i] = result
				}
			}
		}
		if err := validateAll(s); err != nil {
			return err
		}
		audit(s, str(user, "id"), "save-contact", "contacts", str(result, "id"))
		return nil
	})
	if err != nil {
		if status == 200 {
			status = 400
		}
		problem(w, status, err.Error())
		return
	}
	if r.Method == "DELETE" {
		respond(w, 200, Record{"ok": true})
		return
	}
	if r.Method == "POST" {
		status = 201
	}
	respond(w, status, result)
}
func (a *App) memberLifecycle(w http.ResponseWriter, r *http.Request, user Record) {
	if str(user, "role") != "admin" && str(user, "role") != "operator" {
		problem(w, 403, "staff access required")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/members/"), "/")
	if len(parts) != 2 || r.Method != "POST" || !slices.Contains([]string{"activate", "suspend", "close"}, parts[1]) {
		problem(w, 404, "unknown lifecycle action")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Reason) == "" || len(body.Reason) > 1000 {
		problem(w, 400, "a reason of at most 1000 bytes is required")
		return
	}
	connections, peers := 0, 0
	var result Record
	err := a.store.Update(func(s *State) error {
		member := find(s, "members", parts[0])
		if member == nil {
			return fmt.Errorf("member not found")
		}
		member["status"] = "suspended"
		if parts[1] == "activate" {
			member["status"] = "active"
			member["leftAt"] = ""
			if str(member, "joinedAt") == "" {
				member["joinedAt"] = time.Now().UTC().Format("2006-01-02")
			}
		}
		if parts[1] == "close" {
			member["status"] = "closed"
			member["leftAt"] = time.Now().UTC().Format("2006-01-02")
		}
		member["statusReason"] = body.Reason
		member["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		for _, peer := range s.Resources["peers"] {
			if parts[1] != "activate" && str(peer, "memberId") == parts[0] && peer["enabled"] == true {
				peer["enabled"] = false
				peers++
			}
		}
		for _, connection := range s.Resources["connections"] {
			if parts[1] != "activate" && str(connection, "memberId") == parts[0] && str(connection, "status") != "disabled" {
				connection["status"] = "disabled"
				connections++
			}
		}
		if err := validateAll(s); err != nil {
			return err
		}
		audit(s, str(user, "id"), parts[1], "members", parts[0])
		result = clean(member)
		return nil
	})
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	respond(w, 200, Record{"member": result, "disabledConnections": connections, "disabledPeers": peers, "deploymentRequired": true})
}
