package as112ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/routing"
)

const MaxRetainedVersions = 8
const MaxRetainedVersionBytes = 128 << 20

type ReplacementRequest struct {
	Previous             Plan
	Next                 Plan
	PreviousPlanChecksum string
	CurrentTarget        string
	Approval             Approval
}

// ReplacementStatus contains no configuration or credentials. The marker is a
// separate durable filesystem transaction; opening/reading it never resumes it.
type ReplacementStatus struct {
	Version              int       `json:"version"`
	Sequence             uint64    `json:"sequence"`
	BindingChecksum      string    `json:"bindingChecksum"`
	PreviousPlanChecksum string    `json:"previousPlanChecksum"`
	NextPlanChecksum     string    `json:"nextPlanChecksum"`
	PreviousTarget       string    `json:"previousTarget"`
	NextTarget           string    `json:"nextTarget"`
	StagingName          string    `json:"stagingName"`
	Phase                string    `json:"phase"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

func replacementBinding(previous, next Plan) string {
	return routing.Checksum(previous.Checksum + "\n" + next.Checksum + "\n" + filepath.Join("versions", previous.Checksum))
}
func ReadReplacement(root string) (ReplacementStatus, error) {
	var marker ReplacementStatus
	if e := privateDir(root); e != nil {
		return marker, e
	}
	raw, e := privateRead(filepath.Join(root, "replacement.json"), 16<<10)
	if e != nil {
		return marker, e
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&marker) != nil || d.Decode(new(any)) != io.EOF {
		return marker, errors.New("invalid replacement journal")
	}
	canonical, _ := json.MarshalIndent(marker, "", "  ")
	if string(raw) != string(canonical)+"\n" || marker.Version != 1 || marker.Sequence == 0 || marker.Sequence > 1000000 || !validHash(marker.BindingChecksum) || !validHash(marker.PreviousPlanChecksum) || !validHash(marker.NextPlanChecksum) || marker.PreviousPlanChecksum == marker.NextPlanChecksum || marker.PreviousTarget != filepath.Join("versions", marker.PreviousPlanChecksum) || marker.NextTarget != filepath.Join("versions", marker.NextPlanChecksum) || !validStagingName(marker.StagingName) {
		return marker, errors.New("invalid replacement journal binding")
	}
	switch marker.Phase {
	case "version-intent", "version-ready", "pointer-intent", "complete":
	default:
		return marker, errors.New("invalid replacement journal phase")
	}
	return marker, nil
}
func validStagingName(s string) bool {
	if !strings.HasPrefix(s, ".replacement-") || len(s) > 64 {
		return false
	}
	for _, c := range strings.TrimPrefix(s, ".replacement-") {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > len(".replacement-")
}
func writeReplacement(root string, m ReplacementStatus) error {
	raw, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	return writeAtomic(root, "replacement.json", append(raw, '\n'))
}

func validateReplacementPair(previous, next Plan) error {
	if e := ValidatePlan(previous); e != nil {
		return e
	}
	if e := ValidatePlan(next); e != nil {
		return e
	}
	if previous.Checksum == next.Checksum || previous.Root != next.Root || previous.Platform != next.Platform || previous.RouterID != next.RouterID || previous.RouterCreatedAt != next.RouterCreatedAt {
		return errors.New("replacement requires distinct plans for the same root, platform, host and router entity")
	}
	actual, e := CaptureTarget()
	if e != nil || actual != next.Host {
		return errors.New("next replacement plan must name the current execution target")
	}
	s, e := ReadStatus(previous.Root)
	if e != nil {
		return e
	}
	if (s.PlanChecksum != previous.Checksum && s.PlanChecksum != next.Checksum) || boundTarget(previous, s) != next.Host {
		return errors.New("replacement cannot rebind a host, boot or network namespace")
	}
	if e = verifyTarget(previous, s); e != nil {
		return e
	}
	if s.Binding != nil && !slices.Contains(s.Binding.PlanHosts, next.Host) && len(s.Binding.PlanHosts) >= 128 {
		return errors.New("instance host history bound exhausted")
	}
	return nil
}

// OpenForReplacement obtains the ordinary instance lock using whichever exact
// reviewed plan the status currently names. It never advances a pending marker.
func OpenForReplacement(previous, next Plan, r Runtime) (*Controller, error) {
	if e := validateReplacementPair(previous, next); e != nil {
		return nil, e
	}
	s, e := ReadStatus(previous.Root)
	if e != nil {
		return nil, e
	}
	switch s.PlanChecksum {
	case previous.Checksum:
		return Open(previous, r)
	case next.Checksum:
		return Open(next, r)
	default:
		return nil, errors.New("instance status names neither reviewed replacement plan")
	}
}
func (c *Controller) validateReplacementRequest(q ReplacementRequest) error {
	if e := validateReplacementPair(q.Previous, q.Next); e != nil {
		return e
	}
	if q.PreviousPlanChecksum != q.Previous.Checksum || q.CurrentTarget != filepath.Join("versions", q.Previous.Checksum) || !q.Approval.ExclusiveInstanceAcknowledged || q.Approval.PlanChecksum != q.Next.Checksum || q.Approval.ActiveChecksum != q.Next.ActiveChecksum || q.Approval.StandbyChecksum != q.Next.StandbyChecksum || q.Approval.DNSChecksum != q.Next.DNSChecksum {
		return errors.New("explicit previous pointer and complete next-plan approval required")
	}
	if c.closed || c.plan.Root != q.Previous.Root || (c.plan.Checksum != q.Previous.Checksum && c.plan.Checksum != q.Next.Checksum) {
		return errors.New("replacement controller belongs to a different plan")
	}
	return nil
}
func (c *Controller) replacementAbsent(ctx context.Context) error {
	o, e := c.runtime.Observe(ctx, c.plan)
	if e != nil || len(o.Processes) != 0 {
		return errors.New("offline replacement requires verified absence of observable dedicated-instance processes under the service UID")
	}
	return nil
}
func verifyPlanVersion(p Plan, dir string) error {
	view := Controller{plan: p}
	return view.verifyVersion(dir)
}
func exactCurrent(root, target string) error {
	got, e := os.Readlink(filepath.Join(root, "current"))
	if e != nil || got != target {
		return errors.New("current pointer differs from explicit replacement base")
	}
	return nil
}

func replacementCapacity(root string, next Plan) error {
	versions := filepath.Join(root, "versions")
	if e := privateDir(versions); e != nil {
		return e
	}
	entries, e := os.ReadDir(versions)
	if e != nil {
		return e
	}
	if len(entries) >= MaxRetainedVersions {
		return errors.New("retained version count exhausted; explicit retention work is required")
	}
	var bytes int64
	for _, entry := range entries {
		if !entry.IsDir() || !validHash(entry.Name()) {
			return errors.New("unexpected or incomplete version requires operator review")
		}
		e = filepath.WalkDir(filepath.Join(versions, entry.Name()), func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			s, e := d.Info()
			if e != nil {
				return e
			}
			if s.IsDir() {
				return privateDir(path)
			}
			if !s.Mode().IsRegular() || s.Mode()&os.ModeSymlink != 0 {
				return errors.New("non-regular retained version object")
			}
			bytes += s.Size()
			if bytes > MaxRetainedVersionBytes {
				return errors.New("retained version byte bound exceeded")
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	b, e := as112.Decode(next.DNSBundle)
	if e != nil {
		return e
	}
	bytes += int64(len(next.ActiveConfig) + len(next.StandbyConfig) + len(next.DNSBundle))
	for _, f := range b.Files {
		bytes += int64(len(f.Content))
	}
	if bytes > MaxRetainedVersionBytes {
		return errors.New("replacement would exceed retained version byte bound")
	}
	return nil
}
func (c *Controller) stageReplacement(ctx context.Context, next Plan) (string, error) {
	dir, e := os.MkdirTemp(filepath.Join(next.Root, "versions"), ".replacement-")
	if e != nil {
		return "", e
	}
	good := false
	defer func() {
		if !good {
			_ = os.RemoveAll(dir)
		}
	}()
	b, _ := as112.Decode(next.DNSBundle)
	if e = as112.WriteNewDirectory(filepath.Join(dir, "dns"), b); e != nil {
		return "", e
	}
	for name, raw := range map[string]string{"active.conf": next.ActiveConfig, "standby.conf": next.StandbyConfig, "bundle.json": next.DNSBundle} {
		if e = writeNew(filepath.Join(dir, name), []byte(raw)); e != nil {
			return "", e
		}
	}
	for _, d := range []string{filepath.Join(dir, "dns", "zones"), filepath.Join(dir, "dns"), dir} {
		if e = syncDir(d); e != nil {
			return "", e
		}
	}
	if e = c.runtime.Validate(ctx, next, dir); e != nil {
		return "", errors.New("replacement native validation failed")
	}
	if e = verifyPlanVersion(next, dir); e != nil {
		return "", e
	}
	good = true
	return dir, nil
}
func (c *Controller) replacementMark(m *ReplacementStatus, phase string) error {
	m.Phase = phase
	m.UpdatedAt = c.now().UTC()
	if e := writeReplacement(c.plan.Root, *m); e != nil {
		return c.fail("replacement journal write failed")
	}
	return nil
}

func (c *Controller) Replace(ctx context.Context, q ReplacementRequest) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.validateReplacementRequest(q); e != nil {
		return c.snapshot(), e
	}
	if e := c.ready(); e != nil {
		return c.snapshot(), e
	}
	if c.plan.Checksum != q.Previous.Checksum || c.state.PlanChecksum != q.Previous.Checksum || !c.state.Installed || c.state.Phase != "withdrawn" || c.state.Pending != nil || c.state.DNS != nil || c.state.BGP != nil {
		return c.snapshot(), errors.New("replacement requires a completed, withdrawn prior plan with no pending operation")
	}
	if c.state.Sequence >= 1000000 {
		return c.snapshot(), errors.New("journal sequence exhausted")
	}
	if e := c.replacementAbsent(ctx); e != nil {
		return c.snapshot(), e
	}
	if e := c.verifyInstalled(); e != nil {
		return c.snapshot(), e
	}
	if e := exactCurrent(q.Previous.Root, q.CurrentTarget); e != nil {
		return c.snapshot(), e
	}
	if old, e := ReadReplacement(c.plan.Root); e == nil {
		if old.Phase != "complete" || old.NextPlanChecksum != q.Previous.Checksum {
			return c.snapshot(), errors.New("previous replacement must be explicitly reconciled")
		}
	} else if !os.IsNotExist(e) {
		return c.snapshot(), e
	}
	if e := replacementCapacity(c.plan.Root, q.Next); e != nil {
		return c.snapshot(), e
	}
	dir, e := c.stageReplacement(ctx, q.Next)
	if e != nil {
		return c.snapshot(), e
	}
	retained := false
	defer func() {
		if !retained {
			_ = os.RemoveAll(dir)
		}
	}()
	if e = ctx.Err(); e != nil {
		return c.snapshot(), e
	}
	if e = c.replacementAbsent(ctx); e != nil {
		return c.snapshot(), e
	}
	if e = exactCurrent(c.plan.Root, q.CurrentTarget); e != nil {
		return c.snapshot(), e
	}
	m := ReplacementStatus{Version: 1, Sequence: c.state.Sequence + 1, BindingChecksum: replacementBinding(q.Previous, q.Next), PreviousPlanChecksum: q.Previous.Checksum, NextPlanChecksum: q.Next.Checksum, PreviousTarget: q.CurrentTarget, NextTarget: filepath.Join("versions", q.Next.Checksum), StagingName: filepath.Base(dir)}
	// Once intent persistence is attempted its result may be uncertain (for
	// example, rename succeeded but directory fsync failed). Preserve the exact
	// staged bytes even on that error so a durable marker cannot point to data
	// that our cleanup just deleted.
	retained = true
	if e = c.replacementMark(&m, "version-intent"); e != nil {
		return c.snapshot(), e
	}
	e = c.finishReplacement(ctx, q, &m)
	return c.snapshot(), e
}

// ReconcileReplacement is explicit authorization to resolve filesystem effects
// of this exact pair. It never starts, stops, reloads or adopts a service process;
// it may repeat read-only native validation. Every
// rename is preceded by WAL and by actual old/new path evidence; completed
// effects are recognized rather than replayed.
func (c *Controller) ReconcileReplacement(ctx context.Context, q ReplacementRequest) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, e := ReadRetention(c.plan.Root); e == nil && m.Phase != "complete" {
		return c.snapshot(), errors.New("pending retention cannot be bypassed by replacement reconciliation")
	} else if e != nil && !os.IsNotExist(e) {
		return c.snapshot(), e
	}
	if e := c.validateReplacementRequest(q); e != nil {
		return c.snapshot(), e
	}
	if c.state.Pending != nil {
		return c.snapshot(), errors.New("an unrelated process operation is pending")
	}
	if e := c.replacementAbsent(ctx); e != nil {
		return c.snapshot(), e
	}
	m, e := ReadReplacement(c.plan.Root)
	if e != nil {
		return c.snapshot(), e
	}
	if m.BindingChecksum != replacementBinding(q.Previous, q.Next) || m.PreviousPlanChecksum != q.Previous.Checksum || m.NextPlanChecksum != q.Next.Checksum || m.PreviousTarget != q.CurrentTarget {
		return c.snapshot(), errors.New("replacement journal does not authorize this pair")
	}
	if c.state.Sequence > m.Sequence && m.Phase != "complete" {
		return c.snapshot(), errors.New("replacement journal sequence is stale")
	}
	if m.Phase == "complete" {
		if c.state.PlanChecksum != q.Next.Checksum || exactCurrent(c.plan.Root, m.NextTarget) != nil || verifyPlanVersion(q.Next, filepath.Join(c.plan.Root, m.NextTarget)) != nil {
			return c.snapshot(), errors.New("completed replacement evidence differs")
		}
		return c.snapshot(), nil
	}
	e = c.finishReplacement(ctx, q, &m)
	return c.snapshot(), e
}

func (c *Controller) finishReplacement(ctx context.Context, q ReplacementRequest, m *ReplacementStatus) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := c.replacementAbsent(ctx); e != nil {
		return e
	}
	if e := verifyPlanVersion(q.Previous, filepath.Join(c.plan.Root, m.PreviousTarget)); e != nil {
		return errors.New("previous reviewed version changed")
	}
	current, e := os.Readlink(filepath.Join(c.plan.Root, "current"))
	if e != nil || current != m.PreviousTarget && current != m.NextTarget {
		return errors.New("replacement current pointer is neither approved endpoint")
	}
	if c.state.PlanChecksum == q.Next.Checksum && current != m.NextTarget {
		return errors.New("committed status contradicts current pointer")
	}
	stage := filepath.Join(c.plan.Root, "versions", m.StagingName)
	next := filepath.Join(c.plan.Root, m.NextTarget)
	_, se := os.Lstat(stage)
	_, ne := os.Lstat(next)
	stageExists, nextExists := se == nil, ne == nil
	if se != nil && !os.IsNotExist(se) || ne != nil && !os.IsNotExist(ne) {
		return errors.New("replacement filesystem presence cannot be verified")
	}
	if m.Phase == "version-intent" {
		if current != m.PreviousTarget || stageExists == nextExists {
			return errors.New("ambiguous replacement version publication")
		}
		if stageExists {
			if e = verifyPlanVersion(q.Next, stage); e != nil {
				return e
			}
			if e = c.runtime.Validate(ctx, q.Next, stage); e != nil {
				return errors.New("replacement native validation no longer holds")
			}
			if e = c.replacementAbsent(ctx); e != nil {
				return e
			}
			if e = c.replacementMark(m, "version-intent"); e != nil {
				return e
			}
			if e = os.Rename(stage, next); e != nil {
				return c.fail("replacement version publication unknown")
			}
			if e = syncDir(filepath.Dir(next)); e != nil {
				return c.fail("replacement version durability unknown")
			}
		}
		if e = verifyPlanVersion(q.Next, next); e != nil {
			return e
		}
		if e = c.replacementMark(m, "version-ready"); e != nil {
			return e
		}
		stageExists = false
		nextExists = true
	}
	if !nextExists || stageExists {
		return errors.New("replacement final version missing or staging ambiguous")
	}
	if e = verifyPlanVersion(q.Next, next); e != nil {
		return e
	}
	if m.Phase == "version-ready" {
		if current != m.PreviousTarget {
			return errors.New("pointer changed without durable switch intent")
		}
		if e = exactCurrent(c.plan.Root, m.PreviousTarget); e != nil {
			return e
		}
		if e = c.replacementAbsent(ctx); e != nil {
			return e
		}
		if e = c.replacementMark(m, "pointer-intent"); e != nil {
			return e
		}
	}
	if m.Phase != "pointer-intent" {
		return errors.New("unsupported replacement recovery phase")
	}
	if current == m.PreviousTarget {
		if e = c.replacementAbsent(ctx); e != nil {
			return e
		}
		if e = exactCurrent(c.plan.Root, m.PreviousTarget); e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		// A private unique temporary link avoids reusing an uncertain old link.
		f, err := os.CreateTemp(c.plan.Root, ".replacement-link-")
		if err != nil {
			return err
		}
		name := f.Name()
		_ = f.Close()
		if err = os.Remove(name); err != nil {
			return err
		}
		defer os.Remove(name)
		if err = os.Symlink(m.NextTarget, name); err != nil {
			return err
		}
		if err = os.Rename(name, filepath.Join(c.plan.Root, "current")); err != nil {
			return c.fail("replacement pointer result unknown")
		}
		if err = syncDir(c.plan.Root); err != nil {
			return c.fail("replacement pointer durability unknown")
		}
	}
	if e = exactCurrent(c.plan.Root, m.NextTarget); e != nil {
		return e
	}
	if e = c.replacementAbsent(ctx); e != nil {
		return e
	}
	if c.state.Binding != nil && !slices.Contains(c.state.Binding.PlanHosts, q.Next.Host) {
		c.state.Binding.PlanHosts = append(c.state.Binding.PlanHosts, q.Next.Host)
	}
	c.plan = q.Next
	c.state.PlanChecksum = q.Next.Checksum
	c.state.RouterID = q.Next.RouterID
	c.state.Phase = "withdrawn"
	c.state.Installed = true
	c.state.Pending = nil
	c.state.DNS = nil
	c.state.BGP = nil
	c.state.HealthyChecks = 0
	c.state.LastHealthy = false
	c.state.Sequence = m.Sequence
	c.state.Reason = "replacement-complete-recovery-required"
	c.fresh = false
	if e = c.saveStatus(); e != nil {
		return e
	}
	return c.replacementMark(m, "complete")
}
