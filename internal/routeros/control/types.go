// Package control owns the durable RouterOS transaction state machine. It has
// no HTTP server and accepts no transport address or arbitrary command input.
package control

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

type Binding struct {
	Target                        routeros.Target
	PrepareVerified               bool
	ActivationVerified            bool
	ConfirmationVerified          bool
	MaxHistoryActions             int
	RuleBatchVerified             bool
	CleanupVerified               bool
	ObjectObservationVerified     bool
	PreparedDiscardVerified       bool
	RuleRemovalBatchVerified      bool
	RuleRemovalGlobalTailVerified bool
}
type Owner struct {
	SessionID      string `json:"sessionId,omitempty"`
	Known          bool   `json:"known"`
	Active         bool   `json:"active"`
	ThisSession    bool   `json:"thisSession"`
	Alive          bool   `json:"alive"`
	HistoryActions int    `json:"historyActions"`
	HistoryKnown   bool   `json:"historyKnown"`
}
type ReadRequest struct {
	KnownState          routeros.KnownState
	BaselineCredentials routeros.Credentials
	Strict              bool
	CredentialAuthority []CredentialAuthority
}

// CredentialAuthority is issued only by this supervisor from authenticated
// journal objects. It is not a frontend claim or a name/prefix-based grant.
type CredentialAuthority struct {
	ID                 string
	Name               string
	AttributesChecksum string
}

func (ReadRequest) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS controlled read request (credentials redacted)")
}
func (ReadRequest) MarshalJSON() ([]byte, error) { return []byte(`{"readRequest":"private"}`), nil }

type Observation struct {
	Target                       routeros.Target
	Inventory                    api.Inventory      // normalized complete fixed-menu read set
	Snapshot                     *routeros.Snapshot // required for the initial Strict=true read
	Resolver                     *routeros.APIResolver
	Credentials                  map[string]api.Credential
	CrossChannelIdentityVerified bool
	ObservedAt                   time.Time
	Owner                        Owner
}

func (Observation) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS controlled observation (raw values redacted)")
}
func (Observation) MarshalJSON() ([]byte, error) { return []byte(`{"observation":"private"}`), nil }

type Runtime interface {
	Binding() Binding
	BindPlan(routeros.Plan, routeros.Credentials) error
	Observe(context.Context, ReadRequest) (Observation, error)
	Apply(context.Context, routeros.APIMutation) (api.Result, error)
	Take(context.Context, time.Time) (Owner, error)
	Owner(context.Context) (Owner, error)
	Confirm(context.Context) (Owner, error)
	Cancel(context.Context) (Owner, error)
	CloseOwner() error
}

// RuleBatch is a bounded contiguous set of canonical, newly created,
// unreferenced generation FilterRule additions. It never includes entry roots,
// tables, instances, connection changes or credentials.
type RuleBatch struct {
	ID           string
	PlanChecksum string
	Generation   string
	Operations   []routeros.APIMutation
}
type RuleBatchRecorder interface {
	Before(operationID string) error
	After(operationID string, result api.Result, writeErr error) error
}
type RuleBatchRuntime interface {
	ApplyRuleBatch(context.Context, RuleBatch, RuleBatchRecorder) error
}

// CleanupBinding is derived only from the authenticated confirmed journal.
// RemainingOperationIDs must be an exact suffix of that plan's Cleanup phase.
// It carries no authority to repeat Prepare/Activate or acquire Safe Mode.
type CleanupBinding struct {
	Plan                  routeros.Plan
	RemainingOperationIDs []string
}
type CleanupRuntime interface {
	BindCleanup(context.Context, CleanupBinding) error
	ApplyCleanup(context.Context, routeros.APIMutation) (api.Result, error)
}

// PreparedDiscardBinding is issued only from the authenticated rolled-back
// journal. It contains original Prepare additions, never arbitrary deletion
// targets or authority to resume the cancelled activation.
type PreparedDiscardBinding struct {
	Plan                  routeros.Plan
	BaselineKnown         routeros.KnownState
	Objects               []routeros.PreparedDiscardObject
	RemainingOperationIDs []string
}
type PreparedDiscardRuntime interface {
	BindPreparedDiscard(context.Context, PreparedDiscardBinding) error
	ApplyPreparedDiscard(context.Context, routeros.APIMutation) (api.Result, error)
}

// MaxDiscardRequestBatches bounds explicitly requested work without changing deadlines.
const MaxDiscardRequestBatches = 16

// MaxCleanupRequestBatches bounds explicit post-confirm work without changing deadlines.
const MaxCleanupRequestBatches = 16

type CleanupRequest struct {
	TransactionID string
	PlanChecksum  string
	// MaxBatches counts verified removal batches or individual removals.
	// Zero keeps the existing, uncapped explicit cleanup request.
	MaxBatches int
}

type DiscardRequest struct {
	// MaxBatches counts fully verified rule batches or single-object removals.
	// Zero preserves legacy internal callers; product clients use 1..16.
	MaxBatches                  int
	TransactionID               string
	PlanChecksum                string
	ExclusiveAccessAcknowledged bool
}

type RuleRemovalMode string

const (
	RuleRemovalCleanup         RuleRemovalMode = "confirmed-cleanup"
	RuleRemovalPreparedDiscard RuleRemovalMode = "prepared-discard"
)

type RuleRemovalProofMode string

const (
	RuleRemovalProofPerObject  RuleRemovalProofMode = "per-object"
	RuleRemovalProofGlobalTail RuleRemovalProofMode = "global-tail"
)

