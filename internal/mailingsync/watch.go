package mailingsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxWatchRounds = 10000
const maxWatchHistoryBytes int64 = 128 << 20

type WatchOptions struct {
	Automatic       bool
	Exclusive       bool
	MaxChanges      int
	IntervalSeconds int
}

// WatchPolicy authorizes future explicit source intentions for one fixed entity.
// Credential contents may rotate; URLs, identities and credential paths may not.
type WatchPolicy struct {
	Version              int    `json:"version"`
	Automatic            bool   `json:"automatic"`
	Exclusive            bool   `json:"exclusiveAdministrationAcknowledged"`
	ConfigChecksum       string `json:"configChecksum"`
	ApprovedPlanChecksum string `json:"approvedPlanChecksum"`
	TargetCreatedAt      string `json:"targetCreatedAt"`
	MaxChanges           int    `json:"maxChanges"`
	IntervalSeconds      int    `json:"intervalSeconds"`
	AuthorizedAt         string `json:"authorizedAt"`
	Checksum             string `json:"checksum"`
}

type WatchStatus struct {
	Version          int     `json:"version"`
	PolicyChecksum   string  `json:"policyChecksum"`
	Round            uint64  `json:"round"`
	PrunedThrough    uint64  `json:"prunedThrough"`
	Active           bool    `json:"active"`
	Halted           bool    `json:"halted"`
	Reason           string  `json:"reason"`
	LastObservedAt   string  `json:"lastObservedAt,omitempty"`
	SnapshotChecksum string  `json:"snapshotChecksum,omitempty"`
	Summary          Summary `json:"summary"`
}

type watchReceipt struct {
	Version         int    `json:"version"`
	PolicyChecksum  string `json:"policyChecksum"`
	PlanChecksum    string `json:"planChecksum"`
	JournalChecksum string `json:"journalChecksum"`
	Outcome         string `json:"outcome"`
	ClosedAt        string `json:"closedAt"`
}

type Watcher struct {
	config Config
	dir    string
	policy WatchPolicy
	state  WatchStatus
	unlock func()
}

func watchPolicySum(p WatchPolicy) string { p.Checksum = ""; return sum(p) }
func watchNow() string                    { return time.Now().UTC().Format(time.RFC3339Nano) }
func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("watch directory must be an owned real 0700 directory")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		return errors.New("watch directory owner changed")
	}
	_, err = cleanPath(filepath.Join(path, "state.json"))
	return err
}
func writeWatch(path string, value any, replace bool) error {
	raw, err := Encode(value)
	if err != nil {
		return err
	}
	return persist(path, raw, replace)
}
func readWatch(path string, value any) error {
	raw, err := ReadPrivate(path, maxDocument)
	if err != nil {
		return err
	}
	return strict(raw, value)
}
func syncWatchDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func AuthorizeWatch(ctx context.Context, c Config, reviewed Plan, dir, checksum string, opt WatchOptions) (WatchPolicy, error) {
	var policy WatchPolicy
	if !opt.Automatic || !opt.Exclusive || opt.MaxChanges < 1 || opt.MaxChanges > maxChanges || opt.IntervalSeconds < 5 || opt.IntervalSeconds > 86400 {
		return policy, errors.New("automatic synchronization and exclusive administration must be explicit; max changes must be 1..1000 and interval 5..86400 seconds")
	}
	if err := reviewed.validate(c); err != nil {
		return policy, err
	}
	if checksum != reviewed.Checksum {
		return policy, errors.New("exact reviewed initial plan checksum required")
	}
	client, err := NewClient(c)
	if err != nil {
		return policy, err
	}
	client.config.Target.ListCreatedAt = reviewed.Target.ListCreatedAt
	if err = client.fresh(ctx, reviewed); err != nil {
		return policy, err
	}
	if reviewed.Summary(nil).Add > 0 {
		if err = client.subscriptionNotificationsDisabled(ctx); err != nil {
			return policy, err
		}
	}
	dir, err = cleanPath(dir)
	if err != nil {
		return policy, err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return policy, errors.New("watch authorization requires a new private state directory; never replace an existing worker history")
	}
	policy = WatchPolicy{Version: 1, Automatic: true, Exclusive: true, ConfigChecksum: sum(c), ApprovedPlanChecksum: reviewed.Checksum, TargetCreatedAt: reviewed.Target.ListCreatedAt, MaxChanges: opt.MaxChanges, IntervalSeconds: opt.IntervalSeconds, AuthorizedAt: watchNow()}
	policy.Checksum = watchPolicySum(policy)
	if err = writeWatch(filepath.Join(dir, "approved-plan.json"), reviewed, false); err != nil {
		return WatchPolicy{}, err
	}
	if err = writeWatch(filepath.Join(dir, "policy.json"), policy, false); err != nil {
		return WatchPolicy{}, err
	}
	state := WatchStatus{Version: 1, PolicyChecksum: policy.Checksum, Reason: "authorized; no automatic round has run"}
	if err = writeWatch(filepath.Join(dir, "state.json"), state, false); err != nil {
		return WatchPolicy{}, err
	}
	return policy, nil
}

