package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/as112ops"
)

func TestOfflineReplacementRequiresExactPairAndDoesNotStartServices(t *testing.T) {
	previous, previousFile := cliPlan(t)
	installCLI(t, previous, previousFile)
	bundle, err := as112.Decode(previous.DNSBundle)
	if err != nil {
		t.Fatal(err)
	}
	identity := bundle.Identity
	identity.Facility = "Replacement fixture"
	updated, err := as112.RenderNode(bundle.RouterID, bundle.RouterCreatedAt, as112.Service{Mode: bundle.Service, IPv4: true, IPv6: true}, identity, bundle.NodeAddresses)
	if err != nil {
		t.Fatal(err)
	}
	dns, err := as112.Encode(updated)
	if err != nil {
		t.Fatal(err)
	}
	next, err := as112ops.BuildPlan(as112ops.PlanInput{Root: previous.Root, Platform: previous.Platform, ActiveConfig: previous.ActiveConfig, DNSBundle: dns, Tools: previous.Tools, Host: previous.Host, HealthIntervalSeconds: previous.HealthIntervalSeconds, RecoveryHealthyChecks: previous.RecoveryHealthyChecks})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := as112ops.EncodePlan(next)
	if err != nil {
		t.Fatal(err)
	}
	nextFile := filepath.Join(filepath.Dir(previousFile), "next-plan.json")
	if err = writeNewPrivate(nextFile, raw); err != nil {
		t.Fatal(err)
	}
	args := []string{"replace", "--previous-plan", previousFile, "--plan", nextFile, "--previous-plan-checksum", previous.Checksum, "--current-target", filepath.Join("versions", previous.Checksum), "--plan-checksum", next.Checksum, "--active-checksum", next.ActiveChecksum, "--standby-checksum", next.StandbyChecksum, "--dns-checksum", next.DNSChecksum, "--exclusive-instance-acknowledged"}
	for _, bad := range [][]string{args[:len(args)-1], append(append([]string{}, args...), "--current-target", "versions/"+strings.Repeat("0", 64))} {
		if err = run(context.Background(), bad, &bytes.Buffer{}, func() as112ops.Runtime { t.Fatal("unapproved replacement reached a runtime"); return nil }); err == nil {
			t.Fatal("replacement omitted expected pointer or approval")
		}
	}
	model := newCLIRuntime()
	var output bytes.Buffer
	if err = run(context.Background(), args, &output, func() as112ops.Runtime { return model }); err != nil {
		t.Fatal(err)
	}
	status, err := as112ops.ReadStatus(previous.Root)
	if err != nil || status.PlanChecksum != next.Checksum || status.Phase != "withdrawn" || status.BGP != nil || status.DNS != nil || model.starts != 0 || len(model.stops) != 0 {
		t.Fatal("offline replacement confused publication with runtime activation", status, err)
	}
	link, err := os.Readlink(filepath.Join(previous.Root, "current"))
	if err != nil || link != filepath.Join("versions", next.Checksum) {
		t.Fatal("wrong replacement pointer", err)
	}
	if old, err := os.ReadFile(filepath.Join(previous.Root, "versions", previous.Checksum, "bundle.json")); err != nil || string(old) != previous.DNSBundle {
		t.Fatal("old reviewed version was not retained", err)
	}
	output.Reset()
	if err = run(context.Background(), []string{"status", "--root", previous.Root}, &output, nil); err != nil {
		t.Fatal(err)
	}
	var view statusView
	if json.Unmarshal(output.Bytes(), &view) != nil || view.Replacement == nil || view.Replacement.Phase != "complete" || view.LiveHealthConfirmed {
		t.Fatal("status lost its read-only replacement projection")
	}
	args[0] = "replace-reconcile"
	reconciler := newCLIRuntime()
	if err = run(context.Background(), args, &output, func() as112ops.Runtime { return reconciler }); err != nil || reconciler.starts != 0 || len(reconciler.stops) != 0 || reconciler.validations != 0 {
		t.Fatal("completed replacement was replayed", err)
	}
	if strings.Contains(output.String(), "Synthetic-CLI-Secret") || strings.Contains(output.String(), "activeConfig") {
		t.Fatal("replacement output leaked configuration")
	}
	for _, action := range []string{"replace", "replace-reconcile"} {
		if err = run(context.Background(), []string{action, "--help"}, &bytes.Buffer{}, nil); err != nil {
			t.Fatal(err)
		}
	}
}
