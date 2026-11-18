package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/as112ops"
)

func retentionCLIInstance(t *testing.T) (as112ops.Plan, string, string) {
	t.Helper()
	p, firstFile := cliPlan(t)
	installCLI(t, p, firstFile)
	currentFile := firstFile
	for i := 1; i <= 2; i++ {
		b, e := as112.Decode(p.DNSBundle)
		if e != nil {
			t.Fatal(e)
		}
		id := b.Identity
		id.Facility = fmt.Sprintf("CLI retained %d", i)
		b, e = as112.RenderNode(b.RouterID, b.RouterCreatedAt, as112.Service{Mode: b.Service, IPv4: true, IPv6: true}, id, b.NodeAddresses)
		if e != nil {
			t.Fatal(e)
		}
		dns, e := as112.Encode(b)
		if e != nil {
			t.Fatal(e)
		}
		next, e := as112ops.BuildPlan(as112ops.PlanInput{Root: p.Root, Platform: p.Platform, ActiveConfig: p.ActiveConfig, DNSBundle: dns, Tools: p.Tools, Host: p.Host, HealthIntervalSeconds: p.HealthIntervalSeconds, RecoveryHealthyChecks: p.RecoveryHealthyChecks})
		if e != nil {
			t.Fatal(e)
		}
		raw, e := as112ops.EncodePlan(next)
		if e != nil {
			t.Fatal(e)
		}
		nextFile := filepath.Join(filepath.Dir(firstFile), fmt.Sprintf("retained-%d.json", i))
		if e = writeNewPrivate(nextFile, raw); e != nil {
			t.Fatal(e)
		}
		args := []string{"replace", "--previous-plan", currentFile, "--plan", nextFile, "--previous-plan-checksum", p.Checksum, "--current-target", filepath.Join("versions", p.Checksum), "--plan-checksum", next.Checksum, "--active-checksum", next.ActiveChecksum, "--standby-checksum", next.StandbyChecksum, "--dns-checksum", next.DNSChecksum, "--exclusive-instance-acknowledged"}
		if e = run(context.Background(), args, &bytes.Buffer{}, func() as112ops.Runtime { return newCLIRuntime() }); e != nil {
			t.Fatal(e)
		}
		p, currentFile = next, nextFile
	}
	return p, currentFile, firstFile
}

func TestRetentionCLIReviewApplyAndStatus(t *testing.T) {
	p, current, old := retentionCLIInstance(t)
	reviewFile := filepath.Join(filepath.Dir(current), "retention-review.json")
	model := newCLIRuntime()
	var output bytes.Buffer
	args := []string{"retention-plan", "--plan", current, "--history-plan", old, "--output", reviewFile}
	unsafeOutput := append([]string{}, args...)
	unsafeOutput[len(unsafeOutput)-1] = filepath.Join(p.Root, "review.json")
	if e := run(context.Background(), unsafeOutput, &output, func() as112ops.Runtime { t.Fatal("review inside instance reached runtime"); return nil }); e == nil {
		t.Fatal("read-only review could modify its instance")
	}
	if e := run(context.Background(), args, &output, func() as112ops.Runtime { return model }); e != nil {
		t.Fatal(e)
	}
	if model.starts != 0 || len(model.stops) != 0 || model.validations != 0 {
		t.Fatal("review touched native service or checker")
	}
	raw, e := readPrivate(reviewFile, as112ops.MaxRetentionReviewBytes)
	if e != nil {
		t.Fatal(e)
	}
	review, e := as112ops.DecodeRetentionReview(raw)
	if e != nil || len(review.Candidates) != 1 {
		t.Fatal(len(review.Candidates), e)
	}
	if strings.Contains(string(raw), "Synthetic-CLI-Secret") || strings.Contains(output.String(), "Synthetic-CLI-Secret") || strings.Contains(output.String(), "activeConfig") {
		t.Fatal("cleanup review exposed credentials")
	}
	apply := []string{"retention-apply", "--plan", current, "--review", reviewFile, "--checksum", review.Checksum, "--acknowledge", "--exclusive-instance-acknowledged"}
	if e = run(context.Background(), apply[:len(apply)-1], &bytes.Buffer{}, func() as112ops.Runtime { t.Fatal("unapproved deletion constructed runtime"); return nil }); e == nil {
		t.Fatal("missing explicit approval accepted")
	}
	output.Reset()
	if e = run(context.Background(), apply, &output, func() as112ops.Runtime { return model }); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(p.Root, "versions", review.Candidates[0].PlanChecksum)); !os.IsNotExist(e) {
		t.Fatal("approved old version still exists")
	}
	if model.starts != 0 || len(model.stops) != 0 || model.validations != 0 {
		t.Fatal("cleanup started a service/checker")
	}
	output.Reset()
	if e = run(context.Background(), []string{"status", "--root", p.Root}, &output, nil); e != nil {
		t.Fatal(e)
	}
	var view statusView
	if json.Unmarshal(output.Bytes(), &view) != nil || view.Retention == nil || view.Retention.Phase != "complete" || view.LiveHealthConfirmed {
		t.Fatal("retention status is not a journal projection")
	}
	for _, mode := range []string{"retention-plan", "retention-apply", "retention-reconcile"} {
		if e = run(context.Background(), []string{mode, "--help"}, &bytes.Buffer{}, nil); e != nil {
			t.Fatal(e)
		}
	}
}

