// Package live connects the constrained RouterOS controller to authenticated
// API-SSL and a single pinned, persistent SSH terminal. Device evidence is read
// here; callers cannot provide transport/native-validation truth flags.
package live

import (
	"fmt"
	"io"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

type APIConfig struct {
	Address      string `json:"address"`
	ServerName   string `json:"serverName"`
	Username     string `json:"username"`
	PasswordFile string `json:"passwordFile"`
	CAFile       string `json:"caFile"`
	SPKISHA256   string `json:"spkiSha256"`
}
type SSHConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	IdentityFile   string `json:"identityFile"`
	KnownHostsFile string `json:"knownHostsFile"`
	HostKeySHA256  string `json:"hostKeySha256"` // SHA256 of decoded SSH public-key wire bytes, lowercase hex
}
type Config struct {
	SchemaVersion             int                     `json:"schemaVersion"`
	RouterID                  string                  `json:"routerId"`
	Version                   string                  `json:"version"` // only the implemented exact 7.24.6 profile
	DeviceID                  string                  `json:"deviceId"`
	IdentitySource            routeros.IdentitySource `json:"identitySource"`
	API                       APIConfig               `json:"api"`
	SSH                       SSHConfig               `json:"ssh"`
	Supervisor                SupervisorConfig        `json:"supervisor,omitempty"`
	ObservationTimeoutSeconds int                     `json:"observationTimeoutSeconds,omitempty"`
	OperationTimeoutSeconds   int                     `json:"operationTimeoutSeconds,omitempty"`
	SessionLifetimeSeconds    int                     `json:"sessionLifetimeSeconds,omitempty"`
	MaxObjects                int                     `json:"maxObjects,omitempty"`
	MaxGetterCalls            int                     `json:"maxGetterCalls,omitempty"`
}

func (Config) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "RouterOS private live configuration (redacted)")
}
func (Config) MarshalJSON() ([]byte, error) {
	return []byte(`{"configuration":"private-fixed-target"}`), nil
}

type NativeCapabilityProfile struct {
	OwnedMutationFixtureVerified             bool     `json:"ownedMutationFixtureVerified"`
	OwnedStreamOrderFixtureVerified          bool     `json:"ownedStreamOrderFixtureVerified"`
	GeneratedTwoPeerIPv4FixtureVerified      bool     `json:"generatedTwoPeerIpv4FixtureVerified"`
	GeneratedTwoPeerIPv6FixtureVerified      bool     `json:"generatedTwoPeerIpv6FixtureVerified"`
	LargeRuleBatch1000FixtureVerified        bool     `json:"largeRuleBatch1000FixtureVerified"`
	AutomaticCleanupFixtureVerified          bool     `json:"automaticCleanupFixtureVerified"`
	PreparedDiscardCycleFixtureVerified      bool     `json:"preparedDiscardCycleFixtureVerified"`
	SupervisorConfirmFixtureVerified         bool     `json:"supervisorConfirmFixtureVerified"`
	SupervisorRestartReadOnlyFixtureVerified bool     `json:"supervisorRestartReadOnlyFixtureVerified"`
	SupervisorCancelFixtureVerified          bool     `json:"supervisorCancelFixtureVerified"`
	Version                                  string   `json:"version"`
	APIPropertiesObserved                    bool     `json:"apiPropertiesObserved"`
	ReadOnlyObserverNativeVerified           bool     `json:"readOnlyObserverNativeVerified"`
	IdleResetNativeVerified                  bool     `json:"idleResetNativeVerified"`
	GeneratedPrepareFixtureVerified          bool     `json:"generatedPrepareFixtureVerified"`
	SSHProcessDisconnectFixtureVerified      bool     `json:"sshProcessDisconnectFixtureVerified"`
	IPv4SinglePeerCacheFixtureVerified       bool     `json:"ipv4SinglePeerCacheFixtureVerified"`
	SSHOwnerNativeVerified                   bool     `json:"sshOwnerNativeVerified"`
	ConfirmNativeVerified                    bool     `json:"confirmNativeVerified"`
	PreparationVerified                      bool     `json:"preparationVerified"`
	ActivationVerified                       bool     `json:"activationVerified"`
	RuleBatchVerified                        bool     `json:"ruleBatchVerified"`
	CleanupVerified                          bool     `json:"cleanupVerified"`
	ObjectObservationVerified                bool     `json:"objectObservationVerified"`
	PreparedDiscardVerified                  bool     `json:"preparedDiscardVerified"`
	RuleRemovalBatchVerified                 bool     `json:"ruleRemovalBatchVerified"`
	RuleRemovalGlobalTailVerified            bool     `json:"ruleRemovalGlobalTailVerified"`
	FilterRuleDisabledSelectionVerified      bool     `json:"filterRuleDisabledSelectionVerified"`
	MaxHistoryActions                        int      `json:"maxHistoryActions"`
	Blockers                                 []string `json:"blockers"`
	Requirements                             []string `json:"requirements"`
}

// CapabilityProfile is code-owned evidence, not configuration. A successful
// read does not grant write/activation or establish an undo history budget.
func CapabilityProfile() NativeCapabilityProfile {
	return NativeCapabilityProfile{Version: "7.24.6", APIPropertiesObserved: true, FilterRuleDisabledSelectionVerified: true, ReadOnlyObserverNativeVerified: true, IdleResetNativeVerified: true, SSHProcessDisconnectFixtureVerified: true, IPv4SinglePeerCacheFixtureVerified: true, SSHOwnerNativeVerified: true, ConfirmNativeVerified: true, OwnedMutationFixtureVerified: true, OwnedStreamOrderFixtureVerified: true, GeneratedTwoPeerIPv4FixtureVerified: true, GeneratedTwoPeerIPv6FixtureVerified: true, LargeRuleBatch1000FixtureVerified: true, AutomaticCleanupFixtureVerified: true, PreparedDiscardCycleFixtureVerified: true, Blockers: []string{"per-operation-undo-budget-unverified", "identity-and-authentication-transition-matrix-unverified", "device-fault-recovery-unverified", "large-update-confirm-cleanup-unverified"}, Requirements: []string{"exclusive-device-window", "fixed-tls-ssh-target"}}
}

type Status struct {
	Target                       routeros.Target         `json:"target"`
	ObservedAt                   time.Time               `json:"observedAt"`
	SessionExpiresAt             time.Time               `json:"sessionExpiresAt"`
	Digest                       string                  `json:"digest"`
	CrossChannelIdentityVerified bool                    `json:"crossChannelIdentityVerified"`
	StrictSnapshotVerified       bool                    `json:"strictSnapshotVerified"`
	Owner                        control.Owner           `json:"owner"`
	Profile                      NativeCapabilityProfile `json:"profile"`
	LastCode                     string                  `json:"lastCode,omitempty"`
}
type Failure struct {
	Code      string
	Uncertain bool
}

func (e *Failure) Error() string {
	if e.Uncertain {
		return "RouterOS live " + e.Code + ": mutation outcome uncertain; do not retry"
	}
	return "RouterOS live " + e.Code
}
func fail(code string) error { return &Failure{Code: code} }
