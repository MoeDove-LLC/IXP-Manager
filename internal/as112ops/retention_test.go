package as112ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/routing"
)

func retentionNext(t *testing.T, p Plan, n int) Plan {
	t.Helper()
	b, e := as112.Decode(p.DNSBundle)
	if e != nil {
		t.Fatal(e)
	}
	id := b.Identity
	id.Facility = fmt.Sprintf("retained generation %d", n)
	b, e = as112.RenderNode(b.RouterID, b.RouterCreatedAt, as112.Service{Mode: b.Service, IPv4: true}, id, b.NodeAddresses)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := as112.Encode(b)
	if e != nil {
		t.Fatal(e)
	}
	next, e := BuildPlan(PlanInput{Root: p.Root, Platform: p.Platform, ActiveConfig: p.ActiveConfig, DNSBundle: raw, Tools: p.Tools, Host: p.Host, HealthIntervalSeconds: p.HealthIntervalSeconds, RecoveryHealthyChecks: p.RecoveryHealthyChecks})
	if e != nil {
		t.Fatal(e)
	}
	return next
}
func retentionFixture(t *testing.T, count int) (*Controller, *modelRuntime, []Plan) {
	t.Helper()
	p := testPlan(t, "bird2")
	c, r := testController(t, p)
	if _, e := c.Install(context.Background(), approved(p)); e != nil {
		t.Fatal(e)
	}
	history := []Plan{p}
	for i := 1; i < count; i++ {
		next := retentionNext(t, c.plan, i)
		if _, e := c.Replace(context.Background(), replaceRequest(c.plan, next)); e != nil {
			t.Fatal(e)
		}
		history = append(history, next)
	}
	return c, r, history
}
func cleanupRequest(r RetentionReview) RetentionRequest {
	return RetentionRequest{Review: r, ReviewChecksum: r.Checksum, ExclusiveInstanceAcknowledged: true, DeleteAcknowledged: true}
}
func requireRetentionReview(t *testing.T, c *Controller, history []Plan) RetentionReview {
	t.Helper()
	r, e := c.ReviewRetention(context.Background(), history)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := EncodeRetentionReview(r)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), "SyntheticKey") || strings.Contains(string(raw), "activeConfig") || strings.Contains(string(raw), "DNSBundle") {
		t.Fatal("review leaked configuration")
	}
	if _, e = DecodeRetentionReview(raw); e != nil {
		t.Fatal(e)
	}
	return r
}

func TestRetentionReleasesCapacityAndPreservesBaseline(t *testing.T) {
	ctx := context.Background()
	c, r, history := retentionFixture(t, MaxRetainedVersions)
	next := retentionNext(t, c.plan, 99)
	if _, e := c.Replace(ctx, replaceRequest(c.plan, next)); e == nil {
		t.Fatal("capacity fixture did not reach limit")
	}
	before, _ := os.ReadFile(filepath.Join(c.plan.Root, "status.json"))
	review := requireRetentionReview(t, c, history)
	if len(review.Candidates) != 6 || len(review.Protected) != 2 {
		t.Fatal("wrong protected set", review.Protected)
	}
	after, _ := os.ReadFile(filepath.Join(c.plan.Root, "status.json"))
	if string(before) != string(after) {
		t.Fatal("review changed status")
	}
	bad := cleanupRequest(review)
	bad.DeleteAcknowledged = false
	if _, e := c.ApplyRetention(ctx, bad); e == nil {
		t.Fatal("missing delete acknowledgment")
	}
	if _, e := os.Stat(filepath.Join(c.plan.Root, "versions", history[0].Checksum)); e != nil {
		t.Fatal("unapproved cleanup deleted data")
	}
	result, e := c.ApplyRetention(ctx, cleanupRequest(review))
	if e != nil || result.Phase != "complete" || result.Cursor != review.OperationCount() {
		t.Fatal(result.Phase, result.Cursor, e)
	}
	entries, e := os.ReadDir(filepath.Join(c.plan.Root, "versions"))
	if e != nil || len(entries) != 2 {
		t.Fatal("retention did not free capacity", len(entries), e)
	}
	for _, p := range history[len(history)-2:] {
		if e = verifyPlanVersion(p, filepath.Join(p.Root, "versions", p.Checksum)); e != nil {
			t.Fatal("protected current/baseline changed", e)
		}
	}
	if _, e = c.Replace(ctx, replaceRequest(c.plan, next)); e != nil {
		t.Fatal("safe cleanup did not allow another upgrade", e)
	}
	requireNoProcessWrites(t, r)
}
func TestRetentionMissingProofIsKeptAndNoCheckerRuns(t *testing.T) {
	c, r, history := retentionFixture(t, 4)
	before := len(r.calls)
	review := requireRetentionReview(t, c, []Plan{history[0]})
	if len(review.Candidates) != 1 {
		t.Fatal(review.Candidates)
	}
	found := false
	for _, p := range review.Protected {
		found = found || p.PlanChecksum == history[1].Checksum && p.Reason == "historical-plan-not-provided"
	}
	if !found {
		t.Fatal("unproven history not reported")
	}
	if _, e := c.ApplyRetention(context.Background(), cleanupRequest(review)); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(c.plan.Root, "versions", history[1].Checksum)); e != nil {
		t.Fatal("unproven version deleted")
	}
	if len(r.calls) != before {
		t.Fatal("retention executed a checker or service")
	}
}

