package app

import (
	"bytes"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
	"github.com/moedove/ixp-manager/internal/notify"
)

const legacyAccountPreviewPath = "/api/import/account-archive/preview"
const legacyAccountInvitePath = "/api/import/account-archive/invite"

var errLegacyAccountInvitationMFA = errors.New("a valid authenticator code or recovery code is required")

type legacyAccountInvitationRequest struct {
	legacyAccountReviewInput
	Revision        string `json:"revision,omitempty"`
	CurrentPassword string `json:"currentPassword,omitempty"`
	Code            string `json:"code,omitempty"`
}

func (a *App) legacyAccountInvitationRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	issuing := r.URL.Path == legacyAccountInvitePath
	if !issuing && r.URL.Path != legacyAccountPreviewPath {
		return false
	}
	if session.API || str(user, "role") != "admin" {
		problem(w, http.StatusForbidden, "account migration requires a browser administrator")
		return true
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}
	cookie, err := r.Cookie("moedove_session")
	if err != nil {
		problem(w, http.StatusUnauthorized, "sign in required")
		return true
	}
	if issuing {
		if !a.mailConfig.Enabled {
			problem(w, http.StatusServiceUnavailable, "mail delivery is disabled in local configuration")
			return true
		}
		if a.limited(r) {
			problem(w, http.StatusTooManyRequests, "too many attempts; retry in 10 minutes")
			return true
		}
	}
	request, ok := readLegacyAccountInvitationRequest(w, r, issuing)
	if !ok {
		return true
	}
	archive, err := migrate.ReadAccountArchive(bytes.NewReader(request.Archive))
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid legacy account archive")
		return true
	}
	if issuing {
		items, status, err := a.issueLegacyAccountInvitations(cookie.Value, session, archive, request)
		if errors.Is(err, errLegacyAccountInvitationMFA) {
			respond(w, status, Record{"error": err.Error(), "mfaRequired": true})
		} else if err != nil {
			problem(w, status, err.Error())
		} else {
			respond(w, http.StatusCreated, Record{"items": items})
		}
		return true
	}
	review, revision, status, err := a.previewLegacyAccountInvitations(cookie.Value, session, archive, request.legacyAccountReviewInput)
	if err != nil {
		problem(w, status, err.Error())
	} else {
		respond(w, http.StatusOK, Record{"archiveChecksum": review.ArchiveChecksum, "accounts": review.Accounts, "revision": revision})
	}
	return true
}

func readLegacyAccountInvitationRequest(w http.ResponseWriter, r *http.Request, issuing bool) (legacyAccountInvitationRequest, bool) {
	var request legacyAccountInvitationRequest
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		problem(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return request, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, migrate.AccountArchiveLimit+(1<<20))
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			problem(w, http.StatusRequestEntityTooLarge, "legacy account invitation request exceeds 33 MiB")
		} else {
			problem(w, http.StatusBadRequest, "invalid legacy account invitation request")
		}
		return request, false
	}
	if decoder.Decode(new(any)) != io.EOF || len(request.Archive) == 0 {
		problem(w, http.StatusBadRequest, "one legacy archive and its reviewed selection are required")
		return request, false
	}
	if !issuing && (request.Revision != "" || request.CurrentPassword != "" || request.Code != "") {
		problem(w, http.StatusBadRequest, "preview does not accept credentials or a revision")
		return request, false
	}
	if issuing && request.Revision == "" {
		problem(w, http.StatusPreconditionRequired, "review the selected invitations first")
		return request, false
	}
	if issuing && (!validResourceETag(request.Revision) || len(request.CurrentPassword) > 128 || len(request.Code) > 256) {
		problem(w, http.StatusBadRequest, "invalid review revision or credentials")
		return request, false
	}
	return request, true
}

// Called with both locks held in Store, then browser session order.
func (a *App) legacyAccountAdminLocked(cookie string, session Session) (Record, int, error) {
	s := &a.store.state
	actor := find(s, "users", session.UserID)
	if a.store.closed || actor == nil || session.UserCreatedAt == "" || str(actor, "createdAt") != session.UserCreatedAt ||
		s.Security[session.UserID].Generation != session.Generation || !rememberedSessionCurrent(s, session, time.Now()) ||
		!a.memberRosterSessionCurrentLocked(cookie, session) {
		return nil, http.StatusUnauthorized, errors.New("administrator session changed; sign in again")
	}
	if session.API || str(actor, "role") != "admin" {
		return nil, http.StatusForbidden, errors.New("account migration requires a browser administrator")
	}
	return actor, http.StatusOK, nil
}

func (a *App) previewLegacyAccountInvitations(cookie string, session Session, archive *migrate.AccountArchive, selection legacyAccountReviewInput) (legacyAccountReview, string, int, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	a.mu.Lock()
	_, status, err := a.legacyAccountAdminLocked(cookie, session)
	a.mu.Unlock()
	if err != nil {
		return legacyAccountReview{}, "", status, err
	}
	review, err := buildLegacyAccountReview(&a.store.state, archive, selection, time.Now())
	if err != nil {
		return legacyAccountReview{}, "", http.StatusBadRequest, err
	}
	a.mu.Lock()
	_, status, err = a.legacyAccountAdminLocked(cookie, session)
	a.mu.Unlock()
	if err != nil {
		return legacyAccountReview{}, "", status, err
	}
	return review, a.legacyAccountInvitationRevision(session, review), http.StatusOK, nil
}

