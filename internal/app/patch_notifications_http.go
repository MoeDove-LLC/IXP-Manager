package app

import (
	"crypto/subtle"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/moedove/ixp-manager/internal/notify"
)

const patchNotificationReviewLifetime = 10 * time.Minute

type patchNotificationRequest struct {
	patchNotificationDraft
	ReviewToken string `json:"reviewToken"`
	ExpiresAt   string `json:"expiresAt"`
	Acknowledge bool   `json:"acknowledge"`
}

func normalizedPatchDraft(input patchNotificationDraft, c patchNotificationContext) (patchNotificationDraft, []Record, error) {
	d := input
	d.Subject = strings.TrimSpace(d.Subject)
	d.Text = strings.ReplaceAll(d.Text, "\r\n", "\n")
	if d.Type != c.Type || d.Revision != c.Revision {
		return d, nil, errors.New("patch context changed; reload and review again")
	}
	if len(d.Subject) < 1 || len(d.Subject) > 300 || strings.ContainsAny(d.Subject, "\x00\r\n") || !utf8.ValidString(d.Subject) || len(d.Text) < 1 || len(d.Text) > 16384 || strings.ContainsRune(d.Text, 0) || !utf8.ValidString(d.Text) {
		return d, nil, errors.New("subject or plain message text is invalid")
	}
	if len(d.To) < 1 || len(d.To)+len(d.Cc)+len(d.Bcc) > notify.MaxRecipients || len(d.DocumentIDs) > 3 || len(d.RecipientSources) > notify.MaxRecipients {
		return d, nil, errors.New("recipient or attachment selection exceeds limits")
	}
	seen := map[string]bool{}
	headers := map[string][]string{}
	recipients := []Record{}
	for _, group := range []struct {
		name   string
		values *[]string
	}{{"to", &d.To}, {"cc", &d.Cc}, {"bcc", &d.Bcc}} {
		out := []string{}
		for _, raw := range *group.values {
			email := strings.ToLower(strings.TrimSpace(raw))
			if _, valid := noteMailbox(email); !valid || len(email) > 254 || seen[email] {
				return d, nil, errors.New("use distinct bare email addresses across To, CC and BCC")
			}
			seen[email] = true
			out = append(out, email)
		}
		*group.values = out
		headers[group.name] = out
	}
	sources := map[string]patchRecipientSource{}
	for _, source := range d.RecipientSources {
		source.Email = strings.ToLower(strings.TrimSpace(source.Email))
		key := source.Header + ":" + source.Email
		if !contains(headers[source.Header], source.Email) || source.SourceID == "" {
			return d, nil, errors.New("recipient source does not match the selected address")
		}
		if _, exists := sources[key]; exists {
			return d, nil, errors.New("repeated recipient source")
		}
		found := false
		for _, option := range c.RecipientOptions {
			found = found || option.ID == source.SourceID && option.Email == source.Email
		}
		if !found {
			return d, nil, errors.New("recipient source changed; reload and review again")
		}
		sources[key] = source
	}
	d.RecipientSources = []patchRecipientSource{}
	for _, header := range []string{"to", "cc", "bcc"} {
		for _, email := range headers[header] {
			row := Record{"header": header, "email": email, "label": "手工指定", "manual": true}
			if source, ok := sources[header+":"+email]; ok {
				d.RecipientSources = append(d.RecipientSources, source)
				row["sourceId"], row["manual"] = source.SourceID, false
				for _, option := range c.RecipientOptions {
					if option.ID == source.SourceID {
						row["label"] = option.Label
					}
				}
			}
			recipients = append(recipients, row)
		}
	}
	d.DocumentIDs = append([]string{}, d.DocumentIDs...)
	sort.Strings(d.DocumentIDs)
	last := ""
	for _, id := range d.DocumentIDs {
		if id == last || !identifier.MatchString(id) {
			return d, nil, errors.New("invalid document selection")
		}
		last = id
		found := false
		for _, doc := range c.Documents {
			found = found || str(doc, "id") == id
		}
		if !found {
			return d, nil, errors.New("attachment is not visible to the current member allocation")
		}
	}
	if len(d.DocumentIDs) > 0 && d.Type != "info" && d.Type != "cease" {
		return d, nil, errors.New("document attachments are available for information and cease notifications")
	}
	if d.LOAID != "" {
		found := false
		for _, letter := range c.LOAs {
			found = found || str(letter, "id") == d.LOAID
		}
		if !found {
			return d, nil, errors.New("selected LOA is no longer current")
		}
	}
	if d.Type == "loa" && d.LOAID == "" {
		return d, nil, errors.New("loa-required")
	}
	return d, recipients, nil
}

