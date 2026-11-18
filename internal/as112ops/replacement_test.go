package as112ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/as112"
)

func nextPlan(t *testing.T, p Plan) Plan {
	t.Helper()
	b, e := as112.Decode(p.DNSBundle)
	if e != nil {
		t.Fatal(e)
	}
	id := b.Identity
	id.Facility = "new explicitly reviewed facility"
	service := as112.Service{Mode: b.Service}
	for _, f := range b.Families {
		if f == 4 {
			service.IPv4 = true
		} else {
			service.IPv6 = true
		}
	}
	b, e = as112.RenderNode(b.RouterID, b.RouterCreatedAt, service, id, b.NodeAddresses)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := as112.Encode(b)
	if e != nil {
		t.Fatal(e)
	}
	next, e := BuildPlan(PlanInput{Root: p.Root, Platform: p.Platform, ActiveConfig: strings.ReplaceAll(p.ActiveConfig, "SyntheticKey", "ReplacementKey"), DNSBundle: raw, Tools: p.Tools, Host: p.Host, HealthIntervalSeconds: p.HealthIntervalSeconds, RecoveryHealthyChecks: p.RecoveryHealthyChecks})
	if e != nil {
		t.Fatal(e)
	}
	return next
}
func replaceRequest(p, n Plan) ReplacementRequest {
	return ReplacementRequest{Previous: p, Next: n, PreviousPlanChecksum: p.Checksum, CurrentTarget: filepath.Join("versions", p.Checksum), Approval: approved(n)}
}
func installedReplacement(t *testing.T) (*Controller, *modelRuntime, ReplacementRequest) {
	t.Helper()
	p := testPlan(t, "bird2")
	n := nextPlan(t, p)
	c, r := testController(t, p)
	if _, e := c.Install(context.Background(), approved(p)); e != nil {
		t.Fatal(e)
	}
	return c, r, replaceRequest(p, n)
}
func requireNoProcessWrites(t *testing.T, r *modelRuntime) {
	t.Helper()
	for _, call := range r.calls {
		if call != "validate" {
			t.Fatalf("offline replacement performed process operation %s", call)
		}
	}
}

func TestReplacementApprovedOfflineRoundTrip(t *testing.T) {
	ctx := context.Background()
	c, r, q := installedReplacement(t)
	bad := q
	bad.Approval.ExclusiveInstanceAcknowledged = false
	if _, e := c.Replace(ctx, bad); e == nil {
		t.Fatal("implicit approval accepted")
	}
	if s, e := c.Replace(ctx, q); e != nil || s.PlanChecksum != q.Next.Checksum || s.Phase != "withdrawn" || s.DNS != nil || s.BGP != nil || s.LastHealthy || s.HealthyChecks != 0 {
		t.Fatal(s, e)
	}
	requireNoProcessWrites(t, r)
	if e := verifyPlanVersion(q.Previous, filepath.Join(q.Previous.Root, q.CurrentTarget)); e != nil {
		t.Fatal("old version not retained", e)
	}
	if e := verifyPlanVersion(q.Next, filepath.Join(q.Next.Root, "versions", q.Next.Checksum)); e != nil {
		t.Fatal(e)
	}
	marker, e := ReadReplacement(q.Next.Root)
	if e != nil || marker.Phase != "complete" || marker.NextPlanChecksum != q.Next.Checksum {
		t.Fatal(marker, e)
	}
	if e = c.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(q.Previous, &modelRuntime{t: t, plan: q.Previous}); e == nil {
		t.Fatal("old plan reopened replaced instance")
	}
	fresh := &modelRuntime{t: t, plan: q.Next, healthy: true}
	current, e := Open(q.Next, fresh)
	if e != nil {
		t.Fatal(e)
	}
	defer current.Close()
	if s, e := current.Tick(ctx, false); e != nil || s.Phase == "active" {
		t.Fatal("replacement automatically resumed active", s, e)
	}
}

