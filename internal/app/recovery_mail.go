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

	"github.com/moedove/ixp-manager/internal/migrate"
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
	ID             string                   `json:"id"`
	Email          string                   `json:"email"`
	MemberID       string                   `json:"memberId"`
	MemberAdmin    bool                     `json:"memberAdmin"`
	InvitedBy      string                   `json:"invitedBy"`
	ExistingUserID string                   `json:"existingUserId,omitempty"`
	Generation     uint64                   `json:"generation"`
	TokenHash      string                   `json:"tokenHash"`
	CreatedAt      time.Time                `json:"createdAt"`
	ExpiresAt      time.Time                `json:"expiresAt"`
	AcceptedAt     time.Time                `json:"acceptedAt,omitempty"`
	Revoked        bool                     `json:"revoked"`
	LegacyAccount  *legacyAccountInvitation `json:"legacyAccount,omitempty"`
}

type legacyAccountInvitation struct {
	ArchiveChecksum       string                      `json:"archiveChecksum"`
	SourceUserID          string                      `json:"sourceUserId"`
	IssuerCreatedAt       string                      `json:"issuerCreatedAt"`
	ExistingUserCreatedAt string                      `json:"existingUserCreatedAt,omitempty"`
	Organizations         []legacyAccountOrganization `json:"organizations"`
}

var errLegacyInvitationConsent = errors.New("组织范围已更新，请重新核对邀请后再接受")

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
	view := Record{"id": i.ID, "email": i.Email, "memberId": i.MemberID, "memberName": str(find(s, "members", i.MemberID), "name"), "memberAdmin": i.MemberAdmin, "status": invitationStatus(i, time.Now()), "createdAt": i.CreatedAt, "expiresAt": i.ExpiresAt}
	addLegacyInvitationOrganizations(s, i, view)
	return view
}

func legacyInvitationOrganizations(s *State, i Invitation) []Record {
	if i.LegacyAccount == nil {
		return nil
	}
	organizations := make([]Record, 0, len(i.LegacyAccount.Organizations))
	for _, organization := range i.LegacyAccount.Organizations {
		name := organization.MemberName
		if current := find(s, "members", organization.MemberID); current != nil && str(current, "createdAt") == organization.MemberCreatedAt {
			name = str(current, "name")
		}
		organizations = append(organizations, Record{"memberId": organization.MemberID, "memberName": name, "memberAdmin": organization.MemberAdmin})
	}
	return organizations
}

func addLegacyInvitationOrganizations(s *State, i Invitation, view Record) []Record {
	organizations := legacyInvitationOrganizations(s, i)
	if organizations == nil {
		return nil
	}
	for _, organization := range organizations {
		if organization["memberId"] == i.MemberID {
			view["memberName"] = organization["memberName"]
			break
		}
	}
	view["memberAdmin"], view["organizations"] = i.MemberAdmin, organizations
	return organizations
}

func (a *App) legacyInvitationConsentRevision(i Invitation, organizations []Record) string {
	return a.resourceETag("legacy-invite-consent", Record{"id": i.ID, "tokenHash": i.TokenHash, "organizations": organizations})
}

func (a *App) validateLegacyInvitationConsent(s *State, i Invitation, expected string) error {
	if i.LegacyAccount != nil && (!validResourceETag(expected) ||
		subtle.ConstantTimeCompare([]byte(expected), []byte(a.legacyInvitationConsentRevision(i, legacyInvitationOrganizations(s, i)))) != 1) {
		return errLegacyInvitationConsent
	}
	return nil
}

func invitationTargetsMember(i Invitation, memberID string) bool {
	if i.LegacyAccount == nil {
		return i.MemberID == memberID
	}
	for _, organization := range i.LegacyAccount.Organizations {
		if organization.MemberID == memberID {
			return true
		}
	}
	return false
}

func canManageInvitation(s *State, stored, user Record, i Invitation) bool {
	if i.LegacyAccount != nil {
		return str(user, "role") == "admin" && str(stored, "role") == "admin" && str(stored, "createdAt") == str(user, "createdAt")
	}
	return canInvite(s, stored, i.MemberID) && (str(user, "role") != "member" || str(user, "memberId") == i.MemberID)
}

func legacyInvitationAuthority(s *State, i Invitation, issuer Record) bool {
	legacy := i.LegacyAccount
	if !validResourceETag(legacy.ArchiveChecksum) || !migrate.AccountArchiveID(legacy.SourceUserID) || len(legacy.Organizations) == 0 ||
		str(issuer, "role") != "admin" || !legacyAccountCurrentEntity(issuer) || str(issuer, "createdAt") != legacy.IssuerCreatedAt {
		return false
	}
	primary := legacy.Organizations[0]
	if primary.MemberID != i.MemberID || primary.MemberAdmin != i.MemberAdmin {
		return false
	}
	owner := emailUser(s, i.Email)
	if i.ExistingUserID == "" {
		if owner != nil || legacy.ExistingUserCreatedAt != "" {
			return false
		}
	} else if str(owner, "id") != i.ExistingUserID || str(owner, "role") != "member" || !legacyAccountCurrentEntity(owner) ||
		str(owner, "createdAt") != legacy.ExistingUserCreatedAt || s.Security[i.ExistingUserID].Generation != i.Generation {
		return false
	}
	memberships := membershipIDs(owner)
	seen := make(map[string]bool, len(legacy.Organizations))
	for _, organization := range legacy.Organizations {
		member := find(s, "members", organization.MemberID)
		if seen[organization.MemberID] || !legacyAccountCurrentEntity(member) ||
			str(member, "status") != "active" || str(member, "createdAt") != organization.MemberCreatedAt {
			return false
		}
		role := ""
		if contains(memberships, organization.MemberID) {
			role = "member"
			if memberAdministrator(owner, organization.MemberID) {
				role = "member-admin"
			}
		}
		if role != organization.CurrentMemberRole || role == "member-admin" || (role == "member" && !organization.MemberAdmin) {
			return false
		}
		seen[organization.MemberID] = true
	}
	return true
}

