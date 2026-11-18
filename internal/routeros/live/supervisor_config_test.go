package live

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSupervisorBudgetsKeepDefaultsAndSeparateCommandLimits(t *testing.T) {
	defaults, err := (SupervisorConfig{}).ControllerConfig()
	if err != nil || defaults.OperationTimeout != 10*time.Second || defaults.PrepareTimeout != 10*time.Minute || defaults.MinConfirmationTimeout != 10*time.Second || defaults.MaxConfirmationTimeout != 5*time.Minute || defaults.ManualCleanup {
		t.Fatal("existing controller defaults changed", err)
	}
	cfg := fixedConfig(t)
	cfg.OperationTimeoutSeconds, cfg.ObservationTimeoutSeconds, cfg.SessionLifetimeSeconds = 15, 180, 600
	cfg.Supervisor = SupervisorConfig{OperationTimeoutSeconds: 30, PrepareTimeoutSeconds: 420, MaxConfirmationTimeoutSeconds: 120}
	prepared, err := validateConfig(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := cfg.Supervisor.ControllerConfig()
	if err != nil || controller.OperationTimeout != 30*time.Second || controller.PrepareTimeout != 7*time.Minute || controller.MaxConfirmationTimeout != 2*time.Minute {
		t.Fatal("private supervisor budgets were not mapped", err)
	}
	if prepared.config.OperationTimeoutSeconds != 15 || prepared.config.ObservationTimeoutSeconds != 180 || prepared.config.SessionLifetimeSeconds != 600 {
		t.Fatal("supervisor budgets changed command, observation or SSH lifetime limits")
	}
	partial, err := (SupervisorConfig{OperationTimeoutSeconds: 30}).ControllerConfig()
	if err != nil || partial.PrepareTimeout != 600*time.Second || partial.MaxConfirmationTimeout != 300*time.Second {
		t.Fatal("omitted individual supervisor limits did not retain defaults")
	}
}

func TestSupervisorBudgetsRejectInvalidPrivateConfiguration(t *testing.T) {
	for _, budgets := range []SupervisorConfig{
		{OperationTimeoutSeconds: -1}, {OperationTimeoutSeconds: 31},
		{PrepareTimeoutSeconds: -1}, {PrepareTimeoutSeconds: 901},
		{MaxConfirmationTimeoutSeconds: -1}, {MaxConfirmationTimeoutSeconds: 9}, {MaxConfirmationTimeoutSeconds: 301},
	} {
		cfg := fixedConfig(t)
		cfg.Supervisor = budgets
		cfg.API.CAFile = "/missing-fixture-ca"
		_, err := validateConfig(cfg, false)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Code != "invalid-supervisor-bounds" {
			t.Fatal("invalid service budgets were not rejected before trust-file reads", err)
		}
	}
	for _, budgets := range []SupervisorConfig{{OperationTimeoutSeconds: 1, PrepareTimeoutSeconds: 1, MaxConfirmationTimeoutSeconds: 10}, {OperationTimeoutSeconds: 30, PrepareTimeoutSeconds: 900, MaxConfirmationTimeoutSeconds: 300}} {
		if _, err := budgets.ControllerConfig(); err != nil {
			t.Fatal("supported boundary rejected", err)
		}
	}
	cfg := fixedConfig(t)
	type plain Config
	raw, _ := json.Marshal(plain(cfg))
	var object map[string]any
	json.Unmarshal(raw, &object)
	for _, value := range []any{nil, map[string]any{"operationTimeoutSeconds": nil}, map[string]any{"operationTimeoutSeconds": 30.5}, map[string]any{"operationTimeoutSeconds": "30"}, map[string]any{"activationVerified": true}, []any{30}} {
		object["supervisor"] = value
		candidate, _ := json.Marshal(object)
		path := filepath.Join(t.TempDir(), "private.json")
		if err := os.WriteFile(path, candidate, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("invalid nested service policy accepted")
		}
	}
	var duplicate SupervisorConfig
	if err := json.Unmarshal([]byte(`{"operationTimeoutSeconds":10,"operationTimeoutSeconds":30}`), &duplicate); err == nil {
		t.Fatal("ambiguous duplicate budget field accepted")
	}
	object["supervisor"] = map[string]any{"operationTimeoutSeconds": 30, "prepareTimeoutSeconds": 420, "maxConfirmationTimeoutSeconds": 120, "manualCleanup": true}
	raw, _ = json.Marshal(object)
	path := filepath.Join(t.TempDir(), "private.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Supervisor.OperationTimeoutSeconds != 30 || loaded.Supervisor.PrepareTimeoutSeconds != 420 || loaded.Supervisor.MaxConfirmationTimeoutSeconds != 120 || !loaded.Supervisor.ManualCleanup {
		t.Fatal("valid private nested policy lost", err)
	}
	controller, err := loaded.Supervisor.ControllerConfig()
	if err != nil || !controller.ManualCleanup {
		t.Fatal("manual cleanup policy was not passed to the controller", err)
	}
}

func TestSupervisorManualCleanupIsStrictOptionalBoolean(t *testing.T) {
	var cfg SupervisorConfig
	for _, raw := range []string{
		`{"manualCleanup":null}`, `{"manualCleanup":"true"}`, `{"manualCleanup":1}`, `{"manualCleanup":[]}`,
		`{"manualCleanup":true,"manualCleanup":false}`, `{"ManualCleanup":true}`,
	} {
		if err := json.Unmarshal([]byte(raw), &cfg); err == nil {
			t.Fatal("ambiguous manual cleanup policy accepted", raw)
		}
	}
	if err := json.Unmarshal([]byte(`{"manualCleanup":true}`), &cfg); err != nil || !cfg.ManualCleanup {
		t.Fatal("explicit manual cleanup was lost", err)
	}
	if err := json.Unmarshal([]byte(`{"manualCleanup":false}`), &cfg); err != nil || cfg.ManualCleanup {
		t.Fatal("explicit automatic cleanup was lost", err)
	}
}

func TestCapabilityRequirementsDoNotGrantProductionWrites(t *testing.T) {
	p := CapabilityProfile()
	if p.Version != "7.24.6" || !p.FilterRuleDisabledSelectionVerified {
		t.Fatal("verified fixed-version read-only selection capability lost")
	}
	if p.PreparationVerified || p.ActivationVerified || p.RuleBatchVerified || p.CleanupVerified || p.ObjectObservationVerified || p.PreparedDiscardVerified || p.RuleRemovalBatchVerified || p.RuleRemovalGlobalTailVerified || p.MaxHistoryActions != 0 {
		t.Fatal("budget settings or fixture metadata opened production writes")
	}
	if !slices.Contains(p.Requirements, "exclusive-device-window") || !slices.Contains(p.Requirements, "fixed-tls-ssh-target") || slices.Contains(p.Blockers, "exclusive-device-window-not-established") || !slices.Contains(p.Blockers, "large-update-confirm-cleanup-unverified") {
		t.Fatal("request preconditions were conflated with missing native evidence")
	}
}