func (a *App) patchMessage(s *State, d patchNotificationDraft) (notify.Message, []Record, time.Time, error) {
	m := notify.Message{From: a.mailConfig.From, To: d.To[0], ToAdditional: append([]string{}, d.To[1:]...), Cc: append([]string{}, d.Cc...), Bcc: append([]string{}, d.Bcc...), Subject: d.Subject, Text: d.Text}
	attachments := []Record{}
	expires := time.Now().UTC().Add(24 * time.Hour)
	total := int64(0)
	add := func(kind, id, name, mediaType, blob string, size int64, checksum string) error {
		mediaType, _, parseErr := mime.ParseMediaType(mediaType)
		if parseErr != nil {
			return errors.New("attachment media type is unavailable")
		}
		if size < 1 || size > notify.MaxAttachmentBytes || total+size > notify.MaxAttachmentBytes {
			return errors.New("selected attachments exceed 2 MiB")
		}
		file, err := a.openBlob(blob)
		if err != nil {
			return errors.New("attachment is unavailable")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, size+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) != size || patchBytesHash(data) != checksum {
			return errors.New("attachment integrity check failed")
		}
		if kind == "loa" && !strings.HasPrefix(string(data), "%PDF-") {
			return errors.New("LOA PDF integrity check failed")
		}
		m.Attachments = append(m.Attachments, notify.Attachment{Name: patchMailName(name), MediaType: mediaType, Data: data})
		attachments = append(attachments, Record{"kind": kind, "id": id, "name": patchMailName(name), "mediaType": mediaType, "size": size})
		total += size
		return nil
	}
	if d.LOAID != "" {
		letter := loaByID(s, d.LOAID)
		if letter == nil || loaStatus(s, *letter, time.Now()) != "issued" {
			return m, nil, expires, errors.New("selected LOA is no longer current")
		}
		expiry, err := time.Parse(time.RFC3339Nano, letter.ExpiresAt)
		if err != nil {
			return m, nil, expires, errors.New("LOA expiry unavailable")
		}
		if expiry.Before(expires) {
			expires = expiry
		}
		if err = add("loa", letter.ID, "LOA-"+letter.ID+".pdf", "application/pdf", letter.PDFBlobID, letter.PDFSize, letter.Checksum); err != nil {
			return m, nil, expires, err
		}
	}
	for _, id := range d.DocumentIDs {
		doc := documentByID(s, id)
		if doc == nil {
			return m, nil, expires, errors.New("selected attachment is unavailable")
		}
		if err := add("document", id, doc.Name, doc.MediaType, doc.BlobID, doc.Size, doc.Checksum); err != nil {
			return m, nil, expires, err
		}
	}
	check := m
	if check.From == "" && !a.mailConfig.Enabled {
		check.From = "notifications@example.invalid"
	}
	if err := notify.ValidateMessage(check); err != nil {
		return m, nil, expires, errors.New("message or selected attachments are invalid")
	}
	return m, attachments, expires, nil
}
func (a *App) patchSessionBinding(session Session) string {
	return a.coreHMAC("patch-notification-session", session)
}
func (a *App) patchRequestDigest(d patchNotificationDraft, expires string, session Session) string {
	return a.coreHMAC("patch-notification-request", Record{"draft": d, "expires": expires, "session": a.patchSessionBinding(session)})
}
func (a *App) patchReviewToken(nonce string, d patchNotificationDraft, expires string, session Session) string {
	return nonce + "." + a.coreHMAC("patch-notification-review", Record{"nonce": nonce, "request": a.patchRequestDigest(d, expires, session)})
}
func patchNotificationAllowed(c patchNotificationContext) bool {
	for _, reason := range c.BlockingReasons {
		if reason != "delivery-disabled" && reason != "loa-required" {
			return false
		}
	}
	return true
}