func TestRetentionHistoricalDecoderDoesNotGrantActivation(t *testing.T) {
	c, _, history := retentionFixture(t, 3)
	old := history[0]
	old.ActiveConfig = "# Historical profile from an older checker\nprivate-key SyntheticOldKey\n"
	old.ActiveChecksum = routing.Checksum(old.ActiveConfig)
	old.Tools.Router = BinaryPin{Path: "/retired/version/bird", SHA256: strings.Repeat("b", 64)}
	old.Checksum = digest(old)
	oldDir := filepath.Join(old.Root, "versions", history[0].Checksum)
	if e := os.WriteFile(filepath.Join(oldDir, "active.conf"), []byte(old.ActiveConfig), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(oldDir, filepath.Join(old.Root, "versions", old.Checksum)); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.MarshalIndent(old, "", "  ")
	raw = append(raw, '\n')
	if _, e := DecodePlan(raw); e == nil {
		t.Fatal("historical cleanup decoder allowed new activation")
	}
	if _, e := DecodeRetentionHistory(raw); e != nil {
		t.Fatal("old checker absence blocked ownership proof", e)
	}
	review := requireRetentionReview(t, c, []Plan{old})
	if _, e := c.ApplyRetention(context.Background(), cleanupRequest(review)); e != nil {
		t.Fatal(e)
	}
}

func TestRetentionRefusesUnsafeStatesAndObjects(t *testing.T) {
	for _, kind := range []string{"unknown", "foreign", "replacement-pending", "missing-baseline", "symlink", "hardlink", "permissions", "special-permissions", "fifo", "current-fifo", "extra-directory", "current-pointer"} {
		t.Run(kind, func(t *testing.T) {
			c, r, history := retentionFixture(t, 3)
			target := filepath.Join(c.plan.Root, "versions", history[0].Checksum, "active.conf")
			switch kind {
			case "unknown":
				c.state.Phase = "unknown"
			case "foreign":
				r.processes = []Process{{PID: 888, Kind: "foreign"}}
			case "replacement-pending":
				m, e := ReadReplacement(c.plan.Root)
				if e != nil {
					t.Fatal(e)
				}
				m.Phase = "pointer-intent"
				if e = writeReplacement(c.plan.Root, m); e != nil {
					t.Fatal(e)
				}
			case "missing-baseline":
				if e := os.Rename(filepath.Join(c.plan.Root, "versions", history[1].Checksum), filepath.Join(c.plan.Root, "missing-baseline")); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				_ = os.Remove(target)
				if e := os.Symlink(filepath.Join(c.plan.Root, "versions", c.plan.Checksum, "active.conf"), target); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				if e := os.Link(target, filepath.Join(c.plan.Root, "extra-link")); e != nil {
					t.Fatal(e)
				}
			case "permissions":
				if e := os.Chmod(target, 0644); e != nil {
					t.Fatal(e)
				}
			case "special-permissions":
				if e := os.Chmod(target, 0600|os.ModeSetuid); e != nil {
					t.Fatal(e)
				}
			case "current-fifo":
				current := filepath.Join(c.versionDir(), "active.conf")
				_ = os.Remove(current)
				if e := syscall.Mkfifo(current, 0600); e != nil {
					t.Fatal(e)
				}
			case "fifo":
				_ = os.Remove(target)
				if e := syscall.Mkfifo(target, 0600); e != nil {
					t.Fatal(e)
				}
			case "extra-directory":
				if e := os.Mkdir(filepath.Join(filepath.Dir(target), "unowned"), 0700); e != nil {
					t.Fatal(e)
				}
			case "current-pointer":
				_ = os.Remove(filepath.Join(c.plan.Root, "current"))
				if e := os.Symlink(filepath.Join("versions", history[0].Checksum), filepath.Join(c.plan.Root, "current")); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := c.ReviewRetention(context.Background(), history); e == nil {
				t.Fatal("unsafe cleanup review accepted")
			}
			if _, e := ReadRetention(c.plan.Root); !os.IsNotExist(e) {
				t.Fatal("review wrote retention journal", e)
			}
			requireNoProcessWrites(t, r)
		})
	}
}

func TestRetentionApplyRechecksReviewAndProtectedSet(t *testing.T) {
	for _, kind := range []string{"file-changed", "inode-replaced", "status-changed", "marker-changed", "candidate-current", "candidate-baseline", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			c, r, history := retentionFixture(t, 3)
			review := requireRetentionReview(t, c, history)
			target := filepath.Join(c.plan.Root, "versions", history[0].Checksum, "active.conf")
			switch kind {
			case "file-changed":
				if e := os.WriteFile(target, []byte("not reviewed"), 0600); e != nil {
					t.Fatal(e)
				}
			case "inode-replaced":
				raw, e := os.ReadFile(target)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(target, target+".old"); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(target, raw, 0600); e != nil {
					t.Fatal(e)
				}
				_ = os.Remove(target + ".old")
			case "status-changed":
				c.state.Sequence++
				if e := c.persist(); e != nil {
					t.Fatal(e)
				}
			case "marker-changed":
				m, _ := ReadReplacement(c.plan.Root)
				m.UpdatedAt = m.UpdatedAt.Add(1)
				if e := writeReplacement(c.plan.Root, m); e != nil {
					t.Fatal(e)
				}
			case "candidate-current":
				review.Candidates = append(review.Candidates, historySource(c.plan))
				review.Checksum = retentionHash(review)
			case "candidate-baseline":
				review.Candidates = append(review.Candidates, historySource(history[1]))
				review.Protected = []RetentionProtected{{PlanChecksum: c.plan.Checksum, Reason: "current-version"}}
				review.Checksum = retentionHash(review)
			case "foreign":
				r.processes = []Process{{PID: 999, Kind: "foreign"}}
			}
			if _, e := c.ApplyRetention(context.Background(), cleanupRequest(review)); e == nil {
				t.Fatal("changed or protected target pruned")
			}
			if _, e := os.Lstat(target); e != nil {
				t.Fatal("failed apply removed target")
			}
			requireNoProcessWrites(t, r)
		})
	}
}

