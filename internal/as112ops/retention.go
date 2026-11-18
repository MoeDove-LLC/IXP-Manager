package as112ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/routing"
)

const MaxRetentionReviewBytes = 512 << 10
const maxRetentionEntries = 1024

type RetentionEntry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	UID       uint32 `json:"uid"`
	Mode      uint32 `json:"mode"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256,omitempty"`
}
type RetentionTree struct {
	PlanChecksum string           `json:"planChecksum"`
	Entries      []RetentionEntry `json:"entries"`
	Bytes        int64            `json:"bytes"`
}
type RetentionSource struct {
	Host                  *TargetPin `json:"host,omitempty"`
	PlanChecksum          string     `json:"planChecksum"`
	ActiveChecksum        string     `json:"activeChecksum"`
	StandbyChecksum       string     `json:"standbyChecksum"`
	DNSChecksum           string     `json:"dnsChecksum"`
	Tools                 Tools      `json:"tools"`
	HealthIntervalSeconds int        `json:"healthIntervalSeconds"`
	RecoveryHealthyChecks int        `json:"recoveryHealthyChecks"`
}
type RetentionProtected struct {
	PlanChecksum string `json:"planChecksum"`
	Reason       string `json:"reason"`
}
type RetentionReview struct {
	Version                   int                  `json:"version"`
	Root                      string               `json:"root"`
	Host                      TargetPin            `json:"host"`
	CurrentPlanChecksum       string               `json:"currentPlanChecksum"`
	CurrentTarget             string               `json:"currentTarget"`
	StateChecksum             string               `json:"stateChecksum"`
	StateSequence             uint64               `json:"stateSequence"`
	ReplacementChecksum       string               `json:"replacementChecksum"`
	PreviousRetentionChecksum string               `json:"previousRetentionChecksum"`
	ResumeOf                  string               `json:"resumeOf,omitempty"`
	PreviousPhase             string               `json:"previousPhase,omitempty"`
	PreviousIntent            *RetentionIntent     `json:"previousIntent,omitempty"`
	StartCursor               int                  `json:"startCursor"`
	RootIdentity              RetentionEntry       `json:"rootIdentity"`
	VersionsIdentity          RetentionEntry       `json:"versionsIdentity"`
	Inventory                 []RetentionTree      `json:"inventory"`
	Candidates                []RetentionSource    `json:"candidates"`
	Protected                 []RetentionProtected `json:"protected"`
	ReviewedAt                time.Time            `json:"reviewedAt"`
	Checksum                  string               `json:"checksum"`
}
type RetentionRequest struct {
	Review                        RetentionReview
	ReviewChecksum                string
	ExclusiveInstanceAcknowledged bool
	DeleteAcknowledged            bool
}
type RetentionIntent struct {
	Index     int       `json:"index"`
	Path      string    `json:"path"`
	StartedAt time.Time `json:"startedAt"`
}
type RetentionStatus struct {
	Version   int              `json:"version"`
	Review    RetentionReview  `json:"review"`
	Phase     string           `json:"phase"`
	Cursor    int              `json:"cursor"`
	Pending   *RetentionIntent `json:"pending,omitempty"`
	UpdatedAt time.Time        `json:"updatedAt"`
}
type retentionOperation struct {
	Tree  string
	Entry RetentionEntry
	Path  string
}

var retainedZone = regexp.MustCompile(`^zones/zone-[0-9]{2}\.zone$`)

