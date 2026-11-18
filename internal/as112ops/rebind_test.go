package as112ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routing"
)

func saveTestStatus(t *testing.T, root string, s Status) {
	t.Helper()
	raw, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = writeAtomic(root, "status.json", append(raw, '\n')); e != nil {
		t.Fatal(e)
	}
}

// Rewrite only the synthetic fixture's past target/checksum records. No host,
// namespace, process or native daemon is changed to model a completed reboot.
func previousBootFixture(t *testing.T, count int) (Plan, []Plan, Status) {
	t.Helper()
	c, _, history := retentionFixture(t, count)
	if e := c.Close(); e != nil {
		t.Fatal(e)
	}
	state, e := ReadStatus(c.plan.Root)
	if e != nil {
		t.Fatal(e)
	}
	previous := TargetPin{BootID: "00000000-0000-0000-0000-000000000001", NetworkNamespace: "41:112"}
	old := append([]Plan{}, history...)
	for i, p := range old {
		p.Host = previous
		p.Checksum = digest(p)
		if e = os.Rename(filepath.Join(p.Root, "versions", old[i].Checksum), filepath.Join(p.Root, "versions", p.Checksum)); e != nil {
			t.Fatal(e)
		}
		history[i] = p
	}
	p := history[len(history)-1]
	if count > 1 {
		m, e := ReadReplacement(p.Root)
		if e != nil {
			t.Fatal(e)
		}
		m.PreviousPlanChecksum = history[len(history)-2].Checksum
		m.NextPlanChecksum = p.Checksum
		m.PreviousTarget = filepath.Join("versions", m.PreviousPlanChecksum)
		m.NextTarget = filepath.Join("versions", p.Checksum)
		m.BindingChecksum = replacementBinding(history[len(history)-2], p)
		if e = writeReplacement(p.Root, m); e != nil {
			t.Fatal(e)
		}
	}
	if e = os.Remove(filepath.Join(p.Root, "current")); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join("versions", p.Checksum), filepath.Join(p.Root, "current")); e != nil {
		t.Fatal(e)
	}
	state.PlanChecksum = p.Checksum
	state.Binding.Host = previous
	state.Binding.PlanHosts = []TargetPin{previous}
	state.Phase = "active"
	state.HealthyChecks = 2
	state.LastHealthy = true
	state.DNS = &Process{PID: 112, Kind: "dns", StartTime: "old-boot"}
	state.BGP = &Process{PID: 113, Kind: "bgp", Mode: "active", StartTime: "old-boot"}
	saveTestStatus(t, p.Root, state)
	return p, history, state
}
func rebindSession(t *testing.T, p Plan) (*RebindSession, *modelRuntime) {
	t.Helper()
	runtime := &modelRuntime{t: t, plan: p}
	s, e := OpenForRebind(p, runtime)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, runtime
}
func rebindApproval(r RebindReview) RebindRequest {
	return RebindRequest{Review: r, ReviewChecksum: r.Checksum, ExclusiveInstanceAcknowledged: true, SourceIsolatedAcknowledged: r.Mode == "move"}
}
func reviewReboot(t *testing.T, s *RebindSession) RebindReview {
	t.Helper()
	state, e := ReadStatus(s.plan.Root)
	if e != nil {
		t.Fatal(e)
	}
	mode := "same-host-reboot"
	if state.Binding == nil || state.Binding.HostIdentity == "" {
		mode = "move"
	}
	if os.Getenv("AS112_REQUIRE_STABLE_IDENTITY") == "1" && mode != "same-host-reboot" {
		t.Fatal("synthetic stable-host identity fixture is unavailable")
	}
	t.Logf("review mode: %s", mode)
	r, e := s.Review(context.Background(), mode)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := EncodeRebindReview(r)
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := DecodeRebindReview(raw)
	if e != nil || !reflect.DeepEqual(decoded, r) {
		t.Fatal("review round trip", e)
	}
	return r
}
func TestRebindSameHostConfirmedActiveAndExactCAS(t *testing.T) {
	p, _, before := previousBootFixture(t, 1)
	if c, e := Open(p, &modelRuntime{t: t, plan: p}); e == nil {
		c.Close()
		t.Fatal("old boot silently accepted")
	}
	s, runtime := rebindSession(t, p)
	rawBefore, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
	review := reviewReboot(t, s)
	rawAfter, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
	if string(rawBefore) != string(rawAfter) {
		t.Fatal("review changed status")
	}
	if strings.Contains(string(mustRebindBytes(t, review)), "SyntheticKey") {
		t.Fatal("review leaked config")
	}
	state, result, e := s.Confirm(context.Background(), rebindApproval(review))
	if e != nil || result != "unapplied" || state.Phase != "active" {
		t.Fatal(result, e)
	}
	state, e = s.Apply(context.Background(), rebindApproval(review))
	if e != nil {
		t.Fatal(e)
	}
	if state.Phase != "withdrawn" || state.DNS != nil || state.BGP != nil || state.HealthyChecks != 0 || state.LastHealthy || state.PlanChecksum != p.Checksum || state.Binding.InstanceID != before.Binding.InstanceID || state.Binding.ReviewChecksum != review.Checksum {
		t.Fatal("bad rebound state", state)
	}
	committed, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
	if _, e = s.Apply(context.Background(), rebindApproval(review)); e != nil {
		t.Fatal(e)
	}
	afterRetry, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
	if string(afterRetry) != string(committed) {
		t.Fatal("applied review was rewritten")
	}
	if _, result, e = s.Confirm(context.Background(), rebindApproval(review)); e != nil || result != "applied" {
		t.Fatal(result, e)
	}
	if len(runtime.calls) != 0 {
		t.Fatal("rebind ran service or validator", runtime.calls)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	c, r := testController(t, p)
	r.healthy = true
	if state, e = c.Tick(context.Background(), false); e != nil || state.Phase == "active" {
		t.Fatal("rebind granted activation", e)
	}
	if state, e = c.Tick(context.Background(), true); e != nil || state.Phase != "active" {
		t.Fatal("explicit fresh recovery failed", e)
	}
}
func mustRebindBytes(t *testing.T, r RebindReview) []byte {
	t.Helper()
	raw, e := EncodeRebindReview(r)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestRebindLegacyAndIdentityModes(t *testing.T) {
	for _, name := range []string{"legacy", "missing-identity", "different-host", "same-boot"} {
		t.Run(name, func(t *testing.T) {
			p, _, state := previousBootFixture(t, 1)
			switch name {
			case "legacy":
				state.Binding = nil
			case "missing-identity":
				state.Binding.HostIdentity = ""
			case "different-host":
				state.Binding.HostIdentity = strings.Repeat("a", 64)
			case "same-boot":
				state.Binding.Host, _ = CaptureTarget()
			}
			saveTestStatus(t, p.Root, state)
			s, _ := rebindSession(t, p)
			if _, e := s.Review(context.Background(), "same-host-reboot"); e == nil {
				t.Fatal("unproven reboot accepted")
			}
			review, e := s.Review(context.Background(), "move")
			if e != nil {
				t.Fatal(e)
			}
			q := rebindApproval(review)
			q.SourceIsolatedAcknowledged = false
			if _, e = s.Apply(context.Background(), q); e == nil {
				t.Fatal("move without source isolation")
			}
			q.SourceIsolatedAcknowledged = true
			if _, e = s.Apply(context.Background(), q); e != nil {
				t.Fatal(e)
			}
		})
	}
	machine := "0123456789abcdef0123456789abcdef"
	a := scopeHostIdentity(strings.Repeat("a", 64), machine)
	b := scopeHostIdentity(strings.Repeat("b", 64), machine)
	if a == b || strings.Contains(a, machine) {
		t.Fatal("machine identity was disclosed or not instance scoped")
	}
	for _, bad := range []string{"", strings.Repeat("0", 32), strings.Repeat("g", 32), machine + "x"} {
		if machineIdentity([]byte(bad)) != "" {
			t.Fatal("invalid machine identity accepted")
		}
	}
	if machineIdentity([]byte(machine+"\n")) != machine {
		t.Fatal("valid identity rejected")
	}
}
func TestRebindRefusesUnknownUnsafeOrChangedEvidence(t *testing.T) {
	for _, name := range []string{"pending", "unknown", "foreign", "observe-error", "staging", "pointer", "bytes", "permissions", "retention", "replacement", "status-after-review", "identity-after-review", "target-after-review", "checksum", "approval", "tool-pins", "unknown-status-field"} {
		t.Run(name, func(t *testing.T) {
			p, _, state := previousBootFixture(t, 2)
			s, runtime := rebindSession(t, p)
			review := reviewReboot(t, s)
			q := rebindApproval(review)
			switch name {
			case "pending":
				state.Pending = &Intent{Sequence: state.Sequence + 1, Kind: "start-active"}
				saveTestStatus(t, p.Root, state)
			case "unknown":
				state.Phase = "unknown"
				saveTestStatus(t, p.Root, state)
			case "foreign":
				runtime.processes = []Process{{PID: 112, Kind: "foreign"}}
				runtime.owned = map[int]bool{112: true}
			case "observe-error":
				runtime.observeFail = true
			case "staging":
				if e := os.Mkdir(filepath.Join(p.Root, "versions", ".replacement-123"), 0700); e != nil {
					t.Fatal(e)
				}
			case "pointer":
				os.Remove(filepath.Join(p.Root, "current"))
				os.Symlink("versions/other", filepath.Join(p.Root, "current"))
			case "bytes":
				os.WriteFile(filepath.Join(p.Root, "versions", p.Checksum, "active.conf"), []byte("changed"), 0600)
			case "permissions":
				os.Chmod(filepath.Join(p.Root, "versions", p.Checksum, "active.conf"), 0644)
			case "retention":
				writeAtomic(p.Root, "retention.json", []byte("{}\n"))
			case "replacement":
				m, _ := ReadReplacement(p.Root)
				m.Phase = "pointer-intent"
				writeReplacement(p.Root, m)
			case "status-after-review":
				state.Reason = "changed"
				saveTestStatus(t, p.Root, state)
			case "identity-after-review":
				state.Binding.InstanceID = strings.Repeat("b", 64)
				saveTestStatus(t, p.Root, state)
			case "target-after-review":
				q.Review.NextBinding.Host.NetworkNamespace = "1:2"
				q.Review.Checksum = rebindHash(q.Review)
				q.ReviewChecksum = q.Review.Checksum
			case "checksum":
				q.ReviewChecksum = strings.Repeat("c", 64)
			case "approval":
				q.ExclusiveInstanceAcknowledged = false
			case "tool-pins":
				q.Review.Tools.Router.SHA256 = strings.Repeat("f", 64)
				q.Review.Checksum = rebindHash(q.Review)
				q.ReviewChecksum = q.Review.Checksum
			case "unknown-status-field":
				raw, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
				raw = append([]byte("{\n  \"unknownWAL\": {},"), raw[1:]...)
				os.WriteFile(filepath.Join(p.Root, "status.json"), raw, 0600)
			}
			before, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
			if _, e := s.Apply(context.Background(), q); e == nil {
				t.Fatal("unsafe rebind accepted")
			}
			after, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
			if string(before) != string(after) {
				t.Fatal("refusal mutated status")
			}
			s.Close()
			if name == "foreign" && len(runtime.processes) != 1 {
				t.Fatal("review session closed an existing owned process")
			}
			if len(runtime.calls) != 0 {
				t.Fatal("refusal signaled stale PID or ran validator")
			}
		})
	}
}
func TestRebindReplacementAndHistoricalRetentionAcrossBoot(t *testing.T) {
	p, history, _ := previousBootFixture(t, 3)
	s, _ := rebindSession(t, p)
	review := reviewReboot(t, s)
	if _, e := s.Apply(context.Background(), rebindApproval(review)); e != nil {
		t.Fatal(e)
	}
	s.Close()
	// The deliberately unavailable historical tool does not block rebind. Native
	// startup still rejects it; new tools require the ordinary replacement path.
	native := NewNativeRuntime()
	if _, e := native.StartDNS(context.Background(), p, filepath.Join(p.Root, "versions", p.Checksum)); e == nil {
		native.Close()
		t.Fatal("obsolete executable pin ran")
	}
	c, runtime := testController(t, p)
	next := retentionNext(t, p, 10)
	next.Host, _ = CaptureTarget()
	next.Tools.Router.SHA256 = routing.Checksum("new reviewed tool")
	next.Checksum = digest(next)
	if _, e := c.Replace(context.Background(), replaceRequest(p, next)); e != nil {
		t.Fatal(e)
	}
	if runtime.calls[len(runtime.calls)-1] != "validate" {
		t.Fatal("new replacement was not validated")
	}
	cleanup, e := c.ReviewRetention(context.Background(), history)
	if e != nil {
		t.Fatal(e)
	}
	if len(cleanup.Candidates) != 2 || cleanup.Candidates[0].Host == nil {
		t.Fatal("old host proof was lost", cleanup.Candidates)
	}
	if _, e = c.ApplyRetention(context.Background(), cleanupRequest(cleanup)); e != nil {
		t.Fatal(e)
	}
	if e = verifyPlanVersion(next, filepath.Join(next.Root, "versions", next.Checksum)); e != nil {
		t.Fatal(e)
	}
	if e = verifyPlanVersion(p, filepath.Join(p.Root, "versions", p.Checksum)); e != nil {
		t.Fatal("baseline lost", e)
	}
}
func TestRebindAtomicCommitCrashBoundaries(t *testing.T) {
	for _, stage := range []string{"before-rename", "after-rename", "unrecognized-scratch"} {
		t.Run(stage, func(t *testing.T) {
			p, _, state := previousBootFixture(t, 1)
			s, runtime := rebindSession(t, p)
			review := reviewReboot(t, s)
			q := rebindApproval(review)
			switch stage {
			case "after-rename":
				saveTestStatus(t, p.Root, reboundStatus(state, review))
			case "unrecognized-scratch":
				os.WriteFile(filepath.Join(p.Root, ".write-interrupted"), []byte("unknown"), 0600)
			}
			before, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
			s.Close()
			s, runtime = rebindSession(t, p)
			_, result, e := s.Confirm(context.Background(), q)
			after, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
			if string(before) != string(after) {
				t.Fatal("open/confirm resumed writes")
			}
			if stage == "unrecognized-scratch" {
				if e == nil {
					t.Fatal("unknown scratch ignored")
				}
				return
			}
			if e != nil || stage == "after-rename" && result != "applied" || stage == "before-rename" && result != "unapplied" {
				t.Fatal(result, e)
			}
			if _, e = s.Apply(context.Background(), q); e != nil {
				t.Fatal(e)
			}
			if len(runtime.calls) != 0 {
				t.Fatal("file recovery mutated service")
			}
		})
	}
}
func TestRebindLegacyInstallBootstrapsWithoutPlanChange(t *testing.T) {
	p := testPlan(t, "bird2")
	raw, _ := EncodePlan(p)
	c, _ := testController(t, p)
	if _, e := c.Install(context.Background(), approved(p)); e != nil {
		t.Fatal(e)
	}
	c.state.Binding = nil
	if e := c.persist(); e != nil {
		t.Fatal(e)
	}
	state, e := c.Install(context.Background(), approved(p))
	if e != nil || state.Binding == nil {
		t.Fatal(e)
	}
	decoded, e := DecodePlan(raw)
	if e != nil || decoded.Checksum != p.Checksum {
		t.Fatal("legacy canonical plan changed", e)
	}
	if _, e = c.Install(context.Background(), approved(p)); e != nil {
		t.Fatal(e)
	}
	if c.state.Binding.InstanceID != state.Binding.InstanceID {
		t.Fatal("idempotent install replaced instance identity")
	}
}

func TestRebindStableIdentityPolicyAndSequenceBoundary(t *testing.T) {
	old := TargetPin{BootID: "00000000-0000-0000-0000-000000000001", NetworkNamespace: "1:2"}
	next := old
	next.BootID = "00000000-0000-0000-0000-000000000002"
	next.NetworkNamespace = "1:3"
	before := InstanceBinding{InstanceID: strings.Repeat("b", 64), Host: old, HostIdentity: scopeHostIdentity(strings.Repeat("b", 64), "0123456789abcdef0123456789abcdef")}
	after := before
	after.Host = next
	if e := rebindMode("same-host-reboot", &before, after, old); e != nil {
		t.Fatal("matching stable identity on new boot rejected", e)
	}
	after.HostIdentity = scopeHostIdentity(before.InstanceID, "abcdef0123456789abcdef0123456789")
	if e := rebindMode("same-host-reboot", &before, after, old); e == nil {
		t.Fatal("different host accepted as reboot")
	}
	p, _, state := previousBootFixture(t, 1)
	state.Sequence = 999999
	saveTestStatus(t, p.Root, state)
	session, _ := rebindSession(t, p)
	review := reviewReboot(t, session)
	if _, e := session.Apply(context.Background(), rebindApproval(review)); e != nil {
		t.Fatal(e)
	}
	if _, result, e := session.Confirm(context.Background(), rebindApproval(review)); e != nil || result != "applied" {
		t.Fatal("last atomic result not confirmable", result, e)
	}
}