func (a *App) legacyAccountInvitationRevision(session Session, review legacyAccountReview) string {
	if len(review.Accounts) == 0 {
		return ""
	}
	accounts := make([]Record, 0, len(review.Accounts))
	for _, account := range review.Accounts {
		if account.Status != "ready" || len(account.Organizations) == 0 {
			return ""
		}
		organizations := make([]Record, 0, len(account.Organizations))
		for _, org := range account.Organizations {
			organizations = append(organizations, Record{"reviewed": org, "createdAt": org.MemberCreatedAt})
		}
		accounts = append(accounts, Record{
			"sourceUserId": account.SourceUserID, "email": account.Email, "name": account.Name,
			"existingUserId": account.ExistingUserID, "existingGeneration": account.ExistingGeneration,
			"existingUserCreatedAt": account.ExistingUserCreatedAt, "organizations": organizations,
		})
	}
	return a.resourceETag("legacy-account-invitation", Record{
		"id": session.UserID, "sessionId": session.ID, "csrf": session.CSRF, "generation": session.Generation,
		"userCreatedAt": session.UserCreatedAt, "archiveChecksum": review.ArchiveChecksum, "accounts": accounts,
	})
}

func (a *App) issueLegacyAccountInvitations(cookie string, session Session, archive *migrate.AccountArchive, request legacyAccountInvitationRequest) ([]Record, int, error) {
	a.store.mu.RLock()
	a.mu.Lock()
	actor, status, err := a.legacyAccountAdminLocked(cookie, session)
	passwordHash := str(actor, "passwordHash")
	a.mu.Unlock()
	a.store.mu.RUnlock()
	if err != nil {
		return nil, status, err
	}
	if !checkPassword(passwordHash, request.CurrentPassword) {
		return nil, http.StatusForbidden, errors.New("invalid current password")
	}

	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	now := time.Now().UTC()
	review, err := buildLegacyAccountReview(&a.store.state, archive, request.legacyAccountReviewInput, now)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if revision := a.legacyAccountInvitationRevision(session, review); revision == "" || !hmac.Equal([]byte(revision), []byte(request.Revision)) {
		return nil, http.StatusPreconditionFailed, errors.New("account invitation review changed; inspect current accounts and organizations again")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	actor, status, err = a.legacyAccountAdminLocked(cookie, session)
	if err != nil {
		return nil, status, err
	}
	if str(actor, "passwordHash") != passwordHash {
		return nil, http.StatusUnauthorized, errors.New("administrator account changed; sign in again")
	}

	items := make([]Record, 0, len(review.Accounts))
	status = http.StatusConflict
	err = a.store.updateLocked(func(s *State) error {
		if len(s.Invitations)+len(review.Accounts) > 10000 {
			return errors.New("invitation capacity reached")
		}
		security := s.Security[session.UserID]
		if err := a.consumeMFA(session.UserID, &security, request.Code, now); err != nil {
			status = http.StatusForbidden
			return errLegacyAccountInvitationMFA
		}
		s.Security[session.UserID] = security
		for _, account := range review.Accounts {
			invitation, err := a.enqueueLegacyAccountInvitation(s, session, review.ArchiveChecksum, account, now)
			if err != nil {
				return err
			}
			items = append(items, publicInvitation(s, invitation))
		}
		return nil
	})
	return items, status, err
}

func (a *App) enqueueLegacyAccountInvitation(s *State, session Session, checksum string, account legacyAccountReviewAccount, now time.Time) (Invitation, error) {
	raw := token(32)
	primary := account.Organizations[0]
	invitation := Invitation{
		ID: token(16), Email: account.Email, MemberID: primary.MemberID, MemberAdmin: primary.MemberAdmin,
		InvitedBy: session.UserID, ExistingUserID: account.ExistingUserID, Generation: account.ExistingGeneration,
		TokenHash: digest(raw), CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
		LegacyAccount: &legacyAccountInvitation{
			ArchiveChecksum: checksum, SourceUserID: account.SourceUserID, IssuerCreatedAt: session.UserCreatedAt,
			ExistingUserCreatedAt: account.ExistingUserCreatedAt, Organizations: account.Organizations,
		},
	}
	var text strings.Builder
	text.WriteString("请核对受邀组织与权限\n\n")
	for _, org := range account.Organizations {
		role := "成员"
		if org.MemberAdmin {
			role = "成员管理员"
		}
		fmt.Fprintf(&text, "- %s · %s\n", strings.Join(strings.Fields(org.MemberName), " "), role)
	}
	fmt.Fprintf(&text, "\n请在七天内打开邀请链接确认。已有账号需登录并重新验证密码及已启用的两步验证。\n\n%s\n", mailLink(a.mailConfig.BaseURL, "accept-invitation", raw))
	message := notify.Message{From: a.mailConfig.From, To: invitation.Email, Subject: "MoeDove IXP 组织迁移邀请", Text: text.String()}
	if _, err := s.Outbox.Enqueue("invite:"+invitation.ID, message, a.store.securityKey, now, invitation.ExpiresAt); err != nil {
		return Invitation{}, err
	}
	s.Invitations = append(s.Invitations, invitation)
	for _, org := range account.Organizations {
		auditDetails(s, session.UserID, "invite-legacy-member", "members", org.MemberID, Record{
			"archiveChecksum": checksum, "sourceUserId": account.SourceUserID, "sourceMembershipId": org.SourceMembershipID,
		})
	}
	return invitation, nil
}
