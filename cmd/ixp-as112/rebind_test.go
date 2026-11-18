package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/as112ops"
)

func rebindCLIState(t *testing.T, legacy bool) (as112ops.Plan, string, as112ops.Status) {
	t.Helper()
	p, file := cliPlan(t)
	installCLI(t, p, file)
	state, e := as112ops.ReadStatus(p.Root)
	if e != nil {
		t.Fatal(e)
	}
	state.Binding.Host.BootID = "00000000-0000-0000-0000-000000000001"
	state.Binding.Host.NetworkNamespace = "41:112"
	state.Phase = "active"
	state.HealthyChecks = 2
	state.LastHealthy = true
	state.DNS = &as112ops.Process{PID: 112, Kind: "dns", StartTime: "old-boot"}
	state.BGP = &as112ops.Process{PID: 113, Kind: "bgp", Mode: "active", StartTime: "old-boot"}
	if legacy {
		state.Binding = nil
	}
	raw, e := json.MarshalIndent(state, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(p.Root, "status.json"), append(raw, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	return p, file, state
}
func TestRebindCLIReviewConfirmAndExplicitApply(t *testing.T) {
	p, file, before := rebindCLIState(t, false)
	kind := "same-host-reboot"
	if before.Binding.HostIdentity == "" {
		kind = "move"
	}
	if os.Getenv("AS112_REQUIRE_STABLE_IDENTITY") == "1" && kind != "same-host-reboot" {
		t.Fatal("synthetic stable-host identity fixture is unavailable")
	}
	t.Logf("review mode: %s", kind)
	model := newCLIRuntime()
	factory := func() as112ops.Runtime { return model }
	reviewFile := filepath.Join(filepath.Dir(file), "rebind-review.json")
	args := []string{"rebind-plan", "--plan", file, "--mode", kind, "--output", reviewFile}
	unsafe := append([]string{}, args...)
	unsafe[len(unsafe)-1] = filepath.Join(p.Root, "bad-review.json")
	if e := run(context.Background(), unsafe, &bytes.Buffer{}, func() as112ops.Runtime { t.Fatal("unsafe output reached runtime"); return nil }); e == nil {
		t.Fatal("review inside instance")
	}
	rawBefore, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
	var output bytes.Buffer
	if e := run(context.Background(), args, &output, factory); e != nil {
		t.Fatal(e)
	}
	rawAfter, _ := os.ReadFile(filepath.Join(p.Root, "status.json"))
	if string(rawBefore) != string(rawAfter) {
		t.Fatal("review mutated old active state")
	}
	raw, e := readPrivate(reviewFile, as112ops.MaxRebindReviewBytes)
	if e != nil {
		t.Fatal(e)
	}
	review, e := as112ops.DecodeRebindReview(raw)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), "Synthetic-CLI-Secret") || strings.Contains(output.String(), "activeConfig") {
		t.Fatal("review disclosed config")
	}
	apply := []string{"rebind-apply", "--plan", file, "--review", reviewFile, "--checksum", review.Checksum, "--acknowledge", "--exclusive-instance-acknowledged"}
	if kind == "move" {
		apply = append(apply, "--source-isolated-acknowledged")
	}
	if e = run(context.Background(), apply[:len(apply)-1], &bytes.Buffer{}, func() as112ops.Runtime { t.Fatal("unapproved rebind reached runtime"); return nil }); e == nil {
		t.Fatal("missing approval")
	}
	confirm := append([]string{}, apply...)
	confirm[0] = "rebind-confirm"
	output.Reset()
	if e = run(context.Background(), confirm, &output, factory); e != nil || !strings.Contains(output.String(), `"result":"unapplied"`) {
		t.Fatal(e, output.String())
	}
	if e = run(context.Background(), apply, &bytes.Buffer{}, factory); e != nil {
		t.Fatal(e)
	}
	output.Reset()
	if e = run(context.Background(), confirm, &output, factory); e != nil || !strings.Contains(output.String(), `"result":"applied"`) {
		t.Fatal(e, output.String())
	}
	if e = run(context.Background(), apply, &bytes.Buffer{}, factory); e != nil {
		t.Fatal("exact atomic result not idempotent", e)
	}
	state, e := as112ops.ReadStatus(p.Root)
	if e != nil || state.Phase != "withdrawn" || state.DNS != nil || state.BGP != nil || state.Binding.InstanceID != before.Binding.InstanceID || state.PlanChecksum != p.Checksum {
		t.Fatal(state, e)
	}
	if model.starts != 0 || len(model.stops) != 0 || model.validations != 0 {
		t.Fatal("rebind touched native operation")
	}
	output.Reset()
	if e = run(context.Background(), []string{"status", "--root", p.Root}, &output, nil); e != nil || !strings.Contains(output.String(), review.Checksum) {
		t.Fatal("status omitted approved binding", e)
	}
}
func TestRebindCLILegacyRequiresMoveSourceIsolation(t *testing.T) {
	p, file, _ := rebindCLIState(t, true)
	factory := func() as112ops.Runtime { return newCLIRuntime() }
	reviewFile := filepath.Join(filepath.Dir(file), "move-review.json")
	if e := run(context.Background(), []string{"rebind-plan", "--plan", file, "--output", reviewFile}, &bytes.Buffer{}, factory); e == nil {
		t.Fatal("legacy claimed same host")
	}
	if e := run(context.Background(), []string{"rebind-plan", "--plan", file, "--mode", "move", "--output", reviewFile}, &bytes.Buffer{}, factory); e != nil {
		t.Fatal(e)
	}
	raw, _ := readPrivate(reviewFile, as112ops.MaxRebindReviewBytes)
	review, e := as112ops.DecodeRebindReview(raw)
	if e != nil {
		t.Fatal(e)
	}
	apply := []string{"rebind-apply", "--plan", file, "--review", reviewFile, "--checksum", review.Checksum, "--acknowledge", "--exclusive-instance-acknowledged"}
	if e = run(context.Background(), apply, &bytes.Buffer{}, func() as112ops.Runtime { t.Fatal("missing source isolation reached runtime"); return nil }); e == nil {
		t.Fatal("unacknowledged source")
	}
	apply = append(apply, "--source-isolated-acknowledged")
	if e = run(context.Background(), apply, &bytes.Buffer{}, factory); e != nil {
		t.Fatal(e)
	}
	if _, e = readPlan(file); e != nil {
		t.Fatal("original plan no longer canonical", e)
	}
	state, e := as112ops.ReadStatus(p.Root)
	if e != nil || state.Binding == nil {
		t.Fatal(e)
	}
}