func OpenWatch(c Config, dir string) (*Watcher, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = privateDirectory(abs); err != nil {
		return nil, err
	}
	unlock, err := lock(filepath.Join(abs, "worker"))
	if err != nil {
		return nil, errors.New("another watch worker holds this state directory, or its lock is unavailable")
	}
	w := &Watcher{config: c, dir: abs, unlock: unlock}
	ok := false
	defer func() {
		if !ok {
			w.Close()
		}
	}()
	if err = readWatch(filepath.Join(abs, "policy.json"), &w.policy); err != nil {
		return nil, err
	}
	p := w.policy
	if p.Version != 1 || !p.Automatic || !p.Exclusive || p.ConfigChecksum != sum(c) || p.Checksum != watchPolicySum(p) || !hash(p.Checksum) || !hash(p.ApprovedPlanChecksum) || !mailmanTime(p.TargetCreatedAt) || !timestamp(p.AuthorizedAt) || p.MaxChanges < 1 || p.MaxChanges > maxChanges || p.IntervalSeconds < 5 || p.IntervalSeconds > 86400 {
		return nil, errors.New("watch authorization or fixed configuration changed; original authorization must not be silently replaced")
	}
	approved, err := LoadPlan(filepath.Join(abs, "approved-plan.json"), c)
	if err != nil || approved.Checksum != p.ApprovedPlanChecksum || approved.Target.ListCreatedAt != p.TargetCreatedAt {
		return nil, errors.New("initial reviewed target authorization unavailable or changed")
	}
	w.config.Target.ListCreatedAt = p.TargetCreatedAt
	if err = readWatch(filepath.Join(abs, "state.json"), &w.state); err != nil {
		return nil, err
	}
	s := w.state
	if s.Version != 1 || s.PolicyChecksum != p.Checksum || s.PrunedThrough > s.Round || s.Active && s.Round <= s.PrunedThrough || s.LastObservedAt != "" && !timestamp(s.LastObservedAt) || s.SnapshotChecksum != "" && !hash(s.SnapshotChecksum) {
		return nil, errors.New("watch state does not match its authorization")
	}
	if _, err = w.history(true); err != nil {
		return nil, err
	}
	ok = true
	return w, nil
}
func (w *Watcher) Close() error {
	if w.unlock != nil {
		w.unlock()
		w.unlock = nil
	}
	return nil
}
func (w *Watcher) Policy() WatchPolicy { return w.policy }
func (w *Watcher) Status() WatchStatus { return w.state }
func (w *Watcher) roundDir(n uint64) string {
	return filepath.Join(w.dir, fmt.Sprintf("round-%020d", n))
}
func (w *Watcher) save(next WatchStatus) error {
	if err := writeWatch(filepath.Join(w.dir, "state.json"), next, true); err != nil {
		return err
	}
	w.state = next
	return nil
}
func (w *Watcher) halt(reason string, cause error) (WatchStatus, error) {
	next := w.state
	next.Halted = true
	next.Reason = reason + ": " + cause.Error()
	if err := w.save(next); err != nil {
		return w.state, fmt.Errorf("watch halt could not be persisted; stop worker: %w", err)
	}
	return w.state, errors.New(next.Reason)
}
func (w *Watcher) round(n uint64) (Plan, Journal, error) {
	dir := w.roundDir(n)
	if err := privateDirectory(dir); err != nil {
		return Plan{}, Journal{}, err
	}
	p, err := LoadPlan(filepath.Join(dir, "plan.json"), w.config)
	if err != nil {
		return p, Journal{}, err
	}
	if p.Summary(nil).Add+p.Summary(nil).Remove > w.policy.MaxChanges {
		return p, Journal{}, errors.New("retained plan exceeds authorized per-round change limit")
	}
	j, err := journal(filepath.Join(dir, "journal.json"), p, false)
	return p, j, err
}
func uncertain(p Plan, j Journal) bool { s := p.Summary(&j); return s.Unknown > 0 || s.NotObserved > 0 }
func completed(p Plan, j Journal) bool {
	s := p.Summary(&j)
	return s.NoIntentions || s.RecordedComplete
}
func (w *Watcher) receipt(n uint64, p Plan, j Journal) (bool, error) {
	path := filepath.Join(w.roundDir(n), "closed.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return false, nil
	}
	var r watchReceipt
	if err := readWatch(path, &r); err != nil {
		return false, err
	}
	if r.Version != 1 || r.PolicyChecksum != w.policy.Checksum || r.PlanChecksum != p.Checksum || r.JournalChecksum != sum(j) || !timestamp(r.ClosedAt) || uncertain(p, j) || r.Outcome != "retired" && r.Outcome != "completed" || r.Outcome == "completed" && !completed(p, j) {
		return false, errors.New("closed watch round was altered or contains an unresolved write")
	}
	return true, nil
}