// This decoder is for deleting inactive historical files only. It binds the
// original canonical plan and artifact hashes, without requiring its old tools
// to exist or passing old configurations through today's activation renderer.
// It must never be used by Install, Replace, or Run.
func DecodeRetentionHistory(raw []byte) (Plan, error) {
	var p Plan
	if len(raw) > 40<<20 {
		return p, errors.New("historical plan too large")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return p, errors.New("invalid historical plan JSON")
	}
	canonical, _ := json.MarshalIndent(p, "", "  ")
	if string(raw) != string(canonical)+"\n" {
		return p, errors.New("historical plan must retain canonical encoding")
	}
	_, e := retentionFiles(p)
	return p, e
}
func retentionFiles(p Plan) (map[string]string, error) {
	if p.Version != 1 || !cleanAbs(p.Root) || !supported(p.Platform) || !validHash(p.Checksum) || p.Checksum != digest(p) || p.ActiveChecksum != routing.Checksum(p.ActiveConfig) || p.StandbyChecksum != routing.Checksum(p.StandbyConfig) || p.DNSChecksum != routing.Checksum(p.DNSBundle) || len(p.ActiveConfig) == 0 || len(p.ActiveConfig) > 16<<20 || len(p.StandbyConfig) == 0 || len(p.StandbyConfig) > 16<<20 || len(p.DNSBundle) > as112.MaxBundleBytes {
		return nil, errors.New("historical plan/artifact ownership hashes are invalid")
	}
	var b struct {
		Version         int          `json:"version"`
		Kind            string       `json:"kind"`
		RouterID        string       `json:"routerId"`
		RouterCreatedAt string       `json:"routerCreatedAt"`
		Files           []as112.File `json:"files"`
	}
	if json.Unmarshal([]byte(p.DNSBundle), &b) != nil || b.Version != 1 || b.Kind != "as112-dns" || b.RouterID != p.RouterID || b.RouterCreatedAt != p.RouterCreatedAt || len(b.Files) < 3 || len(b.Files) > 102 {
		return nil, errors.New("historical bundle ownership/layout invalid")
	}
	want := map[string]string{"active.conf": p.ActiveConfig, "standby.conf": p.StandbyConfig, "bundle.json": p.DNSBundle}
	seen := map[string]bool{}
	for _, f := range b.Files {
		if seen[f.Path] || f.Path != "pdns.conf" && f.Path != "named.conf" && !retainedZone.MatchString(f.Path) {
			return nil, errors.New("historical bundle contains unapproved file path")
		}
		seen[f.Path] = true
		want["dns/"+f.Path] = f.Content
	}
	if !seen["pdns.conf"] || !seen["named.conf"] {
		return nil, errors.New("historical DNS configuration missing")
	}
	return want, nil
}
func retentionHash(r RetentionReview) string {
	r.Checksum = ""
	b, _ := json.Marshal(r)
	return routing.Checksum(string(b))
}
func EncodeRetentionReview(r RetentionReview) ([]byte, error) {
	if e := validateRetentionReview(r); e != nil {
		return nil, e
	}
	b, e := json.MarshalIndent(r, "", "  ")
	if len(b)+1 > MaxRetentionReviewBytes {
		return nil, errors.New("retention review exceeds its bounded file size")
	}
	return append(b, '\n'), e
}
func DecodeRetentionReview(raw []byte) (RetentionReview, error) {
	var r RetentionReview
	if len(raw) > MaxRetentionReviewBytes {
		return r, errors.New("retention review too large")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return r, errors.New("invalid retention review")
	}
	if e := validateRetentionReview(r); e != nil {
		return r, e
	}
	canonical, _ := EncodeRetentionReview(r)
	if string(raw) != string(canonical) {
		return r, errors.New("retention review must retain canonical encoding")
	}
	return r, nil
}
func validateRetentionReview(r RetentionReview) error {
	if r.Version != 1 || !cleanAbs(r.Root) || !validHash(r.CurrentPlanChecksum) || r.CurrentTarget != filepath.Join("versions", r.CurrentPlanChecksum) || !validHash(r.StateChecksum) || r.StateSequence >= 1000000 || r.Checksum != retentionHash(r) || !validHash(r.Checksum) || r.ReplacementChecksum != "" && !validHash(r.ReplacementChecksum) || r.PreviousRetentionChecksum != "" && !validHash(r.PreviousRetentionChecksum) || r.ResumeOf != "" && !validHash(r.ResumeOf) || len(r.Inventory) == 0 || len(r.Inventory) > MaxRetainedVersions || len(r.Candidates) > MaxRetainedVersions {
		return errors.New("retention review binding invalid")
	}
	seen := map[string]bool{}
	count := 0
	for _, tree := range r.Inventory {
		if !validHash(tree.PlanChecksum) || seen[tree.PlanChecksum] || len(tree.Entries) < 1 {
			return errors.New("invalid retention inventory")
		}
		seen[tree.PlanChecksum] = true
		paths := map[string]bool{}
		var bytes int64
		for _, entry := range tree.Entries {
			count++
			if paths[entry.Path] || !validRetentionEntry(entry) {
				return errors.New("invalid retention entry")
			}
			paths[entry.Path] = true
			bytes += entry.Bytes
		}
		if !paths[""] || bytes != tree.Bytes {
			return errors.New("retention inventory incomplete")
		}
	}
	if count > maxRetentionEntries || !validRetentionEntry(r.RootIdentity) || !r.RootIdentity.Directory || r.RootIdentity.Path != "" || !validRetentionEntry(r.VersionsIdentity) || !r.VersionsIdentity.Directory || r.VersionsIdentity.Path != "" {
		return errors.New("retention inventory bound or identity invalid")
	}
	chosen := map[string]bool{}
	for _, source := range r.Candidates {
		if !seen[source.PlanChecksum] || chosen[source.PlanChecksum] || source.PlanChecksum == r.CurrentPlanChecksum || !validHash(source.ActiveChecksum) || !validHash(source.StandbyChecksum) || !validHash(source.DNSChecksum) {
			return errors.New("invalid retention candidate")
		}
		chosen[source.PlanChecksum] = true
	}
	for _, p := range r.Protected {
		if !seen[p.PlanChecksum] || chosen[p.PlanChecksum] || p.Reason == "" {
			return errors.New("retention protection inconsistent")
		}
	}
	operations := retentionOperations(r)
	if r.StartCursor < 0 || r.StartCursor > len(operations) || r.ResumeOf == "" && r.StartCursor != 0 {
		return errors.New("invalid retention resume cursor")
	}
	if r.ResumeOf == "" && (r.PreviousPhase != "" || r.PreviousIntent != nil) || r.ResumeOf != "" && r.PreviousPhase != "running" && r.PreviousPhase != "unknown" {
		return errors.New("invalid retention superseded intent evidence")
	}
	if r.PreviousIntent != nil && (r.StartCursor >= len(operations) || r.PreviousIntent.Index != r.StartCursor || r.PreviousIntent.Path != operations[r.StartCursor].Path) {
		return errors.New("retention prior intent differs from re-reviewed entry")
	}
	return nil
}
func validRetentionEntry(e RetentionEntry) bool {
	if e.Inode == 0 || e.UID != uint32(os.Geteuid()) || e.Directory && (e.Mode != 0700 || e.Bytes != 0 || e.SHA256 != "" || e.Path != "" && e.Path != "dns" && e.Path != "dns/zones") {
		return false
	}
	if e.Directory {
		return true
	}
	return e.Mode == 0600 && e.Bytes >= 0 && e.Bytes <= 40<<20 && validHash(e.SHA256) && (e.Path == "active.conf" || e.Path == "standby.conf" || e.Path == "bundle.json" || e.Path == "dns/pdns.conf" || e.Path == "dns/named.conf" || strings.HasPrefix(e.Path, "dns/") && retainedZone.MatchString(strings.TrimPrefix(e.Path, "dns/")))
}
func retentionOperations(r RetentionReview) []retentionOperation {
	chosen := map[string]bool{}
	for _, s := range r.Candidates {
		chosen[s.PlanChecksum] = true
	}
	var out []retentionOperation
	for _, tree := range r.Inventory {
		if !chosen[tree.PlanChecksum] {
			continue
		}
		entries := append([]RetentionEntry{}, tree.Entries...)
		sort.Slice(entries, func(i, j int) bool {
			a, b := entries[i], entries[j]
			if a.Directory != b.Directory {
				return !a.Directory
			}
			if a.Directory {
				ad, bd := strings.Count(a.Path, "/")+1, strings.Count(b.Path, "/")+1
				if a.Path == "" {
					ad = 0
				}
				if b.Path == "" {
					bd = 0
				}
				if ad != bd {
					return ad > bd
				}
			}
			return a.Path < b.Path
		})
		for _, entry := range entries {
			out = append(out, retentionOperation{tree.PlanChecksum, entry, filepath.Join("versions", tree.PlanChecksum, entry.Path)})
		}
	}
	return out
}
func (r RetentionReview) OperationCount() int { return len(retentionOperations(r)) }
func (r RetentionReview) RemainingPaths() []string {
	ops := retentionOperations(r)
	paths := []string{}
	if r.StartCursor < 0 || r.StartCursor > len(ops) {
		return paths
	}
	for _, op := range ops[r.StartCursor:] {
		paths = append(paths, op.Path)
	}
	return paths
}

