package app

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"
)

type memberUserRosterItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	MemberRole string `json:"memberRole"`
	MFAEnabled bool   `json:"mfaEnabled"`
	CreatedAt  string `json:"createdAt"`
}

func (a *App) memberUserRoster(w http.ResponseWriter, r *http.Request, session Session) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if session.API {
		problem(w, http.StatusForbidden, "member browser account required")
		return
	}
	cookie, err := r.Cookie("moedove_session")
	if err != nil {
		problem(w, http.StatusUnauthorized, "sign in required")
		return
	}
	items, status := a.readMemberUserRoster(cookie.Value, session)
	switch status {
	case http.StatusUnauthorized:
		problem(w, status, "session or selected member changed; sign in again")
	case http.StatusForbidden:
		problem(w, status, "administrator permission for the active member required")
	case http.StatusOK:
		respond(w, status, Record{"memberId": session.MemberID, "items": items})
	}
}

func (a *App) readMemberUserRoster(cookie string, session Session) ([]memberUserRosterItem, int) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	state := &a.store.state
	if a.store.closed || !a.memberRosterSessionCurrent(cookie, session) {
		return nil, http.StatusUnauthorized
	}
	if _, status := memberRosterAuthority(state, session); status != http.StatusOK {
		return nil, status
	}
	memberID := session.MemberID

	items := []memberUserRosterItem{}
	for _, row := range state.Resources["users"] {
		if str(row, "role") != "member" || !contains(membershipIDs(row), memberID) {
			continue
		}
		id := str(row, "id")
		role := "member"
		if memberAdministrator(row, memberID) {
			role = "member-admin"
		}
		items = append(items, memberUserRosterItem{
			ID: id, Name: str(row, "name"), Email: str(row, "email"), MemberRole: role,
			MFAEnabled: state.Security[id].MFAEnabled, CreatedAt: str(row, "createdAt"),
		})
	}
	slices.SortFunc(items, func(left, right memberUserRosterItem) int {
		if order := cmp.Compare(strings.ToLower(left.Email), strings.ToLower(right.Email)); order != 0 {
			return order
		}
		return cmp.Compare(left.ID, right.ID)
	})
	// A switch or logout can update the browser session while this read holds the Store lock.
	if !a.memberRosterSessionCurrent(cookie, session) {
		return nil, http.StatusUnauthorized
	}
	return items, http.StatusOK
}

func memberRosterAuthority(state *State, session Session) (Record, int) {
	actor := find(state, "users", session.UserID)
	if actor == nil || state.Security[session.UserID].Generation != session.Generation ||
		(session.UserCreatedAt != "" && str(actor, "createdAt") != session.UserCreatedAt) ||
		!rememberedSessionCurrent(state, session, time.Now()) {
		return nil, http.StatusUnauthorized
	}
	memberID := session.MemberID
	member := find(state, "members", memberID)
	if str(actor, "role") != "member" || memberID == "" || !contains(membershipIDs(actor), memberID) ||
		!memberAdministrator(actor, memberID) || str(member, "status") != "active" {
		return nil, http.StatusForbidden
	}
	return member, http.StatusOK
}

func (a *App) memberRosterSessionCurrent(cookie string, session Session) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.memberRosterSessionCurrentLocked(cookie, session)
}

func (a *App) memberRosterSessionCurrentLocked(cookie string, session Session) bool {
	current, ok := a.sessions[cookie]
	return ok && current.ID == session.ID && current.UserID == session.UserID && current.Generation == session.Generation &&
		current.MemberID == session.MemberID && current.CSRF == session.CSRF &&
		current.RememberDeviceID == session.RememberDeviceID && time.Now().Before(current.Expires)
}
