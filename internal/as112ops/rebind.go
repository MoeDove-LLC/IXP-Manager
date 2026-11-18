package as112ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

const MaxRebindReviewBytes = 512 << 10

// RebindReview contains only local identity, hashes and file metadata. It cannot
// grant new artifacts, native executable hashes or permission to announce.
type RebindReview struct {
	Version              int             `json:"version"`
	Mode                 string          `json:"mode"`
	Root                 string          `json:"root"`
	PlanChecksum         string          `json:"planChecksum"`
	ActiveChecksum       string          `json:"activeChecksum"`
	StandbyChecksum      string          `json:"standbyChecksum"`
	DNSChecksum          string          `json:"dnsChecksum"`
	Tools                Tools           `json:"tools"`
	PreviousHost         TargetPin       `json:"previousHost"`
	NextBinding          InstanceBinding `json:"nextBinding"`
	BeforeStatusChecksum string          `json:"beforeStatusChecksum"`
	BeforeSequence       uint64          `json:"beforeSequence"`
	ReplacementChecksum  string          `json:"replacementChecksum"`
	RetentionChecksum    string          `json:"retentionChecksum"`
	CurrentTarget        string          `json:"currentTarget"`
	RootIdentity         RetentionEntry  `json:"rootIdentity"`
	VersionsIdentity     RetentionEntry  `json:"versionsIdentity"`
	Inventory            []RetentionTree `json:"inventory"`
	ReviewedAt           time.Time       `json:"reviewedAt"`
	Checksum             string          `json:"checksum"`
}
type RebindRequest struct {
	Review                        RebindReview
	ReviewChecksum                string
	ExclusiveInstanceAcknowledged bool
	SourceIsolatedAcknowledged    bool
}

// RebindSession holds only the instance lock. Its Close never calls a process
// method, even when the persisted old journal contains stale numeric PIDs.
type RebindSession struct {
	mu      sync.Mutex
	plan    Plan
	runtime Runtime
	lock    *os.File
	closed  bool
}

