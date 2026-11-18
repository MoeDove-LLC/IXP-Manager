// Package terminal provides a bounded, single-channel RouterOS terminal probe
// and Safe Mode control session. It does not execute arbitrary configuration or
// certify a router/platform as safe to apply a plan.
package terminal

import (
	"context"
	"io"
	"time"
)

// Session must be one persistent authenticated terminal channel. Close must
// unblock concurrent Read and Write calls. Serial/CHR adapters can implement
// this interface without importing the parent routeros package.
type Session interface {
	io.Reader
	io.Writer
	io.Closer
}

type Code string

const (
	InvalidInput           Code = "invalid-input"
	TransportFailure       Code = "transport-failure"
	Deadline               Code = "deadline"
	Cancelled              Code = "cancelled"
	BoundsExceeded         Code = "bounds-exceeded"
	ProtocolUnknown        Code = "protocol-unknown"
	RemoteRejected         Code = "remote-rejected"
	AuthenticationRequired Code = "authentication-required"
	OwnerConflict          Code = "owner-conflict"
	OwnershipLost          Code = "ownership-lost"
	StateUnknown           Code = "state-unknown"
	SessionClosed          Code = "session-closed"
)

// Failure deliberately contains no remote output, command echo, path or
// underlying process error; a terminal echo may contain credentials.
type Failure struct{ Code Code }

func (f *Failure) Error() string { return "routeros terminal: " + string(f.Code) }
func failure(code Code) error    { return &Failure{Code: code} }

type Limits struct {
	OperationTimeout  time.Duration
	Lifetime          time.Duration
	MaxOperationBytes int
	MaxSessionBytes   int
	MaxLineBytes      int
	MaxLines          int
	MaxPages          int
}

func DefaultLimits() Limits {
	return Limits{OperationTimeout: 10 * time.Second, Lifetime: 2 * time.Minute, MaxOperationBytes: 64 << 10, MaxSessionBytes: 1 << 20, MaxLineBytes: 4096, MaxLines: 512, MaxPages: 8}
}
func checkedLimits(l Limits) (Limits, error) {
	if l == (Limits{}) {
		l = DefaultLimits()
	}
	if l.OperationTimeout <= 0 || l.OperationTimeout > time.Minute || l.Lifetime < l.OperationTimeout || l.Lifetime > 10*time.Minute || l.MaxOperationBytes < 1024 || l.MaxOperationBytes > 1<<20 || l.MaxSessionBytes < l.MaxOperationBytes || l.MaxSessionBytes > 16<<20 || l.MaxLineBytes < 128 || l.MaxLineBytes > 16384 || l.MaxLines < 8 || l.MaxLines > 8192 || l.MaxPages < 0 || l.MaxPages > 32 {
		return l, failure(InvalidInput)
	}
	return l, nil
}

type Mode string

const (
	Unknown        Mode = "unknown"
	Ready          Mode = "normal-prompt-observed"
	SafeObserved   Mode = "safe-prompt-observed"
	SafeOwned      Mode = "safe-mode-taken-on-this-channel"
	Lost           Mode = "safe-mode-ownership-lost"
	CancelPending  Mode = "cancel-requested-unverified"
	ConfirmPending Mode = "release-requested-unverified"
	Released       Mode = "safe-mode-release-observed"
)

type Owner string

const (
	OwnerUnknown     Owner = "unknown"
	OwnerNone        Owner = "none-reported"
	OwnerThisSession Owner = "current-session-reported"
	OwnerOther       Owner = "other-session-reported"
)

type Evidence struct {
	Mode                Mode      `json:"mode"`
	Owner               Owner     `json:"owner"`
	OwnerQueryAvailable bool      `json:"ownerQueryAvailable"`
	OwnerFingerprint    string    `json:"ownerFingerprint,omitempty"`
	UserFingerprint     string    `json:"userFingerprint,omitempty"`
	ObservedAt          time.Time `json:"observedAt"`
	Closed              bool      `json:"closed"`
	NativeValidated     bool      `json:"nativeValidated"`  // Always false in this foundation package.
	RollbackVerified    bool      `json:"rollbackVerified"` // A terminal close is not readback evidence.
	CommitVerified      bool      `json:"commitVerified"`   // A release is not configuration readback evidence.
}

type Query uint8

const (
	Probe Query = iota
	Version
	ActiveUsers
	History
	SafeModeOwner
	LicenseSystemID
	RouterBoardSerial
	LicenseSoftwareID
	FloatingUndoCount
)

type Result struct {
	Query   Query  `json:"query"`
	Version string `json:"version,omitempty"`
	// IdentifierFingerprint is SHA-256 of one strictly parsed identifier.
	// Its Query specifies the source; raw identifiers/inventory are not returned.
	IdentifierFingerprint string   `json:"identifierFingerprint,omitempty"`
	ObservedLines         int      `json:"observedLines"`
	HistoryActions        int      `json:"historyActions"`
	HistoryKnown          bool     `json:"historyKnown"`
	Evidence              Evidence `json:"evidence"`
}
type Cancellation struct {
	Requested        bool     `json:"requested"`
	SessionEnded     bool     `json:"sessionEnded"`
	RollbackVerified bool     `json:"rollbackVerified"`
	Evidence         Evidence `json:"evidence"`
}

// OwnedReceipt is command completion evidence only. Independent typed API
// readback remains mandatory before claiming a configuration or rollback result.
type OwnedReceipt struct {
	Completed bool     `json:"completed"`
	Evidence  Evidence `json:"evidence"`
}

type Confirmation struct {
	Requested      bool     `json:"requested"`
	Released       bool     `json:"released"`
	CommitVerified bool     `json:"commitVerified"`
	Evidence       Evidence `json:"evidence"`
}

func contextFailure(ctx context.Context) error {
	if ctx.Err() == context.DeadlineExceeded {
		return failure(Deadline)
	}
	return failure(Cancelled)
}