func TestReplacementRejectsUnsafeBase(t *testing.T) {
	for _, kind := range []string{"wrong-pointer", "wrong-previous-checksum", "new-root", "new-host", "new-platform", "active", "pending", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			c, r, q := installedReplacement(t)
			switch kind {
			case "wrong-pointer":
				q.CurrentTarget = "versions/" + strings.Repeat("a", 64)
			case "wrong-previous-checksum":
				q.PreviousPlanChecksum = strings.Repeat("a", 64)
			case "new-root":
				q.Next.Root = filepath.Join(q.Next.Root, "different")
				q.Next.Checksum = digest(q.Next)
				q.Approval = approved(q.Next)
			case "new-host":
				q.Next.Host.BootID = "00000000-0000-0000-0000-000000000000"
				q.Next.Checksum = digest(q.Next)
				q.Approval = approved(q.Next)
			case "new-platform":
				q.Next.Platform = "frr"
				q.Next.Checksum = digest(q.Next)
				q.Approval = approved(q.Next)
			case "active":
				c.state.Phase = "active"
			case "pending":
				c.state.Pending = &Intent{Sequence: 123, Kind: "start-active"}
			case "foreign":
				r.processes = []Process{{PID: 400, Kind: "foreign"}}
			}
			if _, e := c.Replace(context.Background(), q); e == nil {
				t.Fatal("unsafe replacement accepted")
			}
			if e := exactCurrent(c.plan.Root, filepath.Join("versions", q.Previous.Checksum)); e != nil {
				t.Fatal(e)
			}
			if _, e := ReadReplacement(c.plan.Root); !os.IsNotExist(e) {
				t.Fatal("rejected base created journal", e)
			}
			requireNoProcessWrites(t, r)
		})
	}
}

func seedReplacement(t *testing.T, c *Controller, q ReplacementRequest, scenario string) ReplacementStatus {
	t.Helper()
	dir, e := c.stageReplacement(context.Background(), q.Next)
	if e != nil {
		t.Fatal(e)
	}
	m := ReplacementStatus{Version: 1, Sequence: c.state.Sequence + 1, BindingChecksum: replacementBinding(q.Previous, q.Next), PreviousPlanChecksum: q.Previous.Checksum, NextPlanChecksum: q.Next.Checksum, PreviousTarget: q.CurrentTarget, NextTarget: filepath.Join("versions", q.Next.Checksum), StagingName: filepath.Base(dir), Phase: "version-intent", UpdatedAt: c.now().UTC()}
	if scenario != "staged" {
		if e = os.Rename(dir, filepath.Join(c.plan.Root, m.NextTarget)); e != nil {
			t.Fatal(e)
		}
	}
	switch scenario {
	case "ready":
		m.Phase = "version-ready"
	case "pointer-old", "pointer-new", "status-new":
		m.Phase = "pointer-intent"
	}
	if scenario == "pointer-new" || scenario == "status-new" {
		link := filepath.Join(c.plan.Root, "test-new-current")
		if e = os.Symlink(m.NextTarget, link); e != nil {
			t.Fatal(e)
		}
		if e = os.Rename(link, filepath.Join(c.plan.Root, "current")); e != nil {
			t.Fatal(e)
		}
	}
	if scenario == "status-new" {
		c.plan = q.Next
		c.state.PlanChecksum = q.Next.Checksum
		c.state.Sequence = m.Sequence
		if e = c.persist(); e != nil {
			t.Fatal(e)
		}
	}
	if e = writeReplacement(c.plan.Root, m); e != nil {
		t.Fatal(e)
	}
	return m
}

func TestReplacementCrashReconcileIsExplicit(t *testing.T) {
	for _, scenario := range []string{"staged", "version-published", "ready", "pointer-old", "pointer-new", "status-new"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			c, r, q := installedReplacement(t)
			seedReplacement(t, c, q, scenario)
			beforeStatus, e := os.ReadFile(filepath.Join(c.plan.Root, "status.json"))
			if e != nil {
				t.Fatal(e)
			}
			beforeMarker, e := os.ReadFile(filepath.Join(c.plan.Root, "replacement.json"))
			if e != nil {
				t.Fatal(e)
			}
			c.closed = true
			_ = c.lock.Close()
			fresh := &modelRuntime{t: t, plan: q.Previous}
			reopened, e := OpenForReplacement(q.Previous, q.Next, fresh)
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			afterStatus, _ := os.ReadFile(filepath.Join(c.plan.Root, "status.json"))
			afterMarker, _ := os.ReadFile(filepath.Join(c.plan.Root, "replacement.json"))
			if string(afterStatus) != string(beforeStatus) || string(afterMarker) != string(beforeMarker) {
				t.Fatal("Open completed a transaction")
			}
			if _, e = reopened.Tick(ctx, true); e == nil {
				t.Fatal("pending replacement allowed process start")
			}
			if _, e = reopened.Reconcile(ctx); e == nil {
				t.Fatal("ordinary reconciliation resolved replacement")
			}
			if s, e := reopened.ReconcileReplacement(ctx, q); e != nil || s.PlanChecksum != q.Next.Checksum || s.Phase != "withdrawn" || s.Pending != nil || s.DNS != nil || s.BGP != nil {
				t.Fatal(s, e)
			}
			marker, e := ReadReplacement(q.Next.Root)
			if e != nil || marker.Phase != "complete" {
				t.Fatal(marker, e)
			}
			requireNoProcessWrites(t, r)
			requireNoProcessWrites(t, fresh)
		})
	}
}