func captureRetentionEntry(path, relative string) (RetentionEntry, error) {
	var entry RetentionEntry
	s, e := os.Lstat(path)
	if e != nil {
		return entry, e
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || s.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return entry, errors.New("retention entry owner/type invalid")
	}
	entry = RetentionEntry{Path: relative, Directory: s.IsDir(), Device: uint64(st.Dev), Inode: st.Ino, UID: st.Uid, Mode: uint32(s.Mode().Perm())}
	if entry.Directory {
		if e = privateDir(path); e != nil {
			return entry, e
		}
	} else {
		if !s.Mode().IsRegular() || st.Nlink != 1 || entry.Mode != 0600 {
			return entry, errors.New("retention entry must be a private single-link regular file")
		}
		raw, e := privateRead(path, 40<<20)
		if e != nil {
			return entry, e
		}
		entry.Bytes = int64(len(raw))
		entry.SHA256 = routing.Checksum(string(raw))
		after, e := os.Lstat(path)
		if e != nil || !os.SameFile(s, after) || after.Size() != s.Size() || after.ModTime() != s.ModTime() {
			return entry, errors.New("retention entry changed while reading")
		}
	}
	if !validRetentionEntry(entry) {
		return entry, errors.New("unrecognized retention file layout")
	}
	return entry, nil
}
func captureRetentionTree(root, name string) (RetentionTree, error) {
	tree := RetentionTree{PlanChecksum: name}
	dir := filepath.Join(root, "versions", name)
	e := filepath.WalkDir(dir, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		relative, e := filepath.Rel(dir, path)
		if e != nil {
			return e
		}
		if relative == "." {
			relative = ""
		}
		entry, e := captureRetentionEntry(path, relative)
		if e != nil {
			return e
		}
		tree.Entries = append(tree.Entries, entry)
		tree.Bytes += entry.Bytes
		if len(tree.Entries) > maxRetentionEntries || tree.Bytes > MaxRetainedVersionBytes {
			return errors.New("retention inventory too large")
		}
		return nil
	})
	return tree, e
}
func historySource(p Plan) RetentionSource {
	return RetentionSource{PlanChecksum: p.Checksum, ActiveChecksum: p.ActiveChecksum, StandbyChecksum: p.StandbyChecksum, DNSChecksum: p.DNSChecksum, Tools: p.Tools, HealthIntervalSeconds: p.HealthIntervalSeconds, RecoveryHealthyChecks: p.RecoveryHealthyChecks}
}
func (c *Controller) historicalProof(p Plan) error {
	if p.Root != c.plan.Root || !c.acceptsPlanHost(p.Host) || p.Platform != c.plan.Platform || p.RouterID != c.plan.RouterID || p.RouterCreatedAt != c.plan.RouterCreatedAt {
		return errors.New("historical plan belongs to a different dedicated instance")
	}
	want, e := retentionFiles(p)
	if e != nil {
		return e
	}
	tree, e := captureRetentionTree(p.Root, p.Checksum)
	if e != nil {
		return e
	}
	files := 0
	for _, entry := range tree.Entries {
		if entry.Directory {
			continue
		}
		text, ok := want[entry.Path]
		if !ok || entry.SHA256 != routing.Checksum(text) || entry.Bytes != int64(len(text)) {
			return errors.New("historical version differs from its exact private plan")
		}
		files++
	}
	if files != len(want) {
		return errors.New("historical version is incomplete")
	}
	return nil
}