// Every published round has a plan and durable journal before it can be active.
// A crash after publishing the directory but before state.json may leave exactly
// one planned orphan; adopt it rather than create a different journal.
func (w *Watcher) history(adopt bool) (int64, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return 0, err
	}
	seen := map[uint64]bool{}
	var bytes int64
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "round-") {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimPrefix(entry.Name(), "round-"), 10, 64)
		if err != nil || n == 0 || filepath.Base(w.roundDir(n)) != entry.Name() || !entry.IsDir() {
			return 0, errors.New("unexpected watch round artifact")
		}
		if n <= w.state.PrunedThrough {
			continue
		}
		seen[n] = true
		files, err := os.ReadDir(w.roundDir(n))
		if err != nil {
			return 0, err
		}
		for _, f := range files {
			if f.Name() != "plan.json" && f.Name() != "journal.json" && f.Name() != "journal.json.lock" && f.Name() != "closed.json" {
				return 0, errors.New("unexpected file in retained watch round")
			}
			info, err := f.Info()
			if err != nil || !info.Mode().IsRegular() {
				return 0, errors.New("invalid retained watch artifact")
			}
			bytes += info.Size()
		}
	}
	if len(seen) > maxWatchRounds+1 {
		return bytes, errors.New("watch history limit exceeded; prune completed rounds")
	}
	for n := w.state.PrunedThrough + 1; n <= w.state.Round; n++ {
		if !seen[n] {
			return bytes, errors.New("watch history missing; do not replace an unresolved journal")
		}
		delete(seen, n)
		p, j, err := w.round(n)
		if err != nil {
			return bytes, err
		}
		closed, err := w.receipt(n, p, j)
		if err != nil {
			return bytes, err
		}
		if n != w.state.Round || !w.state.Active {
			if !closed {
				return bytes, errors.New("earlier watch round is not closed; no new journal allowed")
			}
		} else if closed && adopt {
			next := w.state
			next.Active = false
			next.Summary = p.Summary(&j)
			if completed(p, j) {
				next.SnapshotChecksum = watchObservedSnapshot(p, j)
			}
			if err := w.save(next); err != nil {
				return bytes, err
			}
		} else if adopt {
			next := w.state
			next.Summary = p.Summary(&j)
			if uncertain(p, j) {
				next.Halted = true
				next.Reason = "uncertain: retained journal requires read-only reconcile before explicit resume"
			}
			if err := w.save(next); err != nil {
				return bytes, err
			}
		}
	}
	if len(seen) != 0 {
		n := w.state.Round + 1
		if !adopt || len(seen) != 1 || !seen[n] || w.state.Active {
			return bytes, errors.New("unreferenced watch history requires inspection")
		}
		p, j, err := w.round(n)
		if err != nil {
			return bytes, err
		}
		for _, r := range j.Results {
			if r.State != "planned" {
				return bytes, errors.New("unreferenced write intent cannot be adopted")
			}
		}
		if closed, err := w.receipt(n, p, j); err != nil || closed {
			return bytes, errors.New("unreferenced watch receipt cannot be adopted")
		}
		next := w.state
		next.Round = n
		next.Active = true
		next.Summary = p.Summary(&j)
		if err := w.save(next); err != nil {
			return bytes, err
		}
	}
	return bytes, nil
}
func watchSnapshot(p Plan) string {
	return sum(struct {
		Source  string
		Actions []Action
		Target  string
	}{p.Source.Checksum, p.Actions, p.Target.ListCreatedAt})
}
func watchObservedSnapshot(p Plan, j Journal) string {
	p.Actions = append([]Action(nil), p.Actions...)
	for i, r := range j.Results {
		if r.State == "done" {
			p.Actions[i].Before = r.Observed
		}
	}
	return watchSnapshot(p)
}
func (w *Watcher) preview(ctx context.Context) (*Client, Plan, string, error) {
	c, err := NewClient(w.config)
	if err != nil {
		return nil, Plan{}, "credentials", err
	}
	if _, err = c.sourceExport(ctx); err != nil {
		return c, Plan{}, "source-authority", err
	}
	if err = c.identity(ctx); err != nil {
		if errors.Is(err, errTargetAuthority) {
			return c, Plan{}, "target-authority", err
		}
		return c, Plan{}, "target-identity", err
	}
	p, err := c.Preview(ctx)
	if err != nil {
		return c, p, "snapshot-rejected", err
	}
	s := p.Summary(nil)
	if s.Add+s.Remove > w.policy.MaxChanges {
		return c, p, "change-limit", fmt.Errorf("round needs %d changes; authorized maximum is %d", s.Add+s.Remove, w.policy.MaxChanges)
	}
	if s.Add > 0 {
		if err = c.subscriptionNotificationsDisabled(ctx); err != nil {
			return c, p, "notification-prerequisite", err
		}
	}
	return c, p, "", nil
}
func (w *Watcher) prepare(p Plan) error {
	bytes, err := w.history(false)
	if err != nil {
		return err
	}
	raw, _ := Encode(p)
	if w.state.Round-w.state.PrunedThrough >= maxWatchRounds || bytes+int64(len(raw)+len(p.Actions)*1024+4096) > maxWatchHistoryBytes || w.state.Round == ^uint64(0) {
		return errors.New("watch history capacity reached; use mailing-watch --prune-keep with policy checksum to remove only closed rounds")
	}
	stage, err := os.MkdirTemp(w.dir, ".preparing-")
	if err != nil {
		return err
	}
	// Staging directories never contain remote write attempts; leave failures as
	// evidence instead of deleting files whose publication outcome is uncertain.
	if err = WriteNew(filepath.Join(stage, "plan.json"), raw); err != nil {
		return err
	}
	j, err := journal(filepath.Join(stage, "journal.json"), p, true)
	if err != nil {
		return err
	}
	n := w.state.Round + 1
	if _, err = os.Lstat(w.roundDir(n)); !os.IsNotExist(err) {
		return errors.New("next watch round already exists")
	}
	if err = os.Rename(stage, w.roundDir(n)); err != nil {
		return err
	}
	if err = syncWatchDirectory(w.dir); err != nil {
		return err
	}
	next := w.state
	next.Round = n
	next.Active = true
	next.Summary = p.Summary(&j)
	next.Reason = "prepared; no write attempted"
	return w.save(next)
}
func (w *Watcher) closeRound(p Plan, j Journal, outcome string) error {
	if uncertain(p, j) {
		return errors.New("unresolved writes cannot be closed or superseded")
	}
	closed, err := w.receipt(w.state.Round, p, j)
	if err != nil {
		return err
	}
	if !closed {
		r := watchReceipt{1, w.policy.Checksum, p.Checksum, sum(j), outcome, watchNow()}
		if err = writeWatch(filepath.Join(w.roundDir(w.state.Round), "closed.json"), r, false); err != nil {
			return err
		}
	}
	next := w.state
	next.Active = false
	next.Summary = p.Summary(&j)
	if outcome == "completed" {
		next.SnapshotChecksum = watchObservedSnapshot(p, j)
	}
	return w.save(next)
}

