package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

const handoffTestSecret = "Handoff_private_fixture_Key"

func handoffFixture(t *testing.T, authenticated bool) (control.Review, routeros.PlanningHandoff) {
	t.Helper()
	review := emptyPlanningReview()
	review.Snapshot.CrossChannelIdentityVerified = true
	router := routing.Router{ID: review.Snapshot.Target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	peer := routing.Peer{ID: "peer-a", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 100, AllowedPrefixes: []string{"203.0.113.0/24"}}
	if authenticated {
		peer.AuthenticationRevision = strings.Repeat("A", 22)
		peer.Password = handoffTestSecret
	}
	handoff, err := routeros.NewPlanningHandoff(router, []routing.Peer{peer}, "draft-one", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	return review, handoff
}
func writeHandoffJSON(t *testing.T, path string, value any, mode os.FileMode) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return raw
}
func handoffPaths(t *testing.T, review control.Review, handoff routeros.PlanningHandoff) ([]string, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	reviewPath, handoffPath, output := filepath.Join(dir, "review.json"), filepath.Join(dir, "download.json"), filepath.Join(dir, "stage.json")
	writeHandoffJSON(t, reviewPath, response{OK: true, Review: &review}, 0600)
	writeHandoffJSON(t, handoffPath, handoff, 0644)
	return []string{"--review", reviewPath, "--handoff", handoffPath, "--output", output}, reviewPath, handoffPath, output
}
func readHandoffStage(t *testing.T, path string) stageWire {
	t.Helper()
	raw, err := privateRead(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	var stage stageWire
	if strictDecode(raw, &stage) != nil {
		t.Fatal("stage JSON invalid")
	}
	return stage
}
func requireNoHandoffOutput(t *testing.T, args []string, path string) {
	t.Helper()
	var output bytes.Buffer
	err := planCommand(args, &output)
	if err == nil || output.Len() != 0 {
		t.Fatal("invalid handoff produced an output/summary")
	}
	if strings.Contains(err.Error(), handoffTestSecret) {
		t.Fatal("handoff error exposed a credential")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("invalid handoff published a stage")
	}
}

func TestHandoffBrowserDownloadBuildsPrivateOfflineStage(t *testing.T) {
	review, handoff := handoffFixture(t, false)
	args, _, download, output := handoffPaths(t, review, handoff)
	if _, err := privateRead(download); err == nil {
		t.Fatal("old private reader was relaxed for browser files")
	}
	var summary bytes.Buffer
	if err := run(context.Background(), append([]string{"plan"}, args...), &summary); err != nil {
		t.Fatal(err)
	}
	stage := readHandoffStage(t, output)
	if stage.Plan.Target.RouterID != handoff.RouterID || stage.Plan.Desired.RouterID != handoff.Desired.RouterID || stage.Plan.Target.RouterID == stage.Plan.Desired.RouterID {
		t.Fatal("inventory ID and BGP router-id were confused")
	}
	if !stage.Plan.Options.Bootstrap || stage.Plan.Options.AllowIdentityChange || stage.ExclusiveAccessAcknowledged || stage.TimeoutSeconds != 60 || stage.Plan.NativeValidated || !stage.Plan.RequiresTargetValidation || !stage.Plan.RequiresExclusiveControl {
		t.Fatal("handoff inferred consent, timeout or native authority")
	}
	if routeros.ValidatePlan(stage.Plan) != nil || !bytes.Contains(summary.Bytes(), []byte(`"nativeValidated":false`)) {
		t.Fatal("offline plan validation/summary changed")
	}
	before, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := planCommand(args, &bytes.Buffer{}); err == nil {
		t.Fatal("existing stage was overwritten")
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(before, after) {
		t.Fatal("failed publication changed the existing stage")
	}
}

func TestHandoffRequiresPrivateMatchingNewCredentialAndKeepsBaselineSeparate(t *testing.T) {
	for _, kind := range []string{"missing", "baseline-only", "wrong-revision", "empty-password", "public-bundle", "valid"} {
		t.Run(kind, func(t *testing.T) {
			review, handoff := handoffFixture(t, true)
			args, _, download, output := handoffPaths(t, review, handoff)
			raw, _ := os.ReadFile(download)
			if bytes.Contains(raw, []byte(handoffTestSecret)) {
				t.Fatal("nonsecret handoff contains a password")
			}
			bundle := handoffCredentialBundle{Credentials: map[string]credentialWire{"peer-a": {Revision: strings.Repeat("A", 22), Password: handoffTestSecret}}, BaselineCredentials: map[string]credentialWire{"old-peer": {Revision: strings.Repeat("B", 22), Password: "Old_private_fixture_Key"}}}
			if kind != "missing" {
				switch kind {
				case "baseline-only":
					bundle.BaselineCredentials = bundle.Credentials
					bundle.Credentials = nil
				case "wrong-revision":
					bundle.Credentials["peer-a"] = credentialWire{Revision: strings.Repeat("C", 22), Password: handoffTestSecret}
				case "empty-password":
					bundle.Credentials["peer-a"] = credentialWire{Revision: strings.Repeat("A", 22)}
				}
				mode := os.FileMode(0600)
				if kind == "public-bundle" {
					mode = 0644
				}
				file := filepath.Join(filepath.Dir(output), "credentials.json")
				writeHandoffJSON(t, file, bundle, mode)
				args = append(args, "--credentials", file)
			}
			if kind != "valid" {
				requireNoHandoffOutput(t, args, output)
				return
			}
			args = append(args, "--exclusive-access", "--allow-identity-change", "--timeout", "75")
			var summary bytes.Buffer
			if err := planCommand(args, &summary); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(summary.Bytes(), []byte(handoffTestSecret)) || bytes.Contains(summary.Bytes(), []byte("Old_private_fixture_Key")) {
				t.Fatal("summary exposed private bundle")
			}
			stage := readHandoffStage(t, output)
			if !stage.ExclusiveAccessAcknowledged || !stage.Plan.Options.AllowIdentityChange || stage.TimeoutSeconds != 75 || stage.Credentials["peer-a"].Password != handoffTestSecret || stage.BaselineCredentials["old-peer"].Password != "Old_private_fixture_Key" {
				t.Fatal("explicit flags or private bundle were changed")
			}
			if _, err := routeros.NewAPIBridge(stage.Plan, credentials(stage.Credentials)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHandoffRejectsRetargetedUnverifiedAndAmbiguousInputs(t *testing.T) {
	for _, kind := range []string{"target", "digest", "unverified-review", "not-empty", "foreign", "authentication-missing", "authentication-null", "enabled-null", "enabled-case", "desired-case", "duplicate-key", "trailing", "injected-secret"} {
		t.Run(kind, func(t *testing.T) {
			review, handoff := handoffFixture(t, false)
			switch kind {
			case "target":
				router := routing.Router{ID: "different-inventory-id", ASN: handoff.Desired.ASN, RouterID: handoff.Desired.RouterID, LocalIPv4: handoff.Desired.LocalIPv4}
				var err error
				handoff, err = routeros.NewPlanningHandoff(router, nil, "draft-two", strings.Repeat("b", 64))
				if err != nil {
					t.Fatal(err)
				}
			case "digest":
				handoff.IntentSHA256 = strings.Repeat("0", 64)
			case "unverified-review":
				review.Snapshot.CrossChannelIdentityVerified = false
			case "not-empty":
				review.Snapshot.NamespaceEmpty = false
			case "foreign":
				review.Snapshot.ForeignReferences = true
			}
			args, _, download, output := handoffPaths(t, review, handoff)
			if strings.Contains(kind, "authentication") || strings.Contains(kind, "enabled") || kind == "desired-case" || kind == "injected-secret" {
				raw, _ := os.ReadFile(download)
				var object map[string]any
				if json.Unmarshal(raw, &object) != nil {
					t.Fatal("fixture decode")
				}
				desired := object["desired"].(map[string]any)
				peer := desired["peers"].([]any)[0].(map[string]any)
				switch kind {
				case "authentication-missing":
					delete(peer, "authentication")
				case "authentication-null":
					peer["authentication"] = nil
				case "enabled-null":
					peer["authentication"].(map[string]any)["enabled"] = nil
				case "enabled-case":
					peer["authentication"] = map[string]any{"Enabled": false}
				case "desired-case":
					object["Desired"] = desired
				case "injected-secret":
					object["credentials"] = map[string]any{"peer-a": map[string]any{"password": handoffTestSecret}}
				}
				writeHandoffJSON(t, download, object, 0644)
			}
			if kind == "duplicate-key" || kind == "trailing" {
				raw, _ := os.ReadFile(download)
				if kind == "duplicate-key" {
					raw = bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
				} else {
					raw = append(raw, []byte("{}")...)
				}
				if err := os.WriteFile(download, raw, 0644); err != nil {
					t.Fatal(err)
				}
			}
			requireNoHandoffOutput(t, args, output)
		})
	}
}

func TestHandoffFilePermissionsLinksAndLimitsRemainSeparate(t *testing.T) {
	for _, kind := range []string{"writable", "symlink", "hardlink", "fifo", "oversized", "public-review"} {
		t.Run(kind, func(t *testing.T) {
			review, handoff := handoffFixture(t, false)
			args, reviewPath, download, output := handoffPaths(t, review, handoff)
			switch kind {
			case "writable":
				if os.Chmod(download, 0664) != nil {
					t.Fatal("fixture chmod")
				}
			case "public-review":
				if os.Chmod(reviewPath, 0644) != nil {
					t.Fatal("fixture chmod")
				}
			case "symlink":
				target := download + ".source"
				if os.Rename(download, target) != nil || os.Symlink(target, download) != nil {
					t.Fatal("fixture symlink")
				}
			case "hardlink":
				if os.Link(download, download+".alias") != nil {
					t.Fatal("fixture hardlink")
				}
			case "fifo":
				if os.Remove(download) != nil || syscall.Mkfifo(download, 0600) != nil {
					t.Fatal("fixture FIFO")
				}
			case "oversized":
				if os.WriteFile(download, bytes.Repeat([]byte{'x'}, routeros.MaxPlanBytes+1), 0644) != nil {
					t.Fatal("fixture size")
				}
			}
			requireNoHandoffOutput(t, args, output)
		})
	}
	if info, err := os.Stat("/usr/bin/true"); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Geteuid()) {
			if _, err := planningHandoffRead("/usr/bin/true"); err == nil {
				t.Fatal("foreign-owned regular file accepted")
			}
		}
	}
}

func TestHandoffFlagsDoNotOverrideOldPrivateIntent(t *testing.T) {
	review, handoff := handoffFixture(t, false)
	args, reviewPath, download, output := handoffPaths(t, review, handoff)
	intent := filepath.Join(filepath.Dir(output), "old-intent.json")
	writeHandoffJSON(t, intent, initialIntent(), 0600)
	for _, tail := range [][]string{{"--file", intent}, {"--file", ""}, {"--timeout", "0"}, {"--timeout", "301"}} {
		requireNoHandoffOutput(t, append(append([]string{}, args...), tail...), output)
	}
	old := []string{"--review", reviewPath, "--file", intent, "--output", output}
	for _, tail := range [][]string{{"--credentials", download}, {"--timeout", "60"}, {"--exclusive-access=false"}, {"--allow-identity-change=false"}, {"--handoff", ""}} {
		requireNoHandoffOutput(t, append(append([]string{}, old...), tail...), output)
	}
	if os.Chmod(intent, 0644) != nil {
		t.Fatal("fixture chmod")
	}
	requireNoHandoffOutput(t, old, output)
}

// Deterministic structural fixture built from actual compiled plan rules. It
// does not claim to be a native device observation or bypass Stage rechecks.
func handoffManagedReview(t *testing.T, handoff routeros.PlanningHandoff, values map[string]credentialWire) control.Review {
	t.Helper()
	review := emptyPlanningReview()
	review.Snapshot.CrossChannelIdentityVerified = true
	plan, err := routeros.BuildReferencedPlan(review.Snapshot.Target, handoff.Desired, review.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := routeros.Compile(plan, credentials(values))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]routeros.CompiledCommand{}
	for _, command := range compiled.Commands {
		byID[command.OperationID] = command
	}
	digest := func(value any) string { raw, _ := json.Marshal(value); return routing.Checksum(string(raw)) }
	s := routeros.Snapshot{CrossChannelIdentityVerified: true, Target: plan.Target, Owner: plan.Owner, Managed: true, ASN: plan.Desired.ASN, RouterID: plan.Desired.RouterID, ActiveGeneration: plan.Generation, Table: routeros.ObjectRef{ID: "*1", Name: plan.Owner, StateChecksum: strings.Repeat("a", 64)}, Instance: routeros.ObjectRef{ID: "*1", Name: plan.Owner, StateChecksum: strings.Repeat("b", 64)}}
	gen := routeros.GenerationState{Generation: plan.Generation, PolicyChecksum: plan.PolicyChecksum}
	for _, op := range plan.Operations {
		body := ""
		for _, attribute := range byID[op.ID].Attributes {
			if attribute.Name == "rule" {
				body = attribute.Value
			}
		}
		if op.Kind == routeros.AddRule && strings.HasPrefix(op.Rule.Comment, plan.Owner+":g:") {
			gen.Rules = append(gen.Rules, routeros.ObjectRef{ID: fmt.Sprintf("*%X", len(gen.Rules)+3), Chain: op.Rule.Chain, Comment: op.Rule.Comment, StateChecksum: digest(*op.Rule), RuleSHA256: routing.Checksum(body)})
		}
		if op.Kind == routeros.SetEntry {
			ref := routeros.ObjectRef{ID: "*1", Chain: op.Rule.Chain, Comment: op.Rule.Comment, RuleSHA256: routing.Checksum(body)}
			if strings.HasSuffix(ref.Chain, "_out") {
				ref.ID = "*2"
			}
			ref.StateChecksum = digest(ref)
			if strings.HasSuffix(ref.Chain, "_in") {
				s.InputEntry = ref
			} else {
				s.OutputEntry = ref
			}
		}
		if op.Kind == routeros.AddDisabledConnection {
			connection := *op.Connection
			s.Connections = append(s.Connections, routeros.ConnectionState{Ref: routeros.ObjectRef{ID: fmt.Sprintf("*%X", len(s.Connections)+1), Name: connection.Name, StateChecksum: digest(connection)}, Config: connection, AuthenticationVerified: true})
		}
	}
	s.Generations = []routeros.GenerationState{gen}
	review.Snapshot = s
	review.ObservedAt = time.Now().UTC()
	return review
}

func TestHandoffManagedIdentityConsentAndExplicitAuthentication(t *testing.T) {
	_, handoff := handoffFixture(t, true)
	values := map[string]credentialWire{"peer-a": {Revision: strings.Repeat("A", 22), Password: handoffTestSecret}}
	review := handoffManagedReview(t, handoff, values)
	input, err := handoffPlanIntent(review, handoff, handoffCredentialBundle{}, 60, false, false)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := buildStageIntent(review, input)
	if err != nil || kept.Plan.Options.Bootstrap || !kept.Plan.Desired.Peers[0].Authentication.Enabled {
		t.Fatal("unchanged credential reference needed new secret or bootstrap", err)
	}
	router := routing.Router{ID: handoff.RouterID, ASN: 64503, RouterID: handoff.Desired.RouterID, LocalIPv4: handoff.Desired.LocalIPv4}
	peer := routing.Peer{ID: "peer-a", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 100, AllowedPrefixes: []string{"203.0.113.0/24"}}
	changed, err := routeros.NewPlanningHandoff(router, []routing.Peer{peer}, "changed-draft", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	input, err = handoffPlanIntent(review, changed, handoffCredentialBundle{}, 60, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildStageIntent(review, input); err == nil {
		t.Fatal("identity change was inferred from browser data")
	}
	input, err = handoffPlanIntent(review, changed, handoffCredentialBundle{}, 60, true, true)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := buildStageIntent(review, input)
	if err != nil {
		t.Fatal(err)
	}
	if stage.Plan.Options.Bootstrap || !stage.Plan.Options.AllowIdentityChange || !stage.ExclusiveAccessAcknowledged || stage.Plan.Desired.Peers[0].Authentication.Enabled || stage.Plan.NativeValidated {
		t.Fatal("handoff silently inherited old authentication or granted execution")
	}
	clearSeen := false
	for _, op := range stage.Plan.Operations {
		if op.Kind == routeros.SetAuthentication && op.Connection != nil && !op.Connection.Authentication.Enabled {
			clearSeen = true
		}
	}
	if !clearSeen {
		t.Fatal("explicit disabled authentication did not produce a clear operation")
	}
}
