package app

import (
	"crypto/hmac"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/moedove/ixp-manager/internal/notify"
)

const memberWelcomeReviewLifetime = 10 * time.Minute
const memberWelcomeDeliveryLifetime = 24 * time.Hour
const memberWelcomeRequestLimit = 512 << 10

var errMemberWelcomeReplay = errors.New("member welcome replay")

type memberWelcomeRequest struct {
	memberWelcomeDraft
	ReviewToken string `json:"reviewToken,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	Acknowledge bool   `json:"acknowledge,omitempty"`
}

func (a *App) memberWelcomeRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	path, matches := strings.CutPrefix(r.URL.Path, "/api/members/")
	if !matches {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "welcome-email" {
		return false
	}
	if session.API || str(user, "role") != "admin" {
		problem(w, http.StatusForbidden, "browser administrator required for member welcome mail")
		return true
	}
	if !identifier.MatchString(parts[0]) || len(parts) > 3 || len(parts) == 3 && parts[2] != "review" && parts[2] != "queue" {
		problem(w, http.StatusNotFound, "member welcome endpoint not found")
		return true
	}
	method := http.MethodGet
	if len(parts) == 3 {
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}
	if len(parts) == 3 && parts[2] == "queue" {
		var request memberWelcomeRequest
		r.Body = http.MaxBytesReader(w, r.Body, memberWelcomeRequestLimit)
		if !decode(w, r, &request) {
			return true
		}
		result, status, err := a.queueMemberWelcome(r, user, session, parts[0], request)
		if err != nil {
			problem(w, status, err.Error())
		} else {
			respond(w, status, result)
		}
		return true
	}
	var draft *memberWelcomeDraft
	if len(parts) == 3 {
		var input memberWelcomeDraft
		r.Body = http.MaxBytesReader(w, r.Body, memberWelcomeRequestLimit)
		if !decode(w, r, &input) {
			return true
		}
		draft = &input
	}
	result, status, err := a.readMemberWelcome(r, user, session, parts[0], draft)
	if err != nil {
		problem(w, status, err.Error())
	} else {
		respond(w, status, result)
	}
	return true
}

func (a *App) readMemberWelcome(r *http.Request, user Record, session Session, memberID string, draft *memberWelcomeDraft) (any, int, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	s := &a.store.state
	if _, err := a.atlasActor(s, user, session, "admin"); err != nil || a.store.closed {
		return nil, http.StatusUnauthorized, errors.New("welcome mail authority changed; sign in again")
	}
	context, err := a.buildMemberWelcomeContext(s, memberID)
	if err != nil {
		return nil, http.StatusConflict, err
	}
	if _, err := a.atlasActor(s, user, session, "admin"); err != nil || r.Context().Err() != nil {
		return nil, http.StatusUnauthorized, errors.New("welcome mail authority changed; sign in again")
	}
	if draft == nil {
		return context, http.StatusOK, nil
	}
	checked, err := normalizeMemberWelcomeDraft(*draft, context)
	if err != nil {
		return nil, http.StatusConflict, err
	}
	expires := time.Now().UTC().Add(memberWelcomeReviewLifetime).Format(time.RFC3339Nano)
	proof := a.memberWelcomeReview(token(16), memberID, checked, expires, a.memberWelcomeSessionBinding(session))
	return Record{"reviewToken": proof, "expiresAt": expires, "draft": checked, "from": context.From, "deliveryEnabled": context.DeliveryEnabled}, http.StatusOK, nil
}

func normalizeMemberWelcomeDraft(draft memberWelcomeDraft, context memberWelcomeContext) (memberWelcomeDraft, error) {
	if draft.Revision != context.Draft.Revision || !externalDigest(draft.Revision) {
		return draft, errors.New("member or network data changed; reload and review again")
	}
	if len(context.BlockingReasons) != 0 {
		return draft, errors.New("current member and recipients must allow this welcome mail")
	}
	if len(draft.To) == 0 || len(draft.To)+len(draft.Cc)+len(draft.Bcc) > notify.MaxRecipients {
		return draft, errors.New("welcome mail requires one to ten total recipients")
	}
	draft.Subject = strings.TrimSpace(draft.Subject)
	draft.Text = strings.ReplaceAll(strings.ReplaceAll(draft.Text, "\r\n", "\n"), "\r", "\n")
	if draft.Subject == "" || len(draft.Subject) > 300 || strings.ContainsAny(draft.Subject, "\x00\r\n") || !utf8.ValidString(draft.Subject) ||
		strings.TrimSpace(draft.Text) == "" || len(draft.Text) > 64<<10 || strings.ContainsRune(draft.Text, 0) || !utf8.ValidString(draft.Text) {
		return draft, errors.New("subject or plain message text is invalid")
	}
	allowed := map[string]bool{}
	member := map[string]bool{}
	for _, option := range context.RecipientOptions {
		allowed[option.Email] = true
		if option.Kind != "operations" {
			member[option.Email] = true
		}
	}
	seen := map[string]bool{}
	memberTo := false
	for i, group := range []*[]string{&draft.To, &draft.Cc, &draft.Bcc} {
		normalized := make([]string, 0, len(*group))
		for _, raw := range *group {
			address := strings.ToLower(strings.TrimSpace(raw))
			if _, valid := noteMailbox(address); !valid || !allowed[address] || seen[address] {
				return draft, errors.New("use distinct current member or support addresses across To, CC and BCC")
			}
			seen[address] = true
			memberTo = memberTo || i == 0 && member[address]
			normalized = append(normalized, address)
		}
		*group = normalized
	}
	if !memberTo {
		return draft, errors.New("a current member recipient is required in To")
	}
	if context.DeliveryEnabled {
		if err := notify.ValidateMessage(memberWelcomeMessage(context.From, draft)); err != nil {
			return draft, errors.New("welcome mail cannot be composed with the current sender configuration")
		}
	}
	return draft, nil
}

func memberWelcomeMessage(from string, draft memberWelcomeDraft) notify.Message {
	return notify.Message{From: from, To: draft.To[0], ToAdditional: draft.To[1:], Cc: draft.Cc, Bcc: draft.Bcc, Subject: draft.Subject, Text: draft.Text}
}

func (a *App) memberWelcomeSessionBinding(session Session) string {
	return a.coreHMAC("member-welcome-session", Record{"user": session.UserID, "createdAt": session.UserCreatedAt, "session": session.ID, "csrf": session.CSRF, "generation": session.Generation})
}

func (a *App) memberWelcomeReview(nonce, id string, draft memberWelcomeDraft, expires, binding string) string {
	return nonce + "." + a.coreHMAC("member-welcome-review", Record{"nonce": nonce, "memberId": id, "draft": draft, "expiresAt": expires, "binding": binding})
}

func (a *App) queueMemberWelcome(r *http.Request, user Record, session Session, memberID string, request memberWelcomeRequest) (Record, int, error) {
	if !request.Acknowledge {
		return nil, http.StatusBadRequest, errors.New("explicit acknowledgement of the reviewed mail is required")
	}
	nonce, mac, separated := strings.Cut(request.ReviewToken, ".")
	if !separated || !identifier.MatchString(nonce) || !externalDigest(mac) || len(request.ExpiresAt) > 64 {
		return nil, http.StatusBadRequest, errors.New("invalid welcome mail review token")
	}
	if !a.mailConfig.Enabled {
		return nil, http.StatusServiceUnavailable, errors.New("mail delivery is disabled in local configuration")
	}
	binding := a.memberWelcomeSessionBinding(session)
	reviewDigest := digest(request.ReviewToken)
	requestDigest := a.coreHMAC("member-welcome-request", Record{"draft": request.memberWelcomeDraft, "expiresAt": request.ExpiresAt, "binding": binding})
	status := http.StatusConflict
	var view Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.atlasActor(s, user, session, "admin")
		if err != nil || r.Context().Err() != nil {
			status = http.StatusUnauthorized
			return errors.New("welcome mail authority changed; sign in again")
		}
		for _, receipt := range s.WelcomeMail {
			if receipt.ID != nonce {
				continue
			}
			if receipt.Member.ID != memberID || receipt.Actor != noteEntity(actor) || receipt.Generation != session.Generation ||
				receipt.ReviewDigest != reviewDigest || receipt.RequestDigest != requestDigest || receipt.SessionBinding != binding {
				return errors.New("review does not match the original queued message")
			}
			view = memberWelcomeReceiptView(s, receipt, true)
			status = http.StatusOK
			return errMemberWelcomeReplay
		}
		now := time.Now().UTC()
		expires, err := time.Parse(time.RFC3339Nano, request.ExpiresAt)
		if err != nil || !expires.After(now) || expires.Sub(now) > memberWelcomeReviewLifetime {
			return errors.New("welcome mail review expired; preview again")
		}
		context, err := a.buildMemberWelcomeContext(s, memberID)
		if err != nil {
			return err
		}
		checked, err := normalizeMemberWelcomeDraft(request.memberWelcomeDraft, context)
		if err != nil {
			return err
		}
		if !hmac.Equal([]byte(request.ReviewToken), []byte(a.memberWelcomeReview(nonce, memberID, checked, request.ExpiresAt, binding))) {
			return errors.New("welcome mail review changed; preview again")
		}
		if _, err := a.atlasActor(s, user, session, "admin"); err != nil || r.Context().Err() != nil {
			status = http.StatusUnauthorized
			return errors.New("welcome mail authority changed; sign in again")
		}
		entry, err := s.Outbox.Enqueue("welcome:"+nonce, memberWelcomeMessage(context.From, checked), a.store.securityKey, now, now.Add(memberWelcomeDeliveryLifetime))
		if err != nil {
			return errors.New("welcome mail could not be queued; check outbox capacity")
		}
		receipt := MemberWelcomeReceipt{
			ID: nonce, OutboxID: entry.ID, Member: noteEntity(find(s, "members", memberID)), Actor: noteEntity(actor), Generation: session.Generation,
			ContextRevision: checked.Revision, ReviewDigest: reviewDigest, RequestDigest: requestDigest, SessionBinding: binding, CreatedAt: now,
			RecipientCount: len(checked.To) + len(checked.Cc) + len(checked.Bcc), BCCCount: len(checked.Bcc),
		}
		s.WelcomeMail = append(s.WelcomeMail, receipt)
		auditDetails(s, session.UserID, "queue-member-welcome", "members", memberID, Record{"notificationId": nonce, "recipientCount": receipt.RecipientCount, "bccCount": receipt.BCCCount})
		view = memberWelcomeReceiptView(s, receipt, false)
		status = http.StatusCreated
		return nil
	})
	if errors.Is(err, errMemberWelcomeReplay) {
		err = nil
	}
	if err != nil && status == http.StatusCreated {
		status = http.StatusInternalServerError
		err = errors.New("welcome mail could not be saved")
	}
	return view, status, err
}

func memberWelcomeReceiptView(s *State, receipt MemberWelcomeReceipt, replayed bool) Record {
	status := "unknown"
	for _, entry := range s.Outbox.Entries {
		if entry.ID == receipt.OutboxID {
			status = entry.Status
			break
		}
	}
	return Record{"id": receipt.ID, "outboxId": receipt.OutboxID, "status": status, "replayed": replayed}
}