func OpenForRebind(p Plan, r Runtime) (*RebindSession, error) {
	if e := ValidatePlan(p); e != nil {
		return nil, e
	}
	if e := privateDir(p.Root); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(p.Root, "lock"), os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	s, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		f.Close()
		return nil, errors.New("unsafe existing instance lock")
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("AS112 instance already has an owner")
	}
	if r == nil {
		r = NewNativeRuntime()
	}
	return &RebindSession{plan: p, runtime: r, lock: f}, nil
}
func (s *RebindSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN), s.lock.Close())
}
func rebindHash(r RebindReview) string {
	r.Checksum = ""
	raw, _ := json.Marshal(r)
	return routing.Checksum(string(raw))
}
func validateRebindReview(r RebindReview) error {
	if r.Version != 1 || (r.Mode != "same-host-reboot" && r.Mode != "move") || !cleanAbs(r.Root) || !validHash(r.PlanChecksum) || !validHash(r.BeforeStatusChecksum) || r.BeforeSequence >= 1000000 || r.Checksum != rebindHash(r) || !validHash(r.Checksum) || validateBinding(&r.NextBinding) != nil || r.NextBinding.ReviewChecksum != "" || !validTarget(r.PreviousHost) || r.CurrentTarget != filepath.Join("versions", r.PlanChecksum) || len(r.Inventory) == 0 || len(r.Inventory) > MaxRetainedVersions || r.ReviewedAt.IsZero() {
		return errors.New("invalid rebind review")
	}
	for _, h := range []string{r.ActiveChecksum, r.StandbyChecksum, r.DNSChecksum, r.ReplacementChecksum, r.RetentionChecksum} {
		if h != "" && !validHash(h) {
			return errors.New("invalid rebind evidence checksum")
		}
	}
	if r.Mode == "same-host-reboot" && (r.PreviousHost.BootID == r.NextBinding.Host.BootID || r.NextBinding.HostIdentity == "") {
		return errors.New("same-host reboot requires changed boot and an established stable host identity")
	}
	return nil
}
func EncodeRebindReview(r RebindReview) ([]byte, error) {
	if e := validateRebindReview(r); e != nil {
		return nil, e
	}
	raw, e := json.MarshalIndent(r, "", "  ")
	if len(raw) > MaxRebindReviewBytes {
		return nil, errors.New("rebind review exceeds bound")
	}
	return append(raw, '\n'), e
}
func DecodeRebindReview(raw []byte) (RebindReview, error) {
	var r RebindReview
	if len(raw) > MaxRebindReviewBytes {
		return r, errors.New("rebind review exceeds bound")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return r, errors.New("invalid rebind review JSON")
	}
	canonical, e := EncodeRebindReview(r)
	if e != nil {
		return r, e
	}
	if string(canonical) != string(raw) {
		return r, errors.New("rebind review must retain canonical encoding")
	}
	return r, nil
}
func statusChecksum(s Status) string {
	raw, _ := json.MarshalIndent(s, "", "  ")
	return routing.Checksum(string(append(raw, '\n')))
}
func reboundStatus(before Status, r RebindReview) Status {
	out := before
	out.Binding = copyBinding(&r.NextBinding)
	out.Binding.ReviewChecksum = r.Checksum
	out.Phase = "withdrawn"
	out.Pending = nil
	out.DNS = nil
	out.BGP = nil
	out.HealthyChecks = 0
	out.LastHealthy = false
	out.Sequence = r.BeforeSequence + 1
	out.Reason = "target-rebound-fresh-recovery-required"
	out.UpdatedAt = r.ReviewedAt
	return out
}
func (s *RebindSession) evidence(ctx context.Context) (Status, RebindReview, error) {
	var r RebindReview
	var state Status
	if s.closed {
		return state, r, errors.New("rebind session closed")
	}
	if e := ctx.Err(); e != nil {
		return state, r, e
	}
	if e := privateDir(s.plan.Root); e != nil {
		return state, r, e
	}
	var e error
	state, e = ReadStatus(s.plan.Root)
	if e != nil {
		return state, r, e
	}
	if state.PlanChecksum != s.plan.Checksum || state.RouterID != s.plan.RouterID || !state.Installed || state.Pending != nil || (state.Phase != "withdrawn" && state.Phase != "active") || state.Sequence > 1000000 {
		return state, r, errors.New("rebind requires a known installed state with no pending process operation")
	}
	// Old confirmed active records are permitted only after fresh absence proof;
	// neither their numeric PIDs nor their old health counters convey authority.
	if m, e := ReadReplacement(s.plan.Root); e == nil {
		if m.Phase != "complete" || m.NextPlanChecksum != s.plan.Checksum {
			return state, r, errors.New("replacement recovery blocks target rebind")
		}
	} else if !os.IsNotExist(e) {
		return state, r, e
	}
	if m, e := ReadRetention(s.plan.Root); e == nil {
		if m.Phase != "complete" || m.Pending != nil {
			return state, r, errors.New("retention recovery blocks target rebind")
		}
	} else if !os.IsNotExist(e) {
		return state, r, e
	}
	if e = retentionMounts(s.plan.Root); e != nil {
		return state, r, e
	}
	view := Controller{plan: s.plan}
	if e = view.verifyInstalled(); e != nil {
		return state, r, e
	}
	r.Root = s.plan.Root
	r.PlanChecksum = s.plan.Checksum
	r.ActiveChecksum = s.plan.ActiveChecksum
	r.StandbyChecksum = s.plan.StandbyChecksum
	r.DNSChecksum = s.plan.DNSChecksum
	r.Tools = s.plan.Tools
	r.PreviousHost = boundTarget(s.plan, state)
	r.BeforeStatusChecksum, e = retainedFileHash(s.plan.Root, "status.json")
	if e != nil {
		return state, r, e
	}
	if r.BeforeStatusChecksum != statusChecksum(state) {
		return state, r, errors.New("noncanonical or concurrently changed status blocks rebind")
	}
	r.BeforeSequence = state.Sequence
	r.CurrentTarget = filepath.Join("versions", s.plan.Checksum)
	if r.ReplacementChecksum, e = retainedFileHash(s.plan.Root, "replacement.json"); e != nil {
		return state, r, e
	}
	if r.RetentionChecksum, e = retainedFileHash(s.plan.Root, "retention.json"); e != nil {
		return state, r, e
	}
	if r.RootIdentity, e = captureRetentionEntry(s.plan.Root, ""); e != nil {
		return state, r, e
	}
	if r.VersionsIdentity, e = captureRetentionEntry(filepath.Join(s.plan.Root, "versions"), ""); e != nil {
		return state, r, e
	}
	entries, e := os.ReadDir(filepath.Join(s.plan.Root, "versions"))
	if e != nil {
		return state, r, e
	}
	if len(entries) == 0 || len(entries) > MaxRetainedVersions {
		return state, r, errors.New("invalid retained version count")
	}
	var bytes int64
	for _, entry := range entries {
		if !entry.IsDir() || !validHash(entry.Name()) {
			return state, r, errors.New("unrecognized staging or version blocks rebind")
		}
		tree, e := captureRetentionTree(s.plan.Root, entry.Name())
		if e != nil {
			return state, r, e
		}
		r.Inventory = append(r.Inventory, tree)
		bytes += tree.Bytes
	}
	if bytes > MaxRetainedVersionBytes {
		return state, r, errors.New("retained version bound exceeded")
	}
	// Unknown publication scratch also remains evidence for operator inspection.
	roots, e := os.ReadDir(s.plan.Root)
	if e != nil {
		return state, r, e
	}
	for _, entry := range roots {
		if strings.HasPrefix(entry.Name(), ".replacement-") || strings.HasPrefix(entry.Name(), ".current-") || strings.HasPrefix(entry.Name(), ".write-") {
			return state, r, errors.New("unresolved publication scratch blocks rebind")
		}
	}
	o, e := s.runtime.Observe(ctx, s.plan)
	if e != nil || len(o.Processes) != 0 {
		return state, r, errors.New("rebind requires absence of observable owned or foreign instance processes under the service UID")
	}
	h, e := retainedFileHash(s.plan.Root, "status.json")
	if e != nil || h != r.BeforeStatusChecksum {
		return state, r, errors.New("status changed during rebind observation")
	}
	return state, r, ctx.Err()
}
func rebindMode(mode string, before *InstanceBinding, next InstanceBinding, previous TargetPin) error {
	if mode == "same-host-reboot" {
		if before == nil || before.HostIdentity == "" || next.HostIdentity == "" || before.HostIdentity != next.HostIdentity || previous.BootID == next.Host.BootID {
			return errors.New("same-host reboot requires prior matching stable identity and changed boot; unproven hosts require explicit move mode")
		}
	} else if mode != "move" {
		return errors.New("rebind mode must be same-host-reboot or move")
	}
	return nil
}
func (s *RebindSession) Review(ctx context.Context, mode string) (RebindReview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, r, e := s.evidence(ctx)
	if e != nil {
		return r, e
	}
	b := copyBinding(state.Binding)
	if b == nil {
		b, e = newBinding(s.plan)
		if e != nil {
			return r, e
		}
	}
	b.Host, e = CaptureTarget()
	if e != nil {
		return r, e
	}
	b.HostIdentity = captureHostIdentity(b.InstanceID)
	b.ReviewChecksum = ""
	if e = rebindMode(mode, state.Binding, *b, r.PreviousHost); e != nil {
		return r, e
	}
	r.Version = 1
	r.Mode = mode
	r.NextBinding = *b
	r.ReviewedAt = time.Now().UTC()
	r.Checksum = rebindHash(r)
	return r, validateRebindReview(r)
}
func (s *RebindSession) request(q RebindRequest) error {
	r := q.Review
	if e := validateRebindReview(r); e != nil {
		return e
	}
	if q.ReviewChecksum != r.Checksum || !q.ExclusiveInstanceAcknowledged || r.Mode == "move" && !q.SourceIsolatedAcknowledged {
		return errors.New("exact rebind checksum, exclusive ownership and move source-isolation acknowledgment required")
	}
	if r.PlanChecksum != s.plan.Checksum || r.Root != s.plan.Root || r.Tools != s.plan.Tools || r.ActiveChecksum != s.plan.ActiveChecksum || r.StandbyChecksum != s.plan.StandbyChecksum || r.DNSChecksum != s.plan.DNSChecksum {
		return errors.New("rebind cannot change the reviewed plan, artifacts or tool pins")
	}
	actual, e := CaptureTarget()
	if e != nil || actual != r.NextBinding.Host || captureHostIdentity(r.NextBinding.InstanceID) != r.NextBinding.HostIdentity {
		return errors.New("reviewed destination target changed")
	}
	return nil
}
func sameRebindEvidence(actual, want RebindReview) bool {
	actual.Version = want.Version
	actual.Mode = want.Mode
	actual.NextBinding = want.NextBinding
	actual.ReviewedAt = want.ReviewedAt
	actual.Checksum = want.Checksum
	return reflect.DeepEqual(actual, want)
}
func (s *RebindSession) inspect(ctx context.Context, q RebindRequest) (Status, string, error) {
	if e := s.request(q); e != nil {
		return Status{}, "", e
	}
	state, actual, e := s.evidence(ctx)
	if e != nil {
		return state, "", e
	}
	r := q.Review
	if state.Binding != nil && state.Binding.ReviewChecksum == r.Checksum {
		// The atomic result is recognized, never rewritten. Restore only the old
		// evidence fields for comparison; all unchanged files/journals still match.
		actual.PreviousHost = r.PreviousHost
		actual.BeforeStatusChecksum = r.BeforeStatusChecksum
		actual.BeforeSequence = r.BeforeSequence
		expected := reboundStatus(state, r)
		if state.Sequence != r.BeforeSequence+1 || !reflect.DeepEqual(state, expected) || !sameRebindEvidence(actual, r) {
			return state, "", errors.New("committed rebind evidence has changed")
		}
		return state, "applied", nil
	}
	if !sameRebindEvidence(actual, r) {
		return state, "", errors.New("instance differs from exact reviewed rebind base")
	}
	if state.Binding != nil {
		if state.Binding.InstanceID != r.NextBinding.InstanceID || !slices.Equal(state.Binding.PlanHosts, r.NextBinding.PlanHosts) {
			return state, "", errors.New("local instance identity cannot change during rebind")
		}
	} else if !slices.Equal(r.NextBinding.PlanHosts, []TargetPin{s.plan.Host}) {
		return state, "", errors.New("invalid legacy instance binding")
	}
	if e = rebindMode(r.Mode, state.Binding, r.NextBinding, r.PreviousHost); e != nil {
		return state, "", e
	}
	return state, "unapplied", nil
}

// Confirm performs only reads. Apply is an explicit compare-and-swap of one
// complete status file: a crash leaves its exact old or exact reviewed new bytes.
// Repeating an explicitly requested Apply rechecks all evidence; it never repeats
// an unknown service, replacement or retention operation.
func (s *RebindSession) Confirm(ctx context.Context, q RebindRequest) (Status, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inspect(ctx, q)
}
func (s *RebindSession) Apply(ctx context.Context, q RebindRequest) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, result, e := s.inspect(ctx, q)
	if e != nil || result == "applied" {
		return state, e
	}
	if e = ctx.Err(); e != nil {
		return state, e
	}
	next := reboundStatus(state, q.Review)
	raw, e := json.MarshalIndent(next, "", "  ")
	if e != nil {
		return state, e
	}
	if e = writeAtomic(s.plan.Root, "status.json", append(raw, '\n')); e != nil {
		return state, errors.New("rebind status persistence uncertain; use explicit rebind-confirm before continuing")
	}
	return next, nil
}
