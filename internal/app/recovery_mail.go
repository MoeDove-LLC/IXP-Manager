package app

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

type RecoveryRequest struct {
	ID         string    `json:"id"`
	UserID     string    `json:"userId"`
	TokenHash  string    `json:"tokenHash"`
	Generation uint64    `json:"generation"`
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	UsedAt     time.Time `json:"usedAt,omitempty"`
}
type Invitation struct {
	ID             string    `json:"id"`
	Email          string    `json:"email"`
	MemberID       string    `json:"memberId"`
	MemberAdmin    bool      `json:"memberAdmin"`
	InvitedBy      string    `json:"invitedBy"`
	ExistingUserID string    `json:"existingUserId,omitempty"`
	Generation     uint64    `json:"generation"`
	TokenHash      string    `json:"tokenHash"`
	CreatedAt      time.Time `json:"createdAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
	AcceptedAt     time.Time `json:"acceptedAt,omitempty"`
	Revoked        bool      `json:"revoked"`
}

func mailAddress(raw string) bool {
	a, err := mail.ParseAddress(raw)
	return err == nil && a.Address == raw && !strings.ContainsAny(raw, "\r\n")
}
func emailUser(s *State, email string) Record {
	for _, u := range s.Resources["users"] {
		if strings.EqualFold(str(u, "email"), email) {
			return u
		}
	}
	return nil
}
func mailLink(base, page, raw string) string {
	return strings.TrimSuffix(base, "/") + "/#" + page + "?token=" + url.QueryEscape(raw)
}
func validMailToken(raw string) bool {
	return len(raw) >= 32 && len(raw) <= 128 && !strings.ContainsAny(raw, "\r\n \t")
}
func canInvite(s *State, user Record, memberID string) bool {
	member := find(s, "members", memberID)
	if member == nil || str(member, "status") == "closed" || str(member, "status") == "suspended" {
		return false
	}
	return str(user, "role") == "admin" || (str(user, "role") == "member" && contains(membershipIDs(user), memberID) && memberAdministrator(user, memberID))
}
func invitationStatus(i Invitation, now time.Time) string {
	if !i.AcceptedAt.IsZero() {
		return "accepted"
	}
	if i.Revoked {
		return "revoked"
	}
	if !now.Before(i.ExpiresAt) {
		return "expired"
	}
	return "pending"
}
func publicInvitation(s *State, i Invitation) Record {
	return Record{"id": i.ID, "email": i.Email, "memberId": i.MemberID, "memberName": str(find(s, "members", i.MemberID), "name"), "memberAdmin": i.MemberAdmin, "status": invitationStatus(i, time.Now()), "createdAt": i.CreatedAt, "expiresAt": i.ExpiresAt}
}
func findInvitation(s *State, raw string, now time.Time) (*Invitation, error) {
	if !validMailToken(raw) {
		return nil, errors.New("邀请链接无效或已过期")
	}
	hash := digest(raw)
	for index := range s.Invitations {
		i := &s.Invitations[index]
		if subtle.ConstantTimeCompare([]byte(i.TokenHash), []byte(hash)) != 1 {
			continue
		}
		if invitationStatus(*i, now) != "pending" || !canInvite(s, find(s, "users", i.InvitedBy), i.MemberID) {
			return nil, errors.New("邀请链接无效或已过期")
		}
		if i.ExistingUserID != "" {
			u := find(s, "users", i.ExistingUserID)
			if u == nil || !strings.EqualFold(str(u, "email"), i.Email) || s.Security[i.ExistingUserID].Generation != i.Generation {
				return nil, errors.New("邀请链接无效或已过期")
			}
		}
		return i, nil
	}
	return nil, errors.New("邀请链接无效或已过期")
}

func (a *App) recoveryMailRoute(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path != "/api/forgot-password" && path != "/api/reset-password" && path != "/api/invitations/inspect" && path != "/api/invitations/accept" {
		return false
	}
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return true
	}
	if a.limited(r) {
		problem(w, 429, "too many attempts; retry in 10 minutes")
		return true
	}
	switch path {
	case "/api/forgot-password":
		started := time.Now()
		var body struct {
			Email string `json:"email"`
		}
		if !decode(w, r, &body) {
			return true
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))
		raw := token(32)
		now := time.Now().UTC()
		if mailAddress(email) && a.mailConfig.Enabled {
			_ = a.store.Update(func(s *State) error {
				user := emailUser(s, email)
				if user == nil {
					return nil
				}
				id := str(user, "id")
				generation := s.Security[id].Generation
				for _, previous := range s.Recovery {
					if previous.UserID == id && previous.Generation == generation && previous.UsedAt.IsZero() && previous.CreatedAt.After(now.Add(-5*time.Minute)) && previous.ExpiresAt.After(now) {
						return nil
					}
				}
				if len(s.Recovery) >= 10000 {
					return errors.New("recovery capacity reached")
				}
				request := RecoveryRequest{ID: token(16), UserID: id, Generation: generation, TokenHash: digest(raw), CreatedAt: now, ExpiresAt: now.Add(20 * time.Minute)}
				msg := notify.Message{From: a.mailConfig.From, To: str(user, "email"), Subject: "MoeDove IXP：重置登录密码", Text: "我们收到了一次密码重置请求。\n\n请在 20 分钟内打开以下链接设置新密码：\n" + mailLink(a.mailConfig.BaseURL, "reset-password", raw) + "\n\n如果您开启了两步验证，还需要验证器验证码或一次性恢复码。此操作不会关闭两步验证。\n如果这不是您发起的请求，请忽略此邮件。"}
				if _, err := s.Outbox.Enqueue("reset:"+request.ID, msg, a.store.securityKey, now, request.ExpiresAt); err != nil {
					return err
				}
				s.Recovery = append(s.Recovery, request)
				return nil
			})
		}
		if delay := time.Until(started.Add(200 * time.Millisecond)); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-r.Context().Done():
			case <-timer.C:
			}
			timer.Stop()
		}
		respond(w, 202, Record{"ok": true, "message": "如果账号存在且邮件服务已启用，您将收到恢复邮件。"})
		return true
	case "/api/reset-password":
		var body struct {
			Token       string `json:"token"`
			NewPassword string `json:"newPassword"`
			Code        string `json:"code"`
		}
		if !decode(w, r, &body) {
			return true
		}
		if !validMailToken(body.Token) || len(body.NewPassword) < 12 || len(body.NewPassword) > 128 {
			problem(w, 400, "链接无效、已过期或新密码不符合要求")
			return true
		}
		hashed := hashPassword(body.NewPassword)
		now := time.Now().UTC()
		userID := ""
		mfaRequired := false
		tokenHash := digest(body.Token)
		err := a.store.Update(func(s *State) error {
			for index := range s.Recovery {
				request := &s.Recovery[index]
				if subtle.ConstantTimeCompare([]byte(request.TokenHash), []byte(tokenHash)) != 1 {
					continue
				}
				user := find(s, "users", request.UserID)
				security := s.Security[request.UserID]
				if user == nil || !request.UsedAt.IsZero() || !now.Before(request.ExpiresAt) || security.Generation != request.Generation {
					return errors.New("invalid recovery token")
				}
				if security.MFAEnabled && body.Code == "" {
					mfaRequired = true
					return errors.New("authentication code required")
				}
				if err := a.consumeMFA(request.UserID, &security, body.Code, now); err != nil {
					mfaRequired = security.MFAEnabled
					return err
				}
				user["passwordHash"] = hashed
				user["updatedAt"] = now.Format(time.RFC3339Nano)
				security.Generation++
				security.Tokens = nil
				security.PendingSecret = ""
				security.PendingUntil = time.Time{}
				s.Security[request.UserID] = security
				request.UsedAt = now
				userID = request.UserID
				audit(s, userID, "mail-password-reset", "users", userID)
				return nil
			}
			return errors.New("invalid recovery token")
		})
		if err != nil {
			if mfaRequired {
				respond(w, 403, Record{"error": "需要有效的两步验证码或一次性恢复码", "mfaRequired": true})
			} else {
				problem(w, 400, "链接无效或已过期，请重新申请")
			}
			return true
		}
		a.revokeSessions(userID, "")
		clearCookie(w, a.secureCookie)
		respond(w, 200, Record{"ok": true})
		return true
	case "/api/invitations/inspect":
		var body struct {
			Token string `json:"token"`
		}
		if !decode(w, r, &body) {
			return true
		}
		s := a.store.Snapshot()
		i, err := findInvitation(&s, body.Token, time.Now())
		if err != nil {
			problem(w, 400, err.Error())
			return true
		}
		respond(w, 200, Record{"email": i.Email, "memberId": i.MemberID, "memberName": str(find(&s, "members", i.MemberID), "name"), "expiresAt": i.ExpiresAt, "accountExists": emailUser(&s, i.Email) != nil})
		return true
	case "/api/invitations/accept":
		a.acceptInvitation(w, r)
		return true
	}
	return true
}

func (a *App) invitations(w http.ResponseWriter, r *http.Request, user Record) {
	s := a.store.Snapshot()
	stored := find(&s, "users", str(user, "id"))
	path := strings.TrimPrefix(r.URL.Path, "/api/invitations")
	if path == "" && r.Method == "GET" {
		items := []Record{}
		for _, i := range s.Invitations {
			if canInvite(&s, stored, i.MemberID) && (str(user, "role") != "member" || str(user, "memberId") == i.MemberID) {
				items = append(items, publicInvitation(&s, i))
			}
		}
		respond(w, 200, Record{"items": items})
		return
	}
	if r.Method == "DELETE" && strings.HasPrefix(path, "/") && !strings.Contains(strings.TrimPrefix(path, "/"), "/") {
		id := strings.TrimPrefix(path, "/")
		err := a.store.Update(func(s *State) error {
			for index := range s.Invitations {
				i := &s.Invitations[index]
				if i.ID == id {
					if !canInvite(s, find(s, "users", str(user, "id")), i.MemberID) || (str(user, "role") == "member" && str(user, "memberId") != i.MemberID) {
						return errors.New("invitation not found")
					}
					if !i.AcceptedAt.IsZero() {
						return errors.New("accepted invitation cannot be revoked; remove membership explicitly")
					}
					i.Revoked = true
					audit(s, str(user, "id"), "revoke-invitation", "members", i.MemberID)
					return nil
				}
			}
			return errors.New("invitation not found")
		})
		if err != nil {
			problem(w, 409, err.Error())
			return
		}
		respond(w, 200, Record{"ok": true})
		return
	}
	if path != "" || r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if !a.mailConfig.Enabled {
		problem(w, 503, "mail delivery is disabled in local configuration")
		return
	}
	if a.limited(r) {
		problem(w, 429, "too many attempts; retry in 10 minutes")
		return
	}
	var body struct {
		Email           string `json:"email"`
		MemberID        string `json:"memberId"`
		MemberAdmin     bool   `json:"memberAdmin"`
		CurrentPassword string `json:"currentPassword"`
		Code            string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if !mailAddress(body.Email) {
		problem(w, 400, "invalid email address")
		return
	}
	var created Invitation
	now := time.Now().UTC()
	raw := token(32)
	err := a.reauth(str(user, "id"), body.CurrentPassword, body.Code, func(s *State, security *SecurityUser) error {
		issuer := find(s, "users", str(user, "id"))
		if !canInvite(s, issuer, body.MemberID) || (str(issuer, "role") == "member" && str(user, "memberId") != body.MemberID) {
			return errors.New("member invitation permission required")
		}
		if len(s.Invitations) >= 10000 {
			return errors.New("invitation capacity reached")
		}
		for _, i := range s.Invitations {
			if i.Email == body.Email && i.MemberID == body.MemberID && invitationStatus(i, now) == "pending" {
				return errors.New("an active invitation already exists; revoke it before resending")
			}
		}
		created = Invitation{ID: token(16), Email: body.Email, MemberID: body.MemberID, MemberAdmin: body.MemberAdmin, InvitedBy: str(issuer, "id"), TokenHash: digest(raw), CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}
		if existing := emailUser(s, body.Email); existing != nil {
			created.ExistingUserID = str(existing, "id")
			created.Generation = s.Security[created.ExistingUserID].Generation
		}
		role := "成员"
		if body.MemberAdmin {
			role = "成员管理员"
		}
		msg := notify.Message{From: a.mailConfig.From, To: body.Email, Subject: "MoeDove IXP：成员账号邀请", Text: fmt.Sprintf("您受邀以%s身份访问成员“%s”。\n\n请在七天内打开以下链接，检查邀请并主动接受：\n%s\n\n已有账号需要登录后重新验证密码及两步验证；该邀请不会自动更改现有账号权限。\n如您不认识此邀请，请忽略邮件。", role, str(find(s, "members", body.MemberID), "name"), mailLink(a.mailConfig.BaseURL, "accept-invitation", raw))}
		if _, err := s.Outbox.Enqueue("invite:"+created.ID, msg, a.store.securityKey, now, created.ExpiresAt); err != nil {
			return err
		}
		s.Invitations = append(s.Invitations, created)
		audit(s, str(issuer, "id"), "invite-member", "members", body.MemberID)
		return nil
	})
	if err != nil {
		problem(w, 403, err.Error())
		return
	}
	snapshot := a.store.Snapshot()
	respond(w, 201, publicInvitation(&snapshot, created))
}
func (a *App) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token           string `json:"token"`
		Name            string `json:"name"`
		Password        string `json:"password"`
		CurrentPassword string `json:"currentPassword"`
		Code            string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	snapshot := a.store.Snapshot()
	invitation, err := findInvitation(&snapshot, body.Token, time.Now())
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	existing := emailUser(&snapshot, invitation.Email)
	var acceptedUserID string
	apply := func(s *State, user Record, security *SecurityUser) error {
		i, err := findInvitation(s, body.Token, time.Now())
		if err != nil {
			return err
		}
		if !strings.EqualFold(str(user, "email"), i.Email) {
			return errors.New("sign in as the invited account")
		}
		ids := membershipIDs(user)
		if !contains(ids, i.MemberID) {
			ids = append(ids, i.MemberID)
		}
		values := []any{}
		for _, id := range ids {
			values = append(values, id)
		}
		user["memberIds"] = values
		if str(user, "memberId") == "" {
			user["memberId"] = i.MemberID
		}
		if i.MemberAdmin {
			admins, _ := user["memberAdminIds"].([]any)
			found := false
			for _, id := range admins {
				if id == i.MemberID {
					found = true
				}
			}
			if !found {
				admins = append(admins, i.MemberID)
			}
			user["memberAdminIds"] = admins
		}
		if err = validate(s, "users", user); err != nil {
			return err
		}
		security.Generation++
		security.Tokens = nil
		i.AcceptedAt = time.Now().UTC()
		acceptedUserID = str(user, "id")
		audit(s, acceptedUserID, "accept-invitation", "members", i.MemberID)
		return nil
	}
	if existing != nil {
		signed, session, ok := a.authenticate(r)
		if !ok || session.API {
			problem(w, 401, "sign in to the invited account first")
			return
		}
		if str(signed, "id") != str(existing, "id") {
			problem(w, 403, "sign in as the invited account")
			return
		}
		if r.Header.Get("X-CSRF-Token") != session.CSRF {
			problem(w, 403, "invalid CSRF token")
			return
		}
		err = a.reauth(str(signed, "id"), body.CurrentPassword, body.Code, func(s *State, security *SecurityUser) error {
			return apply(s, find(s, "users", str(signed, "id")), security)
		})
	} else {
		if strings.TrimSpace(body.Name) == "" || len(body.Name) > 200 || len(body.Password) < 12 || len(body.Password) > 128 {
			problem(w, 400, "name and a password of 12..128 bytes are required")
			return
		}
		hashed := hashPassword(body.Password)
		err = a.store.Update(func(s *State) error {
			i, err := findInvitation(s, body.Token, time.Now())
			if err != nil {
				return err
			}
			if emailUser(s, i.Email) != nil {
				return errors.New("account now exists; sign in before accepting")
			}
			u := Record{"id": token(16), "name": strings.TrimSpace(body.Name), "email": i.Email, "role": "member", "passwordHash": hashed, "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}
			security := SecurityUser{}
			if err = apply(s, u, &security); err != nil {
				return err
			}
			s.Resources["users"] = append(s.Resources["users"], u)
			s.Security[str(u, "id")] = security
			return nil
		})
	}
	if err != nil {
		problem(w, 403, err.Error())
		return
	}
	a.revokeSessions(acceptedUserID, "")
	clearCookie(w, a.secureCookie)
	respond(w, 200, Record{"ok": true})
}