func invitationCurrent(s *State, i Invitation, now time.Time) bool {
	if invitationStatus(i, now) != "pending" {
		return false
	}
	issuer := find(s, "users", i.InvitedBy)
	if i.LegacyAccount != nil {
		return legacyInvitationAuthority(s, i, issuer)
	}
	if !canInvite(s, issuer, i.MemberID) {
		return false
	}
	if i.ExistingUserID != "" {
		u := find(s, "users", i.ExistingUserID)
		return u != nil && strings.EqualFold(str(u, "email"), i.Email) && s.Security[i.ExistingUserID].Generation == i.Generation
	}
	return true
}

func auditInvitationOrganizations(s *State, actor, action string, i Invitation) {
	if i.LegacyAccount == nil {
		audit(s, actor, action, "members", i.MemberID)
		return
	}
	for _, organization := range i.LegacyAccount.Organizations {
		auditDetails(s, actor, action, "members", organization.MemberID, Record{"invitationId": i.ID, "memberAdmin": organization.MemberAdmin})
	}
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
		if !invitationCurrent(s, *i, now) {
			return nil, errors.New("邀请链接无效或已过期")
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
		view := Record{"email": i.Email, "memberId": i.MemberID, "memberName": str(find(&s, "members", i.MemberID), "name"), "expiresAt": i.ExpiresAt, "accountExists": emailUser(&s, i.Email) != nil}
		if organizations := addLegacyInvitationOrganizations(&s, *i, view); organizations != nil {
			view["organizationRevision"] = a.legacyInvitationConsentRevision(*i, organizations)
		}
		respond(w, 200, view)
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
			if canManageInvitation(&s, stored, user, i) {
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
					if !canManageInvitation(s, find(s, "users", str(user, "id")), user, *i) {
						return errors.New("invitation not found")
					}
					if !i.AcceptedAt.IsZero() {
						return errors.New("accepted invitation cannot be revoked; remove membership explicitly")
					}
					i.Revoked = true
					auditInvitationOrganizations(s, str(user, "id"), "revoke-invitation", *i)
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
			if i.Email == body.Email && invitationTargetsMember(i, body.MemberID) && invitationStatus(i, now) == "pending" {
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
		Token                string `json:"token"`
		Name                 string `json:"name"`
		Password             string `json:"password"`
		CurrentPassword      string `json:"currentPassword"`
		Code                 string `json:"code"`
		OrganizationRevision string `json:"organizationRevision"`
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
	if err := a.validateLegacyInvitationConsent(&snapshot, *invitation, body.OrganizationRevision); err != nil {
		problem(w, http.StatusConflict, err.Error())
		return
	}
	existing := emailUser(&snapshot, invitation.Email)
	var acceptedUserID string
	apply := func(s *State, user Record, security *SecurityUser) error {
		i, err := findInvitation(s, body.Token, time.Now())
		if err != nil {
			return err
		}
		if err := a.validateLegacyInvitationConsent(s, *i, body.OrganizationRevision); err != nil {
			return err
		}
		if !strings.EqualFold(str(user, "email"), i.Email) {
			return errors.New("sign in as the invited account")
		}
		ids := membershipIDs(user)
		addMembership := func(memberID string, memberAdmin bool) {
			if !contains(ids, memberID) {
				ids = append(ids, memberID)
			}
			if memberAdmin && !memberAdministrator(user, memberID) {
				admins, _ := user["memberAdminIds"].([]any)
				user["memberAdminIds"] = append(admins, memberID)
			}
		}
		if i.LegacyAccount == nil {
			addMembership(i.MemberID, i.MemberAdmin)
		} else {
			for _, org := range i.LegacyAccount.Organizations {
				addMembership(org.MemberID, org.MemberAdmin)
			}
		}
		values := []any{}
		for _, id := range ids {
			values = append(values, id)
		}
		user["memberIds"] = values
		if str(user, "memberId") == "" {
			user["memberId"] = i.MemberID
		}
		delete(user, membershipDetachedField)
		if err = validate(s, "users", user); err != nil {
			return err
		}
		security.Generation++
		security.Tokens = nil
		i.AcceptedAt = time.Now().UTC()
		acceptedUserID = str(user, "id")
		auditInvitationOrganizations(s, acceptedUserID, "accept-invitation", *i)
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
			if invitation.LegacyAccount != nil {
				cookie, err := r.Cookie("moedove_session")
				if err != nil || !a.memberRosterSessionCurrent(cookie.Value, session) {
					return errors.New("session or selected member changed; sign in again")
				}
			}
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
		if errors.Is(err, errLegacyInvitationConsent) {
			problem(w, http.StatusConflict, err.Error())
			return
		}
		problem(w, 403, err.Error())
		return
	}
	a.revokeSessions(acceptedUserID, "")
	clearCookie(w, a.secureCookie)
	respond(w, 200, Record{"ok": true})
}