// RuleRemovalBatch is an internal contiguous prefix of an already authorized
// removal bridge. It cannot contain roots, connections, instances or tables.
// It requires sustained exclusive writer access. Head/tail global observations
// and per-item object evidence do not atomically exclude an out-of-band writer.
type RuleRemovalBatch struct {
	ID, TransactionID, PlanChecksum, ScopeChecksum string
	Mode                                           RuleRemovalMode
	ProofMode                                      RuleRemovalProofMode
	Operations                                     []routeros.APIMutation
}
type RuleRemovalObservation struct {
	BatchID    string
	Target     routeros.Target
	Resource   api.Resource
	ID         string
	Present    bool
	Attributes api.Record
	ObservedAt time.Time
	Owner      Owner
	Protected  []ObjectObservation
}

func (RuleRemovalObservation) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS private rule-removal observation")
}
func (RuleRemovalObservation) MarshalJSON() ([]byte, error) {
	return []byte(`{"ruleRemovalObservation":"private"}`), nil
}

type RuleRemovalRecorder interface {
	Before(string, RuleRemovalObservation) error
	Result(string, api.Result, error) error
	// Global-tail batches must not call Absent. Their completed prefix is
	// established only by the controller's complete final observation.
	Absent(string, RuleRemovalObservation) error
}
type RuleRemovalBatchRuntime interface {
	ApplyRuleRemovalBatch(context.Context, RuleRemovalBatch, RuleRemovalRecorder) error
}
type Config struct {
	Dir                    string
	Target                 routeros.Target
	OperationTimeout       time.Duration
	PrepareTimeout         time.Duration
	MinConfirmationTimeout time.Duration
	MaxConfirmationTimeout time.Duration
	PollInterval           time.Duration
	ManualCleanup          bool
}
type Request struct {
	Plan                        routeros.Plan
	Credentials                 routeros.Credentials
	BaselineCredentials         routeros.Credentials
	KnownState                  routeros.KnownState
	Timeout                     time.Duration
	ExclusiveAccessAcknowledged bool
}

func (Request) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS stage request (credentials redacted)")
}
func (Request) MarshalJSON() ([]byte, error) { return []byte(`{"stageRequest":"private"}`), nil }

type Phase string

const UnconfirmedCandidateRetainedCode = "unconfirmed-candidate-retained"

const (
	Idle                 Phase = "idle"
	Preparing            Phase = "preparing"
	Prepared             Phase = "prepared"
	Taking               Phase = "taking-safe-mode"
	Activating           Phase = "activating"
	AwaitingConfirmation Phase = "awaiting-confirmation"
	Confirming           Phase = "confirming"
	Cancelling           Phase = "cancelling"
	RollbackPending      Phase = "rollback-pending"
	Uncertain            Phase = "uncertain"
	Confirmed            Phase = "confirmed"
	RolledBack           Phase = "rolled-back"
	PreparationFailed    Phase = "preparation-failed"
)

type Status struct {
	PreparedDiscard           PreparedDiscardStatus `json:"preparedDiscard"`
	Cleanup                   CleanupStatus         `json:"cleanup"`
	TransactionID             string                `json:"transactionId,omitempty"`
	RestoreOf                 string                `json:"restoreOf,omitempty"`
	PlanChecksum              string                `json:"planChecksum,omitempty"`
	Phase                     Phase                 `json:"phase"`
	StartedAt                 time.Time             `json:"startedAt,omitempty"`
	Deadline                  time.Time             `json:"deadline,omitempty"`
	SessionID                 string                `json:"sessionId,omitempty"`
	LastOperationID           string                `json:"lastOperationId,omitempty"`
	LastCode                  string                `json:"lastCode,omitempty"`
	RecoveryOnly              bool                  `json:"recoveryOnly"`
	RollbackVerified          bool                  `json:"rollbackVerified"`
	ConfirmationVerified      bool                  `json:"confirmationVerified"`
	VerifiedAt                time.Time             `json:"verifiedAt,omitempty"`
	PreparedArtifactsRetained bool                  `json:"preparedArtifactsRetained"`
}

type PreparedDiscardPhase string

const (
	PreparedDiscardNone      PreparedDiscardPhase = ""
	PreparedDiscardPending   PreparedDiscardPhase = "pending"
	PreparedDiscardRunning   PreparedDiscardPhase = "running"
	PreparedDiscardUncertain PreparedDiscardPhase = "uncertain"
	PreparedDiscardComplete  PreparedDiscardPhase = "complete"
)

type PreparedDiscardStatus struct {
	Phase           PreparedDiscardPhase `json:"phase"`
	Checksum        string               `json:"checksum,omitempty"`
	Completed       int                  `json:"completed"`
	Total           int                  `json:"total"`
	LastOperationID string               `json:"lastOperationId,omitempty"`
	LastCode        string               `json:"lastCode,omitempty"`
	UpdatedAt       time.Time            `json:"updatedAt,omitempty"`
}
type CleanupPhase string

const (
	CleanupNone      CleanupPhase = "none"
	CleanupPending   CleanupPhase = "pending"
	CleanupRunning   CleanupPhase = "running"
	CleanupUncertain CleanupPhase = "uncertain"
	CleanupComplete  CleanupPhase = "complete"
)

type CleanupStatus struct {
	Phase           CleanupPhase `json:"phase"`
	Completed       int          `json:"completed"`
	Total           int          `json:"total"`
	LastOperationID string       `json:"lastOperationId,omitempty"`
	LastCode        string       `json:"lastCode,omitempty"`
	UpdatedAt       time.Time    `json:"updatedAt,omitempty"`
}
type Failure struct{ Code string }

func (e *Failure) Error() string { return "RouterOS control: " + e.Code }
func failure(code string) error  { return &Failure{Code: code} }