func (w *Watcher) Tick(ctx context.Context) (WatchStatus, error) {
	if w.unlock == nil {
		return w.state, errors.New("watch worker is closed")
	}
	if w.state.Halted {
		return w.state, errors.New("watch is halted; reconcile unresolved writes, then explicitly resume using the policy checksum: " + w.state.Reason)
	}
	var c *Client
	var p Plan
	var err error
	if !w.state.Active {
		var reason string
		c, p, reason, err = w.preview(ctx)
		if err != nil {
			return w.halt(reason, err)
		}
		if watchSnapshot(p) == w.state.SnapshotChecksum {
			next := w.state
			next.LastObservedAt = watchNow()
			next.Reason = "unchanged snapshot; no new round or remote write"
			if err = w.save(next); err != nil {
				return w.state, err
			}
			return w.state, nil
		}
		if err = w.prepare(p); err != nil {
			return w.halt("history", err)
		}
	}
	p, j, err := w.round(w.state.Round)
	if err != nil {
		return w.halt("active-round", err)
	}
	if uncertain(p, j) {
		return w.halt("uncertain", errors.New("pending/unknown/not-observed write blocks all new plans; use read-only reconcile"))
	}
	if c == nil {
		c, err = NewClient(w.config)
		if err != nil {
			return w.halt("credentials", err)
		}
	}
	summary, applyErr := c.Apply(ctx, p, filepath.Join(w.roundDir(w.state.Round), "journal.json"), p.Checksum, true)
	next := w.state
	next.Summary = summary
	if applyErr == nil {
		next.LastObservedAt = watchNow()
	}
	if err = w.save(next); err != nil {
		return w.state, fmt.Errorf("watch status persistence failed; active journal remains authoritative: %w", err)
	}
	if applyErr != nil {
		return w.halt("apply-stopped", applyErr)
	}
	_, j, err = w.round(w.state.Round)
	if err != nil || !completed(p, j) {
		return w.halt("journal", errors.New("successful apply lacks a complete durable journal"))
	}
	if err = w.closeRound(p, j, "completed"); err != nil {
		return w.halt("close-round", err)
	}
	next = w.state
	next.Reason = "round completed; recorded observations only"
	if err = w.save(next); err != nil {
		return w.state, err
	}
	return w.state, nil
}

