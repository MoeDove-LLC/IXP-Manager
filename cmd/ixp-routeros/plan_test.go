package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

func emptyPlanningReview() control.Review {
	target := cliTarget()
	return control.Review{KnownState: routeros.KnownState{Target: target}, Snapshot: routeros.Snapshot{Target: target, Owner: routeros.OwnerName(target.RouterID), NamespaceEmpty: true}, ObservedAt: time.Now().UTC()}
}
func initialIntent() planIntent {
	return planIntent{ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", Peers: []peerIntent{{ID: "peer-a", ASN: 64501, Address: "198.51.100.2", MaxPrefixes: 100, AllowedPrefixes: []string{"203.0.113.0/24"}}}, Options: routeros.PlanOptions{Bootstrap: true}, TimeoutSeconds: 60}
}
func TestPlanPublishesPrivateStageAndPrintsOnlySummary(t *testing.T) {
	dir, err := os.MkdirTemp("", "routeros-plan-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	review := emptyPlanningReview()
	input := initialIntent()
	secret := "Private_planning_fixture_123"
	input.Credentials = map[string]credentialWire{"peer-a": {Revision: strings.Repeat("A", 22), Password: secret}}
	reviewPath, intentPath, outputPath := filepath.Join(dir, "review.json"), filepath.Join(dir, "desired.json"), filepath.Join(dir, "stage.json")
	for path, value := range map[string]any{reviewPath: response{OK: true, Review: &review}, intentPath: input} {
		body, e := json.Marshal(value)
		if e != nil || os.WriteFile(path, body, 0600) != nil {
			t.Fatal("private planning fixture", e)
		}
	}
	var output bytes.Buffer
	args := []string{"--review", reviewPath, "--file", intentPath, "--output", outputPath}
	if err := planCommand(args, &output); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte(secret)) || !bytes.Contains(output.Bytes(), []byte(`"nativeValidated":false`)) {
		t.Fatal("stdout exposed credentials or claimed execution authority")
	}
	body, err := privateRead(outputPath)
	if err != nil {
		t.Fatal("stage was not an owned single-link 0600 file", err)
	}
	var stage stageWire
	if strictDecode(body, &stage) != nil || !stage.Plan.Desired.Peers[0].Authentication.Enabled || stage.Credentials["peer-a"].Password != secret || stage.ExclusiveAccessAcknowledged {
		t.Fatal("stage intent or explicit access precondition changed")
	}
	if _, err := routeros.NewAPIBridge(stage.Plan, credentials(stage.Credentials)); err != nil {
		t.Fatal(err)
	}
	if err := planCommand(args, &bytes.Buffer{}); err == nil {
		t.Fatal("existing reviewed stage was overwritten")
	}
	unchanged, _ := os.ReadFile(outputPath)
	if !bytes.Equal(body, unchanged) {
		t.Fatal("failed publication modified existing output")
	}
	link := filepath.Join(dir, "alias.json")
	if os.Symlink(outputPath, link) != nil || writePrivateNew(link, []byte("changed")) == nil {
		t.Fatal("output symlink accepted")
	}
}

func TestPlanRejectsMissingSecretsAndContradictoryAuthentication(t *testing.T) {
	review, input := emptyPlanningReview(), initialIntent()
	input.Peers[0].Authentication = &routeros.Authentication{Enabled: true, Revision: strings.Repeat("A", 22)}
	if _, err := buildStageIntent(review, input); err == nil {
		t.Fatal("new key reference accepted without required private value")
	}
	input.Credentials = map[string]credentialWire{"peer-a": {Revision: strings.Repeat("A", 22), Password: "private_fixture_123"}}
	input.Peers[0].Authentication.Enabled = false
	if _, err := buildStageIntent(review, input); err == nil {
		t.Fatal("explicit clear silently ignored a supplied new password")
	}
	input.Peers[0].Authentication = nil
	input.Credentials["peer-a"] = credentialWire{Revision: strings.Repeat("A", 22)}
	if _, err := buildStageIntent(review, input); err == nil {
		t.Fatal("empty supplied key implicitly cleared authentication")
	}
	input.Credentials = map[string]credentialWire{"unlisted": {Password: "private_fixture"}}
	if _, err := buildStageIntent(review, input); err == nil {
		t.Fatal("unused credentials accepted into stage file")
	}
}

func TestOmittedAuthenticationPreservesExistingRevisionAndClearIsExplicit(t *testing.T) {
	old := routeros.Authentication{Enabled: true, Revision: strings.Repeat("A", 22)}
	kept, err := intendedAuthentication(old, nil, credentialWire{}, false)
	if err != nil || kept != old {
		t.Fatal("omitted authentication downgraded an existing peer", err)
	}
	clear := routeros.Authentication{}
	cleared, err := intendedAuthentication(old, &clear, credentialWire{}, false)
	if err != nil || cleared.Enabled || cleared.Revision != "" {
		t.Fatal("explicit clear did not remain explicit", err)
	}
	rotated, err := intendedAuthentication(old, nil, credentialWire{Revision: strings.Repeat("B", 22), Password: "new_private_fixture"}, true)
	if err != nil || !rotated.Enabled || rotated.Revision == old.Revision {
		t.Fatal("explicit new key did not select its new revision", err)
	}
}