func TestRetentionCLIUncertainPresenceRequiresNewReview(t *testing.T) {
	p, current, old := retentionCLIInstance(t)
	file := filepath.Join(filepath.Dir(current), "initial-review.json")
	if e := run(context.Background(), []string{"retention-plan", "--plan", current, "--history-plan", old, "--output", file}, &bytes.Buffer{}, func() as112ops.Runtime { return newCLIRuntime() }); e != nil {
		t.Fatal(e)
	}
	raw, e := readPrivate(file, as112ops.MaxRetentionReviewBytes)
	if e != nil {
		t.Fatal(e)
	}
	review, e := as112ops.DecodeRetentionReview(raw)
	if e != nil {
		t.Fatal(e)
	}
	pending := as112ops.RetentionStatus{Version: 1, Review: review, Phase: "unknown", Cursor: 0, Pending: &as112ops.RetentionIntent{Index: 0, Path: review.RemainingPaths()[0], StartedAt: time.Now().UTC()}, UpdatedAt: time.Now().UTC()}
	journal, _ := json.MarshalIndent(pending, "", "  ")
	if e = os.WriteFile(filepath.Join(p.Root, "retention.json"), append(journal, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	state, e := as112ops.ReadStatus(p.Root)
	if e != nil {
		t.Fatal(e)
	}
	state.Phase = "unknown"
	state.Reason = "retention: simulated interrupted unlink"
	status, _ := json.MarshalIndent(state, "", "  ")
	if e = os.WriteFile(filepath.Join(p.Root, "status.json"), append(status, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	args := []string{"retention-reconcile", "--plan", current, "--review", file, "--checksum", review.Checksum, "--acknowledge", "--exclusive-instance-acknowledged"}
	if e = run(context.Background(), args, &bytes.Buffer{}, func() as112ops.Runtime { return newCLIRuntime() }); e == nil {
		t.Fatal("uncertain present target was replayed")
	}
	renewedFile := filepath.Join(filepath.Dir(current), "new-review.json")
	if e = run(context.Background(), []string{"retention-plan", "--plan", current, "--resume", "--output", renewedFile}, &bytes.Buffer{}, func() as112ops.Runtime { return newCLIRuntime() }); e != nil {
		t.Fatal(e)
	}
	nextRaw, e := readPrivate(renewedFile, as112ops.MaxRetentionReviewBytes)
	if e != nil {
		t.Fatal(e)
	}
	next, e := as112ops.DecodeRetentionReview(nextRaw)
	if e != nil || next.Checksum == review.Checksum || next.ResumeOf != review.Checksum {
		t.Fatal("resume did not issue a new binding", e)
	}
	if _, e = os.Stat(filepath.Join(p.Root, pending.Pending.Path)); e != nil {
		t.Fatal("review performed deletion")
	}
	args = []string{"retention-apply", "--plan", current, "--review", renewedFile, "--checksum", next.Checksum, "--acknowledge", "--exclusive-instance-acknowledged"}
	if e = run(context.Background(), args, &bytes.Buffer{}, func() as112ops.Runtime { return newCLIRuntime() }); e != nil {
		t.Fatal("new explicit authorization failed", e)
	}
}