func seedRetentionIntent(t *testing.T, c *Controller, review RetentionReview, deleted bool) RetentionStatus {
	t.Helper()
	ops := retentionOperations(review)
	s := RetentionStatus{Version: 1, Review: review, Phase: "unknown", Cursor: 0, Pending: &RetentionIntent{Index: 0, Path: ops[0].Path, StartedAt: c.now().UTC()}}
	if e := c.writeRetention(&s); e != nil {
		t.Fatal(e)
	}
	if deleted {
		if e := removeRetentionEntry(review, ops[0]); e != nil {
			t.Fatal(e)
		}
	}
	c.state.Phase = "unknown"
	c.state.Reason = "retention: synthetic interrupted unlink"
	if e := c.persist(); e != nil {
		t.Fatal(e)
	}
	return s
}

func TestRetentionMissingResultReconcilesWithoutReplay(t *testing.T) {
	ctx := context.Background()
	c, r, history := retentionFixture(t, 3)
	review := requireRetentionReview(t, c, history)
	seedRetentionIntent(t, c, review, true)
	old, _ := os.ReadFile(filepath.Join(c.plan.Root, "retention.json"))
	c.closed = true
	_ = c.lock.Close()
	freshRuntime := &modelRuntime{t: t, plan: c.plan}
	fresh, e := Open(c.plan, freshRuntime)
	if e != nil {
		t.Fatal(e)
	}
	defer fresh.Close()
	now, _ := os.ReadFile(filepath.Join(c.plan.Root, "retention.json"))
	if string(old) != string(now) {
		t.Fatal("Open replayed cleanup")
	}
	if _, e = fresh.Reconcile(ctx); e == nil {
		t.Fatal("ordinary reconciliation bypassed pending retention")
	}
	if _, e = fresh.ApplyRetention(ctx, cleanupRequest(review)); e == nil {
		t.Fatal("unknown result replayed")
	}
	s, e := fresh.ReconcileRetention(ctx, cleanupRequest(review))
	if e != nil || s.Cursor != 1 || s.Pending != nil {
		t.Fatal(s.Cursor, e)
	}
	if _, e = fresh.ApplyRetention(ctx, cleanupRequest(review)); e != nil {
		t.Fatal("known remaining entries cannot continue", e)
	}
	requireNoProcessWrites(t, r)
	requireNoProcessWrites(t, freshRuntime)
}

