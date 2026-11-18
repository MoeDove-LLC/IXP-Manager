package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

func (a *App) initNotifications(path string) error {
	config, err := notify.LoadConfig(path)
	if err != nil {
		return err
	}
	a.mailConfig = config
	a.mailer = notify.SMTP{Config: config}
	state := a.store.Snapshot()
	for _, entry := range state.Outbox.Entries {
		if _, err := notify.OpenMessage(a.store.securityKey, entry); err != nil {
			return errors.New("cannot decrypt persisted outbox; restore matching security.key and state")
		}
	}
	return a.store.Update(func(s *State) error {
		for _, kind := range []string{"token-expiry", "cleanup"} {
			found := false
			for _, job := range s.Jobs {
				if job.Kind == kind {
					found = true
					break
				}
			}
			if !found {
				s.Jobs = append(s.Jobs, Job{Kind: kind, Status: "idle", NextRunAt: time.Now().UTC()})
			}
		}
		return nil
	})
}
func optionalTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
func publicMail(e notify.Entry) Record {
	lastError := ""
	if e.LastError != "" {
		lastError = "邮件未完成投递，请检查本地 SMTP 配置"
	}
	return Record{"id": e.ID, "to": e.To, "subject": e.Subject, "status": e.Status, "attempts": e.Attempts, "createdAt": e.CreatedAt, "nextAttemptAt": optionalTime(e.NextAttemptAt), "expiresAt": optionalTime(e.ExpiresAt), "lastError": lastError}
}
func (a *App) notifications(w http.ResponseWriter, r *http.Request, user Record) {
	if str(user, "role") != "admin" {
		problem(w, 403, "administrator required")
		return
	}
	if r.URL.Path == "/api/notifications" && r.Method == "GET" {
		items := []Record{}
		entries := a.store.Snapshot().Outbox.Entries
		for i := len(entries) - 1; i >= 0; i-- {
			items = append(items, publicMail(entries[i]))
		}
		respond(w, 200, Record{"deliveryEnabled": a.mailConfig.Enabled, "from": a.mailConfig.From, "items": items})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/notifications/"), "/")
	if len(parts) != 2 || parts[1] != "retry" || r.Method != "POST" {
		problem(w, 404, "notification action not found")
		return
	}
	if !a.mailConfig.Enabled {
		problem(w, 503, "mail delivery is disabled in local configuration")
		return
	}
	err := a.store.Update(func(s *State) error {
		if err := a.patchRetryApplicable(s, parts[0]); err != nil {
			return err
		}
		if err := noteRetryApplicable(s, parts[0]); err != nil {
			return err
		}
		if err := s.Outbox.Retry(parts[0], time.Now()); err != nil {
			return err
		}
		audit(s, str(user, "id"), "retry-email", "notifications", parts[0])
		return nil
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	respond(w, 200, Record{"ok": true})
}
func (a *App) deliverOne(ctx context.Context, now time.Time) error {
	if !a.mailConfig.Enabled || !a.store.Snapshot().Outbox.Ready(now) {
		return nil
	}
	var entry notify.Entry
	claimed := false
	if err := a.store.Update(func(s *State) error {
		for i := range s.Outbox.Entries {
			e := &s.Outbox.Entries[i]
			if (e.Status == "pending" || (e.Status == "sending" && !e.LeaseUntil.After(now))) && !a.mailApplicable(s, *e, now) {
				e.Status = "expired"
				e.Lease = ""
				e.LeaseUntil = time.Time{}
			}
		}
		entry, claimed = s.Outbox.Claim(now)
		return nil
	}); err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	msg, err := notify.OpenMessage(a.store.securityKey, entry)
	if err == nil {
		// Recheck subscriptions or patch allocation/attachment authority after
		// claiming/decrypting and immediately
		// before handing the immutable message to the transport. An SMTP message
		// already handed off cannot be recalled if authority changes in flight.
		if strings.HasPrefix(entry.Key, "note:") || strings.HasPrefix(entry.Key, "patch:") {
			s := a.store.Snapshot()
			if !a.mailApplicable(&s, entry, time.Now()) {
				return a.store.Update(func(s *State) error {
					for i := range s.Outbox.Entries {
						e := &s.Outbox.Entries[i]
						if e.ID == entry.ID && e.Status == "sending" && e.Lease == entry.Lease {
							e.Status, e.Lease, e.LeaseUntil = "expired", "", time.Time{}
						}
					}
					return nil
				})
			}
		}
		sendContext := ctx
		cancel := func() {}
		if !entry.ExpiresAt.IsZero() {
			sendContext, cancel = context.WithDeadline(ctx, entry.ExpiresAt)
		}
		err = a.mailer.Send(sendContext, msg)
		cancel()
	}
	success := err == nil
	return a.store.Update(func(s *State) error { return s.Outbox.Finish(entry.ID, entry.Lease, success, time.Now().UTC()) })
}

func (a *App) mailApplicable(s *State, e notify.Entry, now time.Time) bool {
	if strings.HasPrefix(e.Key, "patch:") {
		return a.patchMailApplicable(s, e, now)
	}
	return mailStillApplicable(s, e, now)
}
func (a *App) StartBackground(parent context.Context) {
	a.backgroundMu.Lock()
	defer a.backgroundMu.Unlock()
	if a.backgroundDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	a.backgroundCancel = cancel
	a.backgroundDone = make(chan struct{})
	go func() {
		defer close(a.backgroundDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			now := time.Now().UTC()
			_ = a.runDueJob(now)
			_ = a.deliverOne(ctx, now)
			_ = a.atlasTick(ctx, now)
			_ = a.sweepLookingGlass(now)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (a *App) stopBackground() {
	a.backgroundMu.Lock()
	cancel, done := a.backgroundCancel, a.backgroundDone
	a.backgroundMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// Recheck security-sensitive mail before delivery: revoked invitations, used
// reset links and removed API credentials must not generate obsolete messages.
func mailStillApplicable(s *State, e notify.Entry, now time.Time) bool {
	if strings.HasPrefix(e.Key, "note:") {
		return noteMailApplicable(s, e)
	}
	if strings.HasPrefix(e.Key, "peering:") {
		return peeringMailApplicable(s, e, now)
	}
	if strings.HasPrefix(e.Key, "reset:") {
		id := strings.TrimPrefix(e.Key, "reset:")
		for _, request := range s.Recovery {
			if request.ID == id {
				return request.UsedAt.IsZero() && request.ExpiresAt.After(now) && find(s, "users", request.UserID) != nil && s.Security[request.UserID].Generation == request.Generation
			}
		}
		return false
	}
	if strings.HasPrefix(e.Key, "invite:") {
		id := strings.TrimPrefix(e.Key, "invite:")
		for _, i := range s.Invitations {
			if i.ID != id {
				continue
			}
			if invitationStatus(i, now) != "pending" || !canInvite(s, find(s, "users", i.InvitedBy), i.MemberID) {
				return false
			}
			if i.ExistingUserID != "" {
				user := find(s, "users", i.ExistingUserID)
				return user != nil && strings.EqualFold(str(user, "email"), i.Email) && s.Security[i.ExistingUserID].Generation == i.Generation
			}
			return true
		}
		return false
	}
	if strings.HasPrefix(e.Key, "token-expiry:") {
		parts := strings.SplitN(strings.TrimPrefix(e.Key, "token-expiry:"), ":", 2)
		if len(parts) != 2 {
			return false
		}
		for userID, security := range s.Security {
			if find(s, "users", userID) == nil {
				continue
			}
			for _, credential := range security.Tokens {
				if credential.ID == parts[0] && credential.ExpiresAt.UTC().Format(time.RFC3339) == parts[1] && credential.ExpiresAt.After(now) {
					return true
				}
			}
		}
		return false
	}
	return true
}
