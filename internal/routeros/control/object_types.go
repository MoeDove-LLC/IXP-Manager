package control

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// ObjectReadRequest names a bound operation, not a caller-selected command or
// object. Its credential grant is derived from this supervisor's private WAL.
type ObjectReadRequest struct {
	PlanChecksum        string
	OperationID         string
	After               bool
	CredentialAuthority *CredentialAuthority
}

// ObjectObservation is actual evidence about one object. It deliberately has
// no global Inventory, Snapshot or Resolver and cannot stand in for one.
type ObjectObservation struct {
	Target                       routeros.Target
	Resource                     api.Resource
	ID                           string
	Attributes                   api.Record
	Credential                   api.Credential
	ObservedAt                   time.Time
	Owner                        Owner
	CrossChannelIdentityVerified bool
}

func (ObjectReadRequest) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS private object read request")
}
func (ObjectReadRequest) MarshalJSON() ([]byte, error) {
	return []byte(`{"objectRead":"private"}`), nil
}
func (ObjectObservation) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS private object observation")
}
func (ObjectObservation) MarshalJSON() ([]byte, error) {
	return []byte(`{"objectObservation":"private"}`), nil
}

type ObjectRuntime interface {
	BindObjectActivation(context.Context, string) error
	ObserveObject(context.Context, ObjectReadRequest) (ObjectObservation, error)
	ApplyObject(context.Context, routeros.APIMutation) (api.Result, error)
}
