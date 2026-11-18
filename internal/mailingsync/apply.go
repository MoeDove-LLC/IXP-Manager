package mailingsync

import (
	"context"
	"errors"
	"os"
	"time"
)

func same(a, b *Member) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func desired(a Action, m *Member) bool { return a.Subscribe == (m != nil) }
func observed(m *Member) Result {
	return Result{State: "done", Observed: m, ObservedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00")}
}
func journal(path string, p Plan, create bool) (Journal, error) {
	j := Journal{Version: 1, PlanChecksum: p.Checksum, Results: make([]Result, len(p.Actions))}
	_, e := os.Lstat(path)
	if os.IsNotExist(e) && create {
		for i := range j.Results {
			j.Results[i].State = "planned"
		}
		raw, _ := Encode(j)
		return j, WriteNew(path, raw)
	}
	b, e := ReadPrivate(path, maxDocument)
	if e != nil {
		return j, e
	}
	if e = strict(b, &j); e != nil {
		return j, e
	}
	if j.Version != 1 || j.PlanChecksum != p.Checksum || len(j.Results) != len(p.Actions) {
		return j, errors.New("journal does not belong to this plan")
	}
	for i, r := range j.Results {
		switch r.State {
		case "planned", "intent", "unknown", "not-observed":
			if r.Observed != nil || r.ObservedAt != "" {
				return j, errors.New("invalid journal observation")
			}
		case "done":
			if !timestamp(r.ObservedAt) || !desired(p.Actions[i], r.Observed) || r.Observed != nil && (r.Observed.Email != p.Actions[i].Email || r.Observed.ListID != p.Target.ListID || r.Observed.Role != "member" || r.Observed.Mode != "as_address" || !uuid.MatchString(r.Observed.ID)) {
				return j, errors.New("invalid completed journal entry")
			}
		default:
			return j, errors.New("invalid journal state")
		}
	}
	return j, nil
}
func saveJournal(path string, j Journal) error {
	raw, e := Encode(j)
	if e != nil {
		return e
	}
	return persist(path, raw, true)
}
func copyJournal(j Journal) Journal {
	j.Results = append([]Result(nil), j.Results...)
	return j
}
func (c *Client) fresh(ctx context.Context, p Plan) error {
	e, err := c.sourceExport(ctx)
	if err != nil {
		return err
	}
	if e.Checksum != p.Source.Checksum {
		return errors.New("source intentions changed; create a fresh plan")
	}
	return c.identity(ctx)
}

// Apply requires a reviewed checksum and exclusive administration of the target
// list for this maintenance window: Mailman does not offer membership CAS. Its
// journal is durable before each write. Reopening never replays an intent.
func (c *Client) Apply(ctx context.Context, p Plan, path, checksum string, exclusive bool) (Summary, error) {
	if err := p.validate(c.config); err != nil {
		return Summary{}, err
	}
	if checksum != p.Checksum || !exclusive {
		return p.Summary(nil), errors.New("exact plan checksum and exclusive-access acknowledgement required")
	}
	bound := *c
	bound.config.Target.ListCreatedAt = p.Target.ListCreatedAt
	c = &bound
	unlock, e := lock(path)
	if e != nil {
		return p.Summary(nil), e
	}
	defer unlock()
	j, e := journal(path, p, true)
	if e != nil {
		return p.Summary(nil), e
	}
	persisted := copyJournal(j)
	dirty := false
	checkpoint := func() error {
		if err := saveJournal(path, j); err != nil {
			return err
		}
		persisted = copyJournal(j)
		dirty = false
		return nil
	}
	for _, r := range j.Results {
		if r.State == "intent" || r.State == "unknown" || r.State == "not-observed" {
			return p.Summary(&persisted), errors.New("journal requires read-only reconcile; uncertain writes are never retried")
		}
	}
	if e = c.fresh(ctx, p); e != nil {
		return p.Summary(&persisted), e
	}
	for i, a := range p.Actions {
		if e = ctx.Err(); e != nil {
			return p.Summary(&persisted), errors.New("synchronization interrupted before next write")
		}
		current, err := c.member(ctx, a.Email)
		if err != nil {
			return p.Summary(&persisted), err
		}
		if j.Results[i].State == "done" {
			if !same(current, j.Results[i].Observed) {
				return p.Summary(&persisted), errors.New("completed target membership drifted; review a fresh plan")
			}
			continue
		}
		if !same(current, a.Before) {
			return p.Summary(&persisted), errors.New("target membership changed since preview; review a fresh plan")
		}
		if desired(a, current) {
			j.Results[i] = observed(current)
			dirty = true
			continue
		}
		// Recheck current source consent and pinned target identity for every
		// mutation, including a resumed partially completed plan.
		if e = c.fresh(ctx, p); e != nil {
			return p.Summary(&persisted), e
		}
		current, err = c.member(ctx, a.Email)
		if err != nil || !same(current, a.Before) {
			return p.Summary(&persisted), errors.New("target changed before mutation; create a fresh plan")
		}
		if a.Subscribe {
			if e = c.subscriptionNotificationsDisabled(ctx); e != nil {
				return p.Summary(&persisted), e
			}
		}
		j.Results[i] = Result{State: "intent"}
		if e = checkpoint(); e != nil {
			return p.Summary(&persisted), e
		}
		mutationErr := c.mutate(ctx, a)
		// Independent readback is bounded and still attempted after a caller
		// timeout. It does not reissue a POST or DELETE.
		readCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		readMember, readErr := c.readback(readCtx, a.Email)
		cancel()
		if readErr == nil && desired(a, readMember) {
			j.Results[i] = observed(readMember)
		} else {
			j.Results[i] = Result{State: "unknown"}
		}
		if e = checkpoint(); e != nil {
			return p.Summary(&persisted), e
		}
		if j.Results[i].State == "unknown" {
			return p.Summary(&persisted), errors.New("membership result unknown; run mailing-reconcile, never repeat the write")
		}
		if mutationErr != nil {
			return p.Summary(&persisted), errors.New("response lost or rejected; readback observed intended membership; stopped before next action")
		}
	}
	if dirty {
		if e = checkpoint(); e != nil {
			return p.Summary(&persisted), e
		}
	}
	if e = c.fresh(ctx, p); e != nil {
		return p.Summary(&persisted), e
	}
	return p.Summary(&persisted), nil
}

// Reconcile only reads the remote target. Not observing the change does not
// prove that an earlier request cannot commit later. It never authorizes retry.
func (c *Client) Reconcile(ctx context.Context, p Plan, path, checksum string) (Summary, error) {
	if e := p.validate(c.config); e != nil {
		return Summary{}, e
	}
	if checksum != p.Checksum {
		return p.Summary(nil), errors.New("exact plan checksum required")
	}
	bound := *c
	bound.config.Target.ListCreatedAt = p.Target.ListCreatedAt
	c = &bound
	unlock, e := lock(path)
	if e != nil {
		return p.Summary(nil), e
	}
	defer unlock()
	j, e := journal(path, p, false)
	if e != nil {
		return p.Summary(nil), e
	}
	persisted := copyJournal(j)
	if e = c.identity(ctx); e != nil {
		return p.Summary(&persisted), e
	}
	for i, a := range p.Actions {
		state := j.Results[i].State
		if state != "intent" && state != "unknown" && state != "not-observed" {
			continue
		}
		m, e := c.readback(ctx, a.Email)
		if e != nil {
			return p.Summary(&persisted), e
		}
		switch {
		case desired(a, m):
			j.Results[i] = observed(m)
		case same(m, a.Before):
			j.Results[i] = Result{State: "not-observed"}
		default:
			j.Results[i] = Result{State: "unknown"}
		}
		if e = saveJournal(path, j); e != nil {
			return p.Summary(&persisted), e
		}
		persisted = copyJournal(j)
	}
	return p.Summary(&persisted), nil
}
func Status(p Plan, path string) (Summary, error) {
	j, e := journal(path, p, false)
	if e != nil {
		return p.Summary(nil), e
	}
	return p.Summary(&j), nil
}