func TestReplacementRecoveryRejectsAmbiguityAndDrift(t *testing.T) {
	for _, kind := range []string{"pointer-third", "pointer-without-intent", "both-stage-and-version", "new-bytes", "old-bytes", "foreign", "validation"} {
		t.Run(kind, func(t *testing.T) {
			c, r, q := installedReplacement(t)
			scenario := "staged"
			if kind == "pointer-without-intent" {
				scenario = "ready"
			}
			m := seedReplacement(t, c, q, scenario)
			switch kind {
			case "pointer-third", "pointer-without-intent":
				target := "versions/" + strings.Repeat("a", 64)
				if kind == "pointer-without-intent" {
					target = m.NextTarget
				}
				_ = os.Remove(filepath.Join(c.plan.Root, "current"))
				if e := os.Symlink(target, filepath.Join(c.plan.Root, "current")); e != nil {
					t.Fatal(e)
				}
			case "both-stage-and-version":
				if e := os.Mkdir(filepath.Join(c.plan.Root, m.NextTarget), 0700); e != nil {
					t.Fatal(e)
				}
			case "new-bytes":
				if e := os.WriteFile(filepath.Join(c.plan.Root, "versions", m.StagingName, "active.conf"), []byte("unexpected"), 0600); e != nil {
					t.Fatal(e)
				}
			case "old-bytes":
				if e := os.WriteFile(filepath.Join(c.plan.Root, m.PreviousTarget, "active.conf"), []byte("unexpected"), 0600); e != nil {
					t.Fatal(e)
				}
			case "foreign":
				r.processes = []Process{{PID: 400, Kind: "foreign"}}
			case "validation":
				r.validateFail = true
			}
			before, _ := os.ReadFile(filepath.Join(c.plan.Root, "replacement.json"))
			if _, e := c.ReconcileReplacement(context.Background(), q); e == nil {
				t.Fatal("ambiguous recovery accepted")
			}
			after, _ := os.ReadFile(filepath.Join(c.plan.Root, "replacement.json"))
			if string(before) != string(after) {
				t.Fatal("unproven recovery advanced journal")
			}
			requireNoProcessWrites(t, r)
		})
	}
}

func TestReplacementRetentionBounds(t *testing.T) {
	for _, kind := range []string{"count", "bytes", "unknown-staging"} {
		t.Run(kind, func(t *testing.T) {
			c, r, q := installedReplacement(t)
			versions := filepath.Join(c.plan.Root, "versions")
			switch kind {
			case "count":
				for i := 0; i < MaxRetainedVersions-1; i++ {
					if e := os.Mkdir(filepath.Join(versions, strings.Repeat(string(rune('0'+i)), 64)), 0700); e != nil {
						t.Fatal(e)
					}
				}
			case "bytes":
				dir := filepath.Join(versions, strings.Repeat("a", 64))
				if e := os.Mkdir(dir, 0700); e != nil {
					t.Fatal(e)
				}
				for _, name := range []string{"one", "two"} {
					f, e := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if e != nil {
						t.Fatal(e)
					}
					if e = f.Truncate(MaxRetainedVersionBytes / 2); e != nil {
						t.Fatal(e)
					}
					_ = f.Close()
				}
			case "unknown-staging":
				if e := os.Mkdir(filepath.Join(versions, ".replacement-unknown"), 0700); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := c.Replace(context.Background(), q); e == nil {
				t.Fatal("retention bound ignored")
			}
			requireNoProcessWrites(t, r)
		})
	}
}

func TestReplacementUncertainIntentPreservesStaging(t *testing.T) {
	c, r, q := installedReplacement(t)
	r.afterValidate = func() {
		if e := os.Mkdir(filepath.Join(c.plan.Root, "replacement.json"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	if s, e := c.Replace(context.Background(), q); e == nil || s.Phase != "unknown" {
		t.Fatal(s, e)
	}
	entries, e := os.ReadDir(filepath.Join(c.plan.Root, "versions"))
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".replacement-") {
			found = true
			if e = verifyPlanVersion(q.Next, filepath.Join(c.plan.Root, "versions", entry.Name())); e != nil {
				t.Fatal("uncertain intent lost staged bytes", e)
			}
		}
	}
	if !found {
		t.Fatal("uncertain intent deleted recoverable staging")
	}
	if e = exactCurrent(c.plan.Root, q.CurrentTarget); e != nil {
		t.Fatal(e)
	}
	requireNoProcessWrites(t, r)
}
