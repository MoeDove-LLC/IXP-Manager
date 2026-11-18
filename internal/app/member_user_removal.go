package app

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

type memberUserRemovalTarget struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	MemberRole string `json:"memberRole"`
}

type memberUserRemovalPreview struct {
	MemberID               string                  `json:"memberId"`
	MemberName             string                  `json:"memberName"`
	Target                 memberUserRemovalTarget `json:"target"`
	CanRemove              bool                    `json:"canRemove"`
	BlockedReason          string                  `json:"blockedReason"`
	DetachesAccount        bool                    `json:"detachesAccount"`
	SignsOutCurrentSession bool                    `json:"signsOutCurrentSession"`
	ETag                   string                  `json:"_etag"`
}

func (a *App) memberUserRemoval(w http.ResponseWriter, r *http.Request, session Session) {
	id, action, hasAction := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/member/users/"), "/")
	if !identifier.MatchString(id) || (hasAction && action != "removal-preview") {
		problem(w, http.StatusNotFound, "member account not found")
		return
	}
	method := http.MethodDelete
	if hasAction {
		method = http.MethodGet
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
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
	if hasAction {
		preview, status, err := a.previewMemberUserRemoval(cookie.Value, session, id)
		if err != nil {
			problem(w, status, err.Error())
			return
		}
		w.Header().Set("ETag", fmt.Sprintf("%q", preview.ETag))
		respond(w, http.StatusOK, preview)
		return
	}
	status, err := a.removeMemberUser(r, cookie.Value, session, id)
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, http.StatusOK, Record{"ok": true, "memberId": session.MemberID, "userId": id, "signedOut": id == session.UserID})
}

func (a *App) previewMemberUserRemoval(cookie string, session Session, targetID string) (memberUserRemovalPreview, int, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.memberUserRemovalReview(cookie, session, targetID)
}

// The caller holds both the Store and session locks.
func (a *App) memberUserRemovalReview(cookie string, session Session, targetID string) (memberUserRemovalPreview, int, error) {
	var preview memberUserRemovalPreview
	if a.store.closed || !a.memberRosterSessionCurrentLocked(cookie, session) {
		return preview, http.StatusUnauthorized, errors.New("session or selected member changed; sign in again")
	}
	s := &a.store.state
	member, status := memberRosterAuthority(s, session)
	if status == http.StatusUnauthorized {
		return preview, status, errors.New("session or selected member changed; sign in again")
	}
	if status != http.StatusOK {
		return preview, status, errors.New("administrator permission for the active member required")
	}
	target := find(s, "users", targetID)
	memberID := session.MemberID
	memberships := membershipIDs(target)
	if str(target, "role") != "member" || !contains(memberships, memberID) {
		return preview, http.StatusNotFound, errors.New("member account not found")
	}
	preview = memberUserRemovalPreview{
		MemberID: memberID, MemberName: str(member, "name"), CanRemove: true,
		DetachesAccount: len(memberships) == 1, SignsOutCurrentSession: session.UserID == targetID,
		Target: memberUserRemovalTarget{ID: targetID, Name: str(target, "name"), Email: str(target, "email"), MemberRole: "member"},
	}
	if memberAdministrator(target, memberID) {
		preview.Target.MemberRole = "member-admin"
		if !hasOtherMemberAdministrator(s, memberID, targetID) {
			preview.CanRemove = false
			preview.BlockedReason = "last-member-admin"
		}
	}
	preview.ETag = a.resourceETag("member-user-removal", Record{
		"id": targetID, "target": target, "targetGeneration": s.Security[targetID].Generation,
		"memberId": memberID, "memberName": preview.MemberName, "memberCreatedAt": str(member, "createdAt"), "blockedReason": preview.BlockedReason,
		"sessionId": session.ID, "sessionCSRF": session.CSRF,
	})
	if preview.ETag == "" {
		return preview, http.StatusInternalServerError, errors.New("member removal revision unavailable")
	}
	return preview, http.StatusOK, nil
}

func hasOtherMemberAdministrator(s *State, memberID, targetID string) bool {
	for _, candidate := range s.Resources["users"] {
		if str(candidate, "id") != targetID && str(candidate, "role") == "member" && str(candidate, "passwordHash") != "" &&
			contains(membershipIDs(candidate), memberID) && memberAdministrator(candidate, memberID) {
			return true
		}
	}
	return false
}

func (a *App) removeMemberUser(r *http.Request, cookie string, session Session, targetID string) (int, error) {
	// Keep both locks through persistence so a logout, tenant switch or another removal
	// cannot change the reviewed authority between validation and commit.
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	preview, status, err := a.memberUserRemovalReview(cookie, session, targetID)
	if err != nil {
		return status, err
	}
	expected, status, err := resourcePrecondition(r, nil)
	if err != nil {
		return status, err
	}
	if expected != preview.ETag {
		return http.StatusPreconditionFailed, errors.New("member removal changed; review the current membership before retrying")
	}
	if !preview.CanRemove {
		return http.StatusConflict, errors.New("cannot remove the last member administrator")
	}
	memberID := session.MemberID
	if err := a.store.updateLocked(func(s *State) error {
		target := find(s, "users", targetID)
		remaining := []any{}
		for _, id := range membershipIDs(target) {
			if id != memberID {
				remaining = append(remaining, id)
			}
		}
		target["memberIds"] = remaining
		if admins, present := target["memberAdminIds"].([]any); present {
			target["memberAdminIds"] = slices.DeleteFunc(admins, func(id any) bool { return id == memberID })
		}
		if preview.DetachesAccount {
			delete(target, "memberId")
			target[membershipDetachedField] = true
		} else if str(target, "memberId") == memberID {
			target["memberId"] = remaining[0]
		}
		target["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		if err := validate(s, "users", target); err != nil {
			return err
		}
		invalidateSecurity(s, targetID)
		auditDetails(s, session.UserID, "remove-member-user", "members", memberID, Record{"userId": targetID, "memberRole": preview.Target.MemberRole})
		return nil
	}); err != nil {
		return http.StatusInternalServerError, errors.New("unable to commit member removal")
	}
	a.revokeSessionsLocked(targetID, "")
	return http.StatusOK, nil
}