func retainedFileHash(root, name string) (string, error) {
	raw, e := privateRead(filepath.Join(root, name), MaxRetentionReviewBytes*2)
	if os.IsNotExist(e) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	return routing.Checksum(string(raw)), nil
}
func (c *Controller) retentionOffline(ctx context.Context) error {
	if c.closed {
		return errors.New("controller closed")
	}
	if e := retentionMounts(c.plan.Root); e != nil {
		return e
	}
	var e error
	if e = verifyTarget(c.plan, c.state); e != nil {
		return errors.New("retention target identity changed")
	}
	if !c.state.Installed || c.state.PlanChecksum != c.plan.Checksum || c.state.Pending != nil || c.state.BGP != nil || c.state.DNS != nil {
		return errors.New("retention requires an installed offline instance without pending service operations")
	}
	if e = c.replacementAbsent(ctx); e != nil {
		return e
	}
	if e = c.verifyInstalled(); e != nil {
		return e
	}
	if marker, e := ReadReplacement(c.plan.Root); e == nil {
		if marker.Phase != "complete" || marker.NextPlanChecksum != c.plan.Checksum {
			return errors.New("replacement recovery material is protected")
		}
		if _, e = os.Lstat(filepath.Join(c.plan.Root, "versions", marker.StagingName)); !os.IsNotExist(e) {
			return errors.New("unexpected replacement staging is protected")
		}
		if e = privateDir(filepath.Join(c.plan.Root, marker.PreviousTarget)); e != nil {
			return errors.New("retained replacement baseline is missing or unsafe")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	return nil
}

func (c *Controller) ReviewRetention(ctx context.Context, historical []Plan) (RetentionReview, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var r RetentionReview
	if e := c.retentionOffline(ctx); e != nil {
		return r, e
	}
	if c.state.Phase != "withdrawn" {
		return r, errors.New("unknown or active state cannot authorize retention")
	}
	if len(historical) > MaxRetainedVersions {
		return r, errors.New("too many historical plan proofs")
	}
	if status, e := ReadRetention(c.plan.Root); e == nil && status.Phase != "complete" {
		return r, errors.New("retention is pending; explicitly review remaining work with resume")
	} else if e != nil && !os.IsNotExist(e) {
		return r, e
	}
	r = RetentionReview{Version: 1, Root: c.plan.Root, Host: c.plan.Host, CurrentPlanChecksum: c.plan.Checksum, CurrentTarget: filepath.Join("versions", c.plan.Checksum), StateSequence: c.state.Sequence, ReviewedAt: c.now().UTC(), Candidates: []RetentionSource{}, Protected: []RetentionProtected{}}
	var e error
	if r.StateChecksum, e = retainedFileHash(r.Root, "status.json"); e != nil {
		return r, e
	}
	if r.ReplacementChecksum, e = retainedFileHash(r.Root, "replacement.json"); e != nil {
		return r, e
	}
	if r.PreviousRetentionChecksum, e = retainedFileHash(r.Root, "retention.json"); e != nil {
		return r, e
	}
	if r.RootIdentity, e = captureRetentionEntry(r.Root, ""); e != nil {
		return r, e
	}
	if r.VersionsIdentity, e = captureRetentionEntry(filepath.Join(r.Root, "versions"), ""); e != nil {
		return r, e
	}
	proofs := map[string]Plan{}
	for _, p := range historical {
		if _, ok := proofs[p.Checksum]; ok {
			return r, errors.New("repeated historical plan")
		}
		if e = c.historicalProof(p); e != nil {
			return r, e
		}
		proofs[p.Checksum] = p
	}
	protect := map[string]string{c.plan.Checksum: "current-version"}
	if m, e := ReadReplacement(r.Root); e == nil {
		protect[m.PreviousPlanChecksum] = "latest-replacement-baseline"
		protect[m.NextPlanChecksum] = "current-version"
	}
	entries, e := os.ReadDir(filepath.Join(r.Root, "versions"))
	if e != nil || len(entries) > MaxRetainedVersions {
		return r, errors.New("version inventory unavailable or exceeds ownership bound")
	}
	var bytes int64
	identities := map[[2]uint64]bool{}
	for _, entry := range entries {
		if !entry.IsDir() || !validHash(entry.Name()) {
			return r, errors.New("unknown or incomplete version directory is protected")
		}
		tree, e := captureRetentionTree(r.Root, entry.Name())
		if e != nil {
			return r, e
		}
		for _, f := range tree.Entries {
			identity := [2]uint64{f.Device, f.Inode}
			if f.Device != r.VersionsIdentity.Device || identities[identity] {
				return r, errors.New("version alias or mount boundary is not owned")
			}
			identities[identity] = true
		}
		bytes += tree.Bytes
		if bytes > MaxRetainedVersionBytes {
			return r, errors.New("retention inventory exceeds byte bound")
		}
		r.Inventory = append(r.Inventory, tree)
		if reason := protect[entry.Name()]; reason != "" {
			r.Protected = append(r.Protected, RetentionProtected{entry.Name(), reason})
		} else if p, ok := proofs[entry.Name()]; ok {
			source := historySource(p)
			if p.Host != c.plan.Host {
				h := p.Host
				source.Host = &h
			}
			r.Candidates = append(r.Candidates, source)
		} else {
			r.Protected = append(r.Protected, RetentionProtected{entry.Name(), "historical-plan-not-provided"})
		}
	}
	r.Checksum = retentionHash(r)
	return r, validateRetentionReview(r)
}