func TestRetentionExistingUnknownNeedsNewReviewedAuthority(t *testing.T) {
	ctx := context.Background()
	c, r, history := retentionFixture(t, 3)
	review := requireRetentionReview(t, c, history)
	old := seedRetentionIntent(t, c, review, false)
	target := filepath.Join(c.plan.Root, old.Pending.Path)
	if _, e := c.ReconcileRetention(ctx, cleanupRequest(review)); e == nil {
		t.Fatal("existing uncertain target treated as absent")
	}
	if _, e := c.ApplyRetention(ctx, cleanupRequest(review)); e == nil {
		t.Fatal("uncertain unlink replayed without fresh approval")
	}
	renewed, e := c.ReviewRetentionResume(ctx)
	if e != nil || renewed.Checksum == review.Checksum || renewed.ResumeOf != review.Checksum || renewed.PreviousIntent == nil {
		t.Fatal(renewed.ResumeOf, e)
	}
	if _, e = os.Stat(target); e != nil {
		t.Fatal("resume review deleted data")
	}
	bad := cleanupRequest(renewed)
	bad.ReviewChecksum = review.Checksum
	if _, e = c.ApplyRetention(ctx, bad); e == nil {
		t.Fatal("old checksum authorized new attempt")
	}
	if _, e = c.ApplyRetention(ctx, cleanupRequest(renewed)); e != nil {
		t.Fatal("fresh exact authorization could not finish cleanup", e)
	}
	if _, e = os.Stat(target); !os.IsNotExist(e) {
		t.Fatal("newly approved cleanup did not remove entry")
	}
	requireNoProcessWrites(t, r)
}

func TestRetentionResumeCannotExpandOrIgnoreChangedEntries(t *testing.T) {
	for _, kind := range []string{"changed-entry", "expanded-scope"} {
		t.Run(kind, func(t *testing.T) {
			c, _, history := retentionFixture(t, 4)
			review := requireRetentionReview(t, c, []Plan{history[0]})
			seedRetentionIntent(t, c, review, false)
			if kind == "changed-entry" {
				op := retentionOperations(review)[0]
				if e := os.WriteFile(filepath.Join(c.plan.Root, op.Path), []byte("changed after uncertain unlink"), 0600); e != nil {
					t.Fatal(e)
				}
				if _, e := c.ReviewRetentionResume(context.Background()); e == nil {
					t.Fatal("changed unknown object reauthorized")
				}
			} else {
				next, e := c.ReviewRetentionResume(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				next.Candidates = append(next.Candidates, historySource(history[1]))
				for i, p := range next.Protected {
					if p.PlanChecksum == history[1].Checksum {
						next.Protected = append(next.Protected[:i], next.Protected[i+1:]...)
						break
					}
				}
				next.Checksum = retentionHash(next)
				if _, e = c.ApplyRetention(context.Background(), cleanupRequest(next)); e == nil {
					t.Fatal("resume expanded original ownership")
				}
			}
		})
	}
}
func TestRetentionRejectsNestedMountAliases(t *testing.T) {
	root := "/private/as112 instance"
	for _, point := range []string{"/private/as112\\040instance/versions", "/private/as112\\040instance/versions/abcdef/dns"} {
		if e := checkRetentionMounts(root, "7 1 0:1 / "+point+" rw - tmpfs tmpfs rw\n"); e == nil {
			t.Fatal("same-device bind mount accepted")
		}
	}
	if e := checkRetentionMounts(root, "7 1 0:1 / /private/as112\\040instance rw - tmpfs tmpfs rw\n8 1 0:1 / /unrelated/versions rw - tmpfs tmpfs rw\n"); e != nil {
		t.Fatal("instance filesystem itself or unrelated mount rejected", e)
	}
}
