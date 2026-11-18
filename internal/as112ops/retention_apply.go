package as112ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"github.com/moedove/ixp-manager/internal/routing"
)

func ReadRetention(root string) (RetentionStatus, error) {
	var s RetentionStatus
	if e := privateDir(root); e != nil {
		return s, e
	}
	raw, e := privateRead(filepath.Join(root, "retention.json"), MaxRetentionReviewBytes*2)
	if e != nil {
		return s, e
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
		return s, errors.New("invalid retention journal")
	}
	canonical, _ := json.MarshalIndent(s, "", "  ")
	ops := retentionOperations(s.Review)
	if string(raw) != string(canonical)+"\n" || s.Version != 1 || validateRetentionReview(s.Review) != nil || s.Cursor < s.Review.StartCursor || s.Cursor > len(ops) || s.Phase != "running" && s.Phase != "unknown" && s.Phase != "complete" || s.Phase == "complete" && (s.Cursor != len(ops) || s.Pending != nil) {
		return s, errors.New("retention journal inconsistent")
	}
	if s.Pending != nil && (s.Cursor >= len(ops) || s.Pending.Index != s.Cursor || s.Pending.Path != ops[s.Cursor].Path) {
		return s, errors.New("retention intent does not match its reviewed cursor")
	}
	return s, nil
}
func (c *Controller) writeRetention(s *RetentionStatus) error {
	s.UpdatedAt = c.now().UTC()
	raw, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	return writeAtomic(c.plan.Root, "retention.json", append(raw, '\n'))
}
func retentionResult(s *RetentionStatus, e error) (RetentionStatus, error) { return *s, e }
func (c *Controller) retentionFailure(s *RetentionStatus, reason string) error {
	s.Phase = "unknown"
	_ = c.writeRetention(s)
	return c.fail("retention: " + reason)
}
func (c *Controller) retentionRequest(q RetentionRequest) error {
	r := q.Review
	if e := validateRetentionReview(r); e != nil {
		return e
	}
	if !q.ExclusiveInstanceAcknowledged || !q.DeleteAcknowledged || q.ReviewChecksum != r.Checksum || r.CurrentPlanChecksum != c.plan.Checksum || r.Root != c.plan.Root || r.Host != c.plan.Host {
		return errors.New("retention requires the exact review checksum, deletion acknowledgment and exclusive instance ownership")
	}
	return nil
}
func sameIdentity(actual, want RetentionEntry) bool {
	return actual.Path == want.Path && actual.Directory == want.Directory && actual.Device == want.Device && actual.Inode == want.Inode && actual.UID == want.UID && actual.Mode == want.Mode && actual.Bytes == want.Bytes && actual.SHA256 == want.SHA256
}
func (c *Controller) retentionGuard(ctx context.Context, r RetentionReview) error {
	if e := retentionMounts(r.Root); e != nil {
		return e
	}
	if c.closed || c.state.PlanChecksum != r.CurrentPlanChecksum || c.state.Pending != nil || c.state.DNS != nil || c.state.BGP != nil {
		return errors.New("retention service ownership changed")
	}
	var e error
	if e = verifyTarget(c.plan, c.state); e != nil {
		return errors.New("retention target changed")
	}
	if e = c.replacementAbsent(ctx); e != nil {
		return e
	}
	if e = exactCurrent(r.Root, r.CurrentTarget); e != nil {
		return e
	}
	for _, pair := range []struct {
		path  string
		entry RetentionEntry
	}{{r.Root, r.RootIdentity}, {filepath.Join(r.Root, "versions"), r.VersionsIdentity}} {
		entry, e := captureRetentionEntry(pair.path, "")
		if e != nil || !sameIdentity(entry, pair.entry) {
			return errors.New("retention root directory identity changed")
		}
	}
	h, e := retainedFileHash(r.Root, "replacement.json")
	if e != nil || h != r.ReplacementChecksum {
		return errors.New("replacement marker changed since retention review")
	}
	if m, e := ReadReplacement(r.Root); e == nil {
		if m.Phase != "complete" || m.NextPlanChecksum != r.CurrentPlanChecksum {
			return errors.New("replacement recovery material cannot be pruned")
		}
		for _, source := range r.Candidates {
			if source.PlanChecksum == m.PreviousPlanChecksum || source.PlanChecksum == m.NextPlanChecksum {
				return errors.New("latest replacement endpoints are protected")
			}
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	return ctx.Err()
}
func (c *Controller) retentionRemaining(ctx context.Context, r RetentionReview, cursor int) error {
	if e := c.retentionGuard(ctx, r); e != nil {
		return e
	}
	ops := retentionOperations(r)
	if cursor < r.StartCursor || cursor > len(ops) {
		return errors.New("invalid deletion cursor")
	}
	gone := map[string]bool{}
	for _, op := range ops[:cursor] {
		gone[op.Path] = true
	}
	actualNames, e := os.ReadDir(filepath.Join(r.Root, "versions"))
	if e != nil {
		return e
	}
	expectedNames := map[string]bool{}
	for _, tree := range r.Inventory {
		rootPath := filepath.Join("versions", tree.PlanChecksum)
		if gone[rootPath] {
			if _, e = os.Lstat(filepath.Join(r.Root, rootPath)); !os.IsNotExist(e) {
				return errors.New("a removed version reappeared")
			}
			continue
		}
		expectedNames[tree.PlanChecksum] = true
		actual, e := captureRetentionTree(r.Root, tree.PlanChecksum)
		if e != nil {
			return e
		}
		want := []RetentionEntry{}
		for _, entry := range tree.Entries {
			if !gone[filepath.Join(rootPath, entry.Path)] {
				want = append(want, entry)
			}
		}
		if !reflect.DeepEqual(actual.Entries, want) {
			return errors.New("reviewed version entries changed or unexpected files appeared")
		}
	}
	if len(actualNames) != len(expectedNames) {
		return errors.New("version inventory changed since review")
	}
	for _, entry := range actualNames {
		if !expectedNames[entry.Name()] {
			return errors.New("unreviewed version or recovery material appeared")
		}
	}
	return nil
}
func (c *Controller) proveInitialRetention(r RetentionReview) error {
	for _, source := range r.Candidates {
		root := filepath.Join(r.Root, "versions", source.PlanChecksum)
		active, e := privateRead(filepath.Join(root, "active.conf"), 16<<20)
		if e != nil {
			return e
		}
		standby, e := privateRead(filepath.Join(root, "standby.conf"), 16<<20)
		if e != nil {
			return e
		}
		bundle, e := privateRead(filepath.Join(root, "bundle.json"), 1<<20)
		if e != nil {
			return e
		}
		p := Plan{Version: 1, Root: r.Root, Platform: c.plan.Platform, RouterID: c.plan.RouterID, RouterCreatedAt: c.plan.RouterCreatedAt, Host: r.Host, ActiveConfig: string(active), StandbyConfig: string(standby), DNSBundle: string(bundle), ActiveChecksum: source.ActiveChecksum, StandbyChecksum: source.StandbyChecksum, DNSChecksum: source.DNSChecksum, Checksum: source.PlanChecksum, Tools: source.Tools, HealthIntervalSeconds: source.HealthIntervalSeconds, RecoveryHealthyChecks: source.RecoveryHealthyChecks}
		if source.Host != nil {
			p.Host = *source.Host
		}
		if e = c.historicalProof(p); e != nil {
			return e
		}
	}
	return nil
}
func sameRetentionAuthority(a, b RetentionReview) bool {
	return a.Root == b.Root && a.Host == b.Host && a.CurrentPlanChecksum == b.CurrentPlanChecksum && a.CurrentTarget == b.CurrentTarget && a.StateSequence == b.StateSequence && a.ReplacementChecksum == b.ReplacementChecksum && reflect.DeepEqual(a.RootIdentity, b.RootIdentity) && reflect.DeepEqual(a.VersionsIdentity, b.VersionsIdentity) && reflect.DeepEqual(a.Inventory, b.Inventory) && reflect.DeepEqual(a.Candidates, b.Candidates) && reflect.DeepEqual(a.Protected, b.Protected)
}

// ReviewRetentionResume does not delete anything. It proves that the remaining
// entries still have the original identities/bytes, then issues a NEW checksum
// bound to the old pending journal. Applying it requires fresh explicit consent;
// the old uncertain unlink is never retried automatically.
func (c *Controller) ReviewRetentionResume(ctx context.Context) (RetentionReview, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var r RetentionReview
	if e := c.retentionOffline(ctx); e != nil {
		return r, e
	}
	old, e := ReadRetention(c.plan.Root)
	if e != nil {
		return r, e
	}
	ops := retentionOperations(old.Review)
	if old.Phase == "complete" || old.Cursor >= len(ops) {
		return r, errors.New("no remaining retention work; reconcile a completed cursor instead")
	}
	if c.state.Sequence != old.Review.StateSequence || c.state.Phase != "withdrawn" && c.state.Phase != "unknown" {
		return r, errors.New("retention base state changed")
	}
	if e = c.retentionRemaining(ctx, old.Review, old.Cursor); e != nil {
		return r, e
	}
	r = old.Review
	r.ResumeOf = old.Review.Checksum
	r.PreviousPhase = old.Phase
	r.PreviousIntent = old.Pending
	r.StartCursor = old.Cursor
	r.ReviewedAt = c.now().UTC()
	if r.PreviousRetentionChecksum, e = retainedFileHash(r.Root, "retention.json"); e != nil {
		return r, e
	}
	if r.StateChecksum, e = retainedFileHash(r.Root, "status.json"); e != nil {
		return r, e
	}
	r.Checksum = retentionHash(r)
	return r, validateRetentionReview(r)
}

func (c *Controller) ApplyRetention(ctx context.Context, q RetentionRequest) (RetentionStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var s RetentionStatus
	if e := c.retentionRequest(q); e != nil {
		return s, e
	}
	r := q.Review
	old, e := ReadRetention(c.plan.Root)
	exists := e == nil
	if e != nil && !os.IsNotExist(e) {
		return s, e
	}
	if exists && old.Review.Checksum == r.Checksum {
		if old.Phase == "complete" {
			return old, nil
		}
		if old.Pending != nil {
			return old, errors.New("uncertain deletion is not replayed; reconcile absence or create a new resume review")
		}
		if c.state.Sequence != r.StateSequence || c.state.Phase != "withdrawn" && c.state.Phase != "unknown" {
			return old, errors.New("retention state changed")
		}
		if e = c.retentionRemaining(ctx, r, old.Cursor); e != nil {
			return old, e
		}
		s = old
	} else {
		if e = c.retentionOffline(ctx); e != nil {
			return s, e
		}
		if len(retentionOperations(r)) == r.StartCursor {
			return s, errors.New("review contains no remaining approved deletions")
		}
		stateHash, e := retainedFileHash(r.Root, "status.json")
		if e != nil || stateHash != r.StateChecksum || c.state.Sequence != r.StateSequence {
			return s, errors.New("status changed since retention review")
		}
		markerHash, e := retainedFileHash(r.Root, "retention.json")
		if e != nil || markerHash != r.PreviousRetentionChecksum {
			return s, errors.New("retention journal changed since review")
		}
		if r.ResumeOf != "" {
			if !exists || old.Phase == "complete" || old.Review.Checksum != r.ResumeOf || old.Cursor != r.StartCursor || r.PreviousPhase != old.Phase || !reflect.DeepEqual(r.PreviousIntent, old.Pending) || !sameRetentionAuthority(r, old.Review) {
				return s, errors.New("resume review expands or changes the original authority")
			}
		} else {
			if c.state.Phase != "withdrawn" || exists && old.Phase != "complete" {
				return s, errors.New("unknown state requires an explicit resume review")
			}
			if e = c.proveInitialRetention(r); e != nil {
				return s, e
			}
		}
		if e = c.retentionRemaining(ctx, r, r.StartCursor); e != nil {
			return s, e
		}
		s = RetentionStatus{Version: 1, Review: r, Phase: "running", Cursor: r.StartCursor}
		if e = c.writeRetention(&s); e != nil {
			return retentionResult(&s, c.retentionFailure(&s, "initial journal durability unknown"))
		}
	}
	return c.runRetention(ctx, &s)
}
func (c *Controller) runRetention(ctx context.Context, s *RetentionStatus) (RetentionStatus, error) {
	ops := retentionOperations(s.Review)
	for s.Cursor < len(ops) {
		if e := c.retentionGuard(ctx, s.Review); e != nil {
			return retentionResult(s, c.retentionFailure(s, "pre-delete ownership evidence failed"))
		}
		op := ops[s.Cursor]
		s.Pending = &RetentionIntent{Index: s.Cursor, Path: op.Path, StartedAt: c.now().UTC()}
		if e := c.writeRetention(s); e != nil {
			return retentionResult(s, c.retentionFailure(s, "delete intent durability unknown"))
		}
		if e := c.retentionGuard(ctx, s.Review); e != nil {
			return retentionResult(s, c.retentionFailure(s, "ownership changed after intent"))
		}
		if e := removeRetentionEntry(s.Review, op); e != nil {
			return retentionResult(s, c.retentionFailure(s, "delete result or absence unknown"))
		}
		s.Cursor++
		s.Pending = nil
		s.Phase = "running"
		if e := c.writeRetention(s); e != nil {
			return retentionResult(s, c.retentionFailure(s, "delete result durability unknown"))
		}
	}
	return c.completeRetention(ctx, s)
}
func (c *Controller) completeRetention(ctx context.Context, s *RetentionStatus) (RetentionStatus, error) {
	if e := c.retentionRemaining(ctx, s.Review, s.Cursor); e != nil {
		return retentionResult(s, c.retentionFailure(s, "final inventory evidence failed"))
	}
	if s.Cursor != len(retentionOperations(s.Review)) || s.Pending != nil {
		return *s, errors.New("retention cursor incomplete")
	}
	if c.state.Sequence != s.Review.StateSequence && c.state.Sequence != s.Review.StateSequence+1 {
		return *s, errors.New("retention completion sequence changed")
	}
	c.state.Sequence = s.Review.StateSequence + 1
	c.state.Phase = "withdrawn"
	c.state.HealthyChecks = 0
	c.state.LastHealthy = false
	c.state.Reason = "retention-complete"
	if e := c.saveStatus(); e != nil {
		return *s, e
	}
	s.Phase = "complete"
	if e := c.writeRetention(s); e != nil {
		return retentionResult(s, c.retentionFailure(s, "completion marker durability unknown"))
	}
	return *s, nil
}

func (c *Controller) ReconcileRetention(ctx context.Context, q RetentionRequest) (RetentionStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var s RetentionStatus
	if e := c.retentionRequest(q); e != nil {
		return s, e
	}
	s, e := ReadRetention(c.plan.Root)
	if e != nil {
		return s, e
	}
	if s.Review.Checksum != q.ReviewChecksum {
		return s, errors.New("retention journal belongs to a different review")
	}
	if s.Phase == "complete" {
		return s, nil
	}
	if e = c.retentionOffline(ctx); e != nil {
		return s, e
	}
	cursor := s.Cursor
	if s.Pending != nil {
		if _, e = os.Lstat(filepath.Join(c.plan.Root, s.Pending.Path)); !os.IsNotExist(e) {
			return s, errors.New("uncertain target still exists or is unreadable; create a NEW retention-plan --resume review and explicitly approve its checksum")
		}
		cursor++
	}
	if e = c.retentionRemaining(ctx, s.Review, cursor); e != nil {
		return s, e
	}
	s.Cursor = cursor
	s.Pending = nil
	s.Phase = "running"
	if e = c.writeRetention(&s); e != nil {
		return retentionResult(&s, c.retentionFailure(&s, "reconciliation marker durability unknown"))
	}
	if s.Cursor == len(retentionOperations(s.Review)) {
		return c.completeRetention(ctx, &s)
	}
	return s, nil
}

func removeRetentionEntry(r RetentionReview, op retentionOperation) error {
	root, e := os.OpenRoot(r.Root)
	if e != nil {
		return e
	}
	defer root.Close()
	parentPath := filepath.Dir(op.Path)
	parent, e := root.OpenRoot(parentPath)
	if e != nil {
		return e
	}
	defer parent.Close()
	var expectedParent RetentionEntry
	found := false
	if parentPath == "versions" {
		expectedParent = r.VersionsIdentity
		found = true
	} else {
		for _, tree := range r.Inventory {
			for _, entry := range tree.Entries {
				if entry.Directory && filepath.Join("versions", tree.PlanChecksum, entry.Path) == parentPath {
					expectedParent = entry
					found = true
				}
			}
		}
	}
	if !found {
		return errors.New("unapproved deletion parent")
	}
	parentStat, e := parent.Stat(".")
	if e != nil {
		return e
	}
	ps, ok := parentStat.Sys().(*syscall.Stat_t)
	if !ok || uint64(ps.Dev) != expectedParent.Device || ps.Ino != expectedParent.Inode || ps.Uid != expectedParent.UID || parentStat.Mode().Perm() != 0700 || parentStat.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errors.New("deletion parent identity changed")
	}
	base := filepath.Base(op.Path)
	info, e := parent.Lstat(base)
	if e != nil {
		return e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	want := op.Entry
	if !ok || uint64(st.Dev) != want.Device || st.Ino != want.Inode || st.Uid != want.UID || uint32(info.Mode().Perm()) != want.Mode || info.IsDir() != want.Directory || info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errors.New("deletion target identity changed")
	}
	if !want.Directory {
		if !info.Mode().IsRegular() || st.Nlink != 1 || info.Size() != want.Bytes {
			return errors.New("deletion file metadata changed")
		}
		f, e := parent.Open(base)
		if e != nil {
			return e
		}
		raw, e := io.ReadAll(io.LimitReader(f, want.Bytes+1))
		after, se := f.Stat()
		_ = f.Close()
		if e != nil || se != nil || !os.SameFile(info, after) || int64(len(raw)) != want.Bytes || routing.Checksum(string(raw)) != want.SHA256 {
			return errors.New("deletion file bytes changed")
		}
	}
	if e = parent.Remove(base); e != nil {
		return e
	}
	if _, e = parent.Lstat(base); !os.IsNotExist(e) {
		return errors.New("deleted target absence unverified")
	}
	f, e := parent.Open(".")
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