func (a *App) patchNotifications(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 || len(parts) > 6 || parts[0] != "api" || parts[1] != "patching" || parts[2] != "ports" || parts[4] != "notifications" {
		return false
	}
	if session.API || str(user, "role") != "admin" {
		problem(w, 403, "browser administrator required for patch notification mail")
		return true
	}
	id := parts[3]
	if len(parts) == 5 && r.Method == "GET" {
		kind := r.URL.Query().Get("type")
		if kind == "" {
			kind = "connect"
		}
		a.store.mu.RLock()
		actor, err := a.atlasActor(&a.store.state, user, session, "admin")
		if err != nil || a.store.closed {
			a.store.mu.RUnlock()
			problem(w, 401, "notification authority changed; sign in again")
			return true
		}
		_ = actor
		context, err := a.patchNotificationContext(&a.store.state, id, kind, time.Now())
		_, authErr := a.atlasActor(&a.store.state, user, session, "admin")
		a.store.mu.RUnlock()
		if authErr != nil {
			problem(w, 401, "notification authority changed; sign in again")
		} else if err != nil {
			problem(w, 409, err.Error())
		} else {
			respond(w, 200, context)
		}
		return true
	}
	if len(parts) != 6 || r.Method != "POST" || (parts[5] != "review" && parts[5] != "queue") {
		problem(w, 405, "notification method is not supported")
		return true
	}
	var input patchNotificationRequest
	if !decode(w, r, &input) {
		return true
	}
	if parts[5] == "review" {
		a.store.mu.RLock()
		defer a.store.mu.RUnlock()
		if _, err := a.atlasActor(&a.store.state, user, session, "admin"); err != nil || a.store.closed {
			problem(w, 401, "notification authority changed; sign in again")
			return true
		}
		context, err := a.patchNotificationContext(&a.store.state, id, input.Type, time.Now())
		if err != nil || !patchNotificationAllowed(context) {
			problem(w, 409, "patch allocation or status no longer allows this notification")
			return true
		}
		draft, recipients, err := normalizedPatchDraft(input.patchNotificationDraft, context)
		if err != nil {
			problem(w, 409, err.Error())
			return true
		}
		_, attachments, _, err := a.patchMessage(&a.store.state, draft)
		if err != nil {
			problem(w, 409, err.Error())
			return true
		}
		if _, err = a.atlasActor(&a.store.state, user, session, "admin"); err != nil || r.Context().Err() != nil {
			problem(w, 401, "notification authority changed; sign in again")
			return true
		}
		expires := time.Now().UTC().Add(patchNotificationReviewLifetime).Format(time.RFC3339Nano)
		review := a.patchReviewToken(token(16), draft, expires, session)
		respond(w, 200, Record{"reviewToken": review, "expiresAt": expires, "draft": draft, "to": draft.To, "cc": draft.Cc, "bcc": draft.Bcc, "recipients": recipients, "attachments": attachments, "subject": draft.Subject, "text": draft.Text, "deliveryEnabled": a.mailConfig.Enabled, "blockingReasons": context.BlockingReasons})
		return true
	}
	if !input.Acknowledge {
		problem(w, 400, "explicit acknowledgement of the reviewed mail is required")
		return true
	}
	if len(input.ReviewToken) > 150 || len(input.ExpiresAt) > 64 {
		problem(w, 400, "invalid review token")
		return true
	}
	if !a.mailConfig.Enabled {
		problem(w, 503, "mail delivery is disabled in local configuration")
		return true
	}
	status := 409
	var result Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.atlasActor(s, user, session, "admin")
		if err != nil {
			status = 401
			return errors.New("notification authority changed; sign in again")
		}
		// Same reviewed request after an unknown HTTP outcome is idempotent even
		// if the allocation has changed since it was queued. It cannot send again.
		if s.PatchNotifications != nil {
			for _, receipt := range s.PatchNotifications.Receipts {
				if receipt.ReviewDigest == digest(input.ReviewToken) {
					if receipt.PortID != id || receipt.Actor != noteEntity(actor) || receipt.Generation != session.Generation || receipt.SessionBinding != a.patchSessionBinding(session) || receipt.RequestDigest != a.patchRequestDigest(input.patchNotificationDraft, input.ExpiresAt, session) {
						return errors.New("review does not match the original queued request")
					}
					result = patchNotificationReceiptView(s, receipt, true)
					status = 200
					return nil
				}
			}
		}
		now := time.Now().UTC()
		expires, err := time.Parse(time.RFC3339Nano, input.ExpiresAt)
		if err != nil || !now.Before(expires) || expires.Sub(now) > patchNotificationReviewLifetime {
			return errors.New("review expired; reload and preview again")
		}
		context, err := a.patchNotificationContext(s, id, input.Type, now)
		if err != nil || !patchNotificationAllowed(context) {
			return errors.New("patch allocation or status no longer allows this notification")
		}
		draft, _, err := normalizedPatchDraft(input.patchNotificationDraft, context)
		if err != nil {
			return err
		}
		tokenParts := strings.Split(input.ReviewToken, ".")
		if len(tokenParts) != 2 || !identifier.MatchString(tokenParts[0]) || !externalDigest(tokenParts[1]) || subtle.ConstantTimeCompare([]byte(input.ReviewToken), []byte(a.patchReviewToken(tokenParts[0], draft, input.ExpiresAt, session))) != 1 {
			return errors.New("review changed; reload and preview again")
		}
		message, _, mailExpiry, err := a.patchMessage(s, draft)
		if err != nil {
			return err
		}
		if _, err = a.atlasActor(s, user, session, "admin"); err != nil || r.Context().Err() != nil {
			status = 401
			return errors.New("notification authority changed; sign in again")
		}
		entry, err := s.Outbox.Enqueue("patch:"+tokenParts[0], message, a.store.securityKey, now, mailExpiry)
		if err != nil {
			return errors.New("notification could not be queued; check outbox capacity")
		}
		if s.PatchNotifications == nil {
			s.PatchNotifications = &PatchNotificationState{Version: 1}
		}
		receipt := PatchNotificationReceipt{ID: tokenParts[0], OutboxID: entry.ID, PortID: id, Type: draft.Type, Actor: noteEntity(actor), Generation: session.Generation, ContextRevision: context.Revision, ReviewDigest: digest(input.ReviewToken), RequestDigest: a.patchRequestDigest(draft, input.ExpiresAt, session), SessionBinding: a.patchSessionBinding(session), CreatedAt: now, RecipientCount: len(draft.To) + len(draft.Cc) + len(draft.Bcc), BCCCount: len(draft.Bcc)}
		s.PatchNotifications.Receipts = append(s.PatchNotifications.Receipts, receipt)
		prunePatchNotifications(s)
		if err = validatePatchNotifications(s); err != nil {
			return err
		}
		auditDetails(s, str(actor, "id"), "queue-notification", "patch-ports", id, Record{"notificationId": receipt.ID, "type": receipt.Type, "recipientCount": receipt.RecipientCount, "bccCount": receipt.BCCCount})
		result = patchNotificationReceiptView(s, receipt, false)
		status = 201
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
	} else {
		respond(w, status, result)
	}
	return true
}
func patchNotificationReceiptView(s *State, r PatchNotificationReceipt, replayed bool) Record {
	items := []Record{}
	for _, e := range s.Outbox.Entries {
		if e.ID == r.OutboxID {
			items = append(items, Record{"id": e.ID, "to": e.To, "status": e.Status})
		}
	}
	return Record{"notificationId": r.ID, "status": "queued", "items": items, "replayed": replayed, "recipientCount": r.RecipientCount, "bccCount": r.BCCCount}
}