// Reconcile never clears the halt latch, even when a late commit becomes visible.
func (w *Watcher) Reconcile(ctx context.Context) (WatchStatus, error) {
	if w.unlock == nil || !w.state.Active {
		return w.state, errors.New("no active watch round to reconcile")
	}
	p, _, err := w.round(w.state.Round)
	if err != nil {
		return w.state, err
	}
	c, err := NewClient(w.config)
	if err != nil {
		return w.halt("credentials", err)
	}
	s, reconcileErr := c.Reconcile(ctx, p, filepath.Join(w.roundDir(w.state.Round), "journal.json"), p.Checksum)
	next := w.state
	next.Halted = true
	next.Summary = s
	if reconcileErr == nil {
		next.LastObservedAt = watchNow()
		next.Reason = "read-only reconcile recorded; explicit policy-checksum resume still required"
	} else {
		next.Reason = "reconcile-stopped: " + reconcileErr.Error()
	}
	if err = w.save(next); err != nil {
		return w.state, err
	}
	return w.state, reconcileErr
}

// Resume retires only known results, then allows the next fresh consent snapshot.
// It does not mutate membership and cannot resolve an unknown by creating a plan.
func (w *Watcher) Resume(ctx context.Context, checksum string) (WatchStatus, error) {
	if w.unlock == nil || checksum != w.policy.Checksum || !w.state.Halted {
		return w.state, errors.New("halted worker and exact policy checksum required to resume")
	}
	if _, err := w.history(false); err != nil {
		return w.state, err
	}
	if w.state.Active {
		p, j, err := w.round(w.state.Round)
		if err != nil {
			return w.state, err
		}
		if uncertain(p, j) {
			return w.state, errors.New("unresolved old write prevents resume regardless of current consent; reconcile read-only")
		}
	}
	_, _, reason, err := w.preview(ctx)
	if err != nil {
		return w.halt(reason, err)
	}
	if w.state.Active {
		p, j, err := w.round(w.state.Round)
		if err != nil {
			return w.state, err
		}
		if err = w.closeRound(p, j, "retired"); err != nil {
			return w.state, err
		}
	}
	next := w.state
	next.Halted = false
	next.Reason = "explicitly resumed; next round will obtain fresh consent"
	if err = w.save(next); err != nil {
		return w.state, err
	}
	return w.state, nil
}

