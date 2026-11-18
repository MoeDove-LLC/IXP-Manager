package app

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

type Job struct {
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	NextRunAt  time.Time `json:"nextRunAt"`
	LastRunAt  time.Time `json:"lastRunAt,omitempty"`
	LastError  string    `json:"lastError,omitempty"`
	Lease      string    `json:"lease,omitempty"`
	LeaseUntil time.Time `json:"leaseUntil,omitempty"`
}

func jobLabel(kind string) string {
	switch kind {
	case "token-expiry":
		return "API 令牌到期提醒"
	case "cleanup":
		return "过期安全数据清理"
	default:
		return ""
	}
}
func (a *App) jobs(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if session.API || str(user, "role") != "admin" {
		problem(w, 403, "administrator required")
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/jobs" {
		a.store.mu.RLock()
		_, err := a.atlasActor(&a.store.state, user, session, "admin")
		if a.store.closed || err != nil {
			a.store.mu.RUnlock()
			problem(w, 403, "administrator authority changed")
			return
		}
		jobs := slices.Clone(a.store.state.Jobs)
		a.store.mu.RUnlock()
		now := time.Now()
		items := []Record{}
		for _, j := range jobs {
			label := jobLabel(j.Kind)
			if label == "" {
				continue
			}
			lastError := ""
			if j.LastError != "" {
				lastError = "任务未完成，请检查配置并重试"
			}
			var leaseUntil any
			if j.Status == "running" {
				leaseUntil = optionalTime(j.LeaseUntil)
			}
			items = append(items, Record{
				"kind": j.Kind, "label": label, "status": j.Status, "health": jobHealth(j, now),
				"nextRunAt": j.NextRunAt, "lastRunAt": optionalTime(j.LastRunAt), "lastError": lastError, "leaseUntil": leaseUntil,
			})
		}
		respond(w, 200, Record{"items": items, "scheduler": a.schedulerHealth(now), "checkedAt": now.UTC()})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/jobs/"), "/")
	if len(parts) != 2 || parts[1] != "run" || r.Method != "POST" || jobLabel(parts[0]) == "" {
		problem(w, 404, "unknown fixed job kind")
		return
	}
	status := http.StatusConflict
	err := a.store.Update(func(s *State) error {
		if _, err := a.atlasActor(s, user, session, "admin"); err != nil {
			status = http.StatusForbidden
			return errors.New("administrator authority changed")
		}
		for i := range s.Jobs {
			j := &s.Jobs[i]
			if j.Kind != parts[0] {
				continue
			}
			now := time.Now().UTC()
			if j.Status == "running" && j.LeaseUntil.After(now) {
				return errors.New("job is already running")
			}
			j.NextRunAt = now
			j.Status = "idle"
			j.Lease = ""
			j.LeaseUntil = time.Time{}
			audit(s, str(user, "id"), "queue-job", "jobs", j.Kind)
			return nil
		}
		return errors.New("job not found")
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, Record{"ok": true})
}

func jobHealth(j Job, now time.Time) string {
	if j.Status == "running" {
		if !j.LeaseUntil.After(now) {
			return "lease-expired"
		}
		return "running"
	}
	if j.Status != "idle" {
		return "unknown"
	}
	if j.NextRunAt.After(now) {
		return "scheduled"
	}
	if now.Sub(j.NextRunAt) > schedulerStallAfter {
		return "overdue"
	}
	return "due"
}

func fixedJobReady(j Job, now time.Time) bool {
	return jobLabel(j.Kind) != "" && (j.Status == "idle" && !j.NextRunAt.After(now) || j.Status == "running" && !j.LeaseUntil.After(now))
}

func (a *App) runDueJob(now time.Time) error {
	state := a.store.Snapshot()
	ready := false
	for _, j := range state.Jobs {
		if fixedJobReady(j, now) {
			ready = true
			break
		}
	}
	if !ready {
		return nil
	}
	var selected Job
	claimed := false
	if err := a.store.Update(func(s *State) error {
		for i := range s.Jobs {
			j := &s.Jobs[i]
			if !fixedJobReady(*j, now) {
				continue
			}
			j.Status = "running"
			j.Lease = token(24)
			j.LeaseUntil = now.Add(time.Minute)
			selected = *j
			claimed = true
			return nil
		}
		return nil
	}); err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	err := a.store.Update(func(s *State) error {
		for i := range s.Jobs {
			j := &s.Jobs[i]
			if j.Kind != selected.Kind {
				continue
			}
			if j.Status != "running" || j.Lease != selected.Lease {
				return errors.New("job lease changed")
			}
			nextInterval := time.Hour
			switch j.Kind {
			case "token-expiry":
				if a.mailConfig.Enabled {
					queued := 0
				expiryScan:
					for userID, security := range s.Security {
						user := find(s, "users", userID)
						if user == nil {
							continue
						}
						for _, credential := range security.Tokens {
							if credential.ExpiresAt.After(now) && !credential.ExpiresAt.After(now.Add(7*24*time.Hour)) {
								key := "token-expiry:" + credential.ID + ":" + credential.ExpiresAt.UTC().Format(time.RFC3339)
								exists := false
								for _, entry := range s.Outbox.Entries {
									if entry.Key == key {
										exists = true
										break
									}
								}
								if exists {
									continue
								}
								if queued >= 100 || len(s.Outbox.Entries) >= notify.MaxEntries {
									nextInterval = time.Minute
									break expiryScan
								}
								message := notify.Message{From: a.mailConfig.From, To: str(user, "email"), Subject: "MoeDove IXP：API 令牌即将到期", Text: fmt.Sprintf("您的 API 令牌“%s”将于 %s 到期。\n请登录控制台检查自动化任务，按需创建新令牌并撤销旧令牌。\n\n此邮件不会包含令牌内容。", credential.Name, credential.ExpiresAt.UTC().Format(time.RFC3339))}
								if _, err := s.Outbox.Enqueue(key, message, a.store.securityKey, now, credential.ExpiresAt); err != nil {
									return err
								}
								queued++
							}
						}
					}
				}
			case "cleanup":
				for id, security := range s.Security {
					tokens := security.Tokens[:0]
					for _, credential := range security.Tokens {
						if credential.ExpiresAt.After(now) {
							tokens = append(tokens, credential)
						}
					}
					security.Tokens = tokens
					if !security.PendingUntil.IsZero() && !security.PendingUntil.After(now) {
						security.PendingSecret = ""
						security.PendingUntil = time.Time{}
					}
					s.Security[id] = security
				}
				requests := s.Recovery[:0]
				for _, request := range s.Recovery {
					if request.ExpiresAt.After(now.Add(-24 * time.Hour)) {
						requests = append(requests, request)
					}
				}
				s.Recovery = requests
				invitations := s.Invitations[:0]
				for _, invitation := range s.Invitations {
					if invitation.ExpiresAt.After(now.Add(-30 * 24 * time.Hour)) {
						invitations = append(invitations, invitation)
					}
				}
				s.Invitations = invitations
				s.Outbox.Prune(now)
			default:
				return errors.New("unsupported job")
			}
			j.LastRunAt = now
			j.NextRunAt = now.Add(nextInterval)
			j.Status = "idle"
			j.Lease = ""
			j.LeaseUntil = time.Time{}
			j.LastError = ""
			return nil
		}
		return errors.New("job disappeared")
	})
	if err != nil {
		_ = a.store.Update(func(s *State) error {
			for i := range s.Jobs {
				j := &s.Jobs[i]
				if j.Kind == selected.Kind && j.Lease == selected.Lease {
					j.Status = "idle"
					j.Lease = ""
					j.LeaseUntil = time.Time{}
					j.NextRunAt = now.Add(5 * time.Minute)
					j.LastError = "任务未完成，将在稍后重试"
				}
			}
			return nil
		})
		return err
	}
	if selected.Kind == "cleanup" {
		a.mu.Lock()
		for key, value := range a.attempts {
			if !value.Until.After(now) {
				delete(a.attempts, key)
			}
		}
		a.mu.Unlock()
	}
	return nil
}