// Prune is explicit local retention, never automatic recovery. The initial
// authorization and active round are retained; only validated closed rounds go.
func (w *Watcher) Prune(keep int, checksum string) (WatchStatus, error) {
	if w.unlock == nil || checksum != w.policy.Checksum || keep < 1 || keep > maxWatchRounds {
		return w.state, errors.New("pruning requires exact policy checksum and 1..10000 retained closed rounds")
	}
	if _, err := w.history(false); err != nil {
		return w.state, err
	}
	last := w.state.Round
	if w.state.Active {
		last--
	}
	through := w.state.PrunedThrough
	if last > uint64(keep) && last-uint64(keep) > through {
		through = last - uint64(keep)
	}
	next := w.state
	next.PrunedThrough = through
	if err := w.save(next); err != nil {
		return w.state, err
	}
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return w.state, err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "round-") {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimPrefix(entry.Name(), "round-"), 10, 64)
		if err != nil || n == 0 || n > through || filepath.Base(w.roundDir(n)) != entry.Name() {
			continue
		}
		dir := w.roundDir(n)
		if err = privateDirectory(dir); err != nil {
			return w.state, err
		}
		for _, file := range []string{"plan.json", "journal.json", "closed.json", "journal.json.lock"} {
			if err = os.Remove(filepath.Join(dir, file)); err != nil && !os.IsNotExist(err) {
				return w.state, err
			}
		}
		if err = os.Remove(dir); err != nil {
			return w.state, err
		}
	}
	if err = syncWatchDirectory(w.dir); err != nil {
		return w.state, err
	}
	return w.state, nil
}
