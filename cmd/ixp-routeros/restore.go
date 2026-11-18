package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

type restoreWire struct {
	RestoreChecksum     string                    `json:"restoreChecksum"`
	TimeoutSeconds      int                       `json:"timeoutSeconds"`
	Credentials         map[string]credentialWire `json:"credentials,omitempty"`
	BaselineCredentials map[string]credentialWire `json:"baselineCredentials,omitempty"`
}

func (restoreWire) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "private restore request(redacted)")
}

func (r *restoreWire) UnmarshalJSON(raw []byte) error {
	fields, ok := handoffObject(raw, "restoreChecksum", "timeoutSeconds", "credentials", "baselineCredentials")
	if !ok || validateCredentialMaps(fields) != nil {
		return errors.New("invalid restore payload fields")
	}
	type wire restoreWire
	return json.Unmarshal(raw, (*wire)(r))
}

// Reject explicit nulls for action-specific inputs that the typed decoder
// would otherwise treat as absent. Ordinary actions keep their old envelope.
func validRestoreEnvelope(r request, fields map[string]bool) bool {
	switch r.Action {
	case "restore-review":
		return !fields["stage"] && !fields["restore"] && !fields["exclusiveAccessAcknowledged"] && !fields["maxBatches"]
	case "restore-stage":
		return !fields["stage"] && !fields["maxBatches"] && r.Restore != nil
	default:
		return !fields["restore"]
	}
}

func validRestoreReviewResponse(input request, result response) bool {
	review := result.RestoreReview
	return review != nil && result.Review == nil && result.Status.TransactionID == input.TransactionID && result.Status.PlanChecksum == input.Checksum &&
		review.TransactionID == input.TransactionID && review.PlanChecksum == input.Checksum && !review.ObservedAt.IsZero() &&
		canonicalHex(result.RestoreChecksum, 64) && result.RestoreChecksum != input.Checksum && result.RestoreChecksum == review.Plan.Checksum &&
		routeros.ValidatePlan(review.Plan) == nil
}

func validRestoreStageResponse(input request, result response) bool {
	return result.Status.Phase == control.AwaitingConfirmation && canonicalHex(result.Status.TransactionID, 32) &&
		result.Status.TransactionID != input.TransactionID && result.Status.RestoreOf == input.TransactionID &&
		result.Status.PlanChecksum == input.Restore.RestoreChecksum &&
		!result.Status.ConfirmationVerified && !result.Status.RollbackVerified
}

func restoreCommand(ctx context.Context, args []string, out io.Writer) error {
	mode := args[0]
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "private supervisor socket")
	id := flags.String("id", "", "transaction ID reported by the supervisor")
	checksum := flags.String("checksum", "", "exact plan checksum reported for the transaction")
	var outputFile, credentialFile string
	var exclusiveAccess bool
	var restore restoreWire
	if mode == "restore-review" {
		flags.StringVar(&outputFile, "output", "", "new private restore review file")
	} else {
		flags.StringVar(&restore.RestoreChecksum, "restore-checksum", "", "exact checksum returned by restore-review")
		flags.IntVar(&restore.TimeoutSeconds, "timeout", 60, "confirmation window in seconds")
		flags.BoolVar(&exclusiveAccess, "exclusive-access-acknowledged", false, "acknowledge the exclusive recovery maintenance window")
		flags.StringVar(&credentialFile, "file", "", "private restore credential bundle")
	}
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *socket == "" {
		return errors.New("invalid restore flags or missing supervisor socket")
	}
	input := request{Version: 1, Action: mode, TransactionID: *id, Checksum: *checksum}
	if mode == "restore-stage" {
		input.Restore = &restore
		input.ExclusiveAccessAcknowledged = &exclusiveAccess
	}
	if !validRequest(input) {
		return errors.New("restore requires the current transaction ID and checksum, and staging requires a distinct restore checksum, an exclusive window and a timeout of 10..300 seconds")
	}
	if input.Restore != nil {
		bundle, err := readHandoffCredentials(credentialFile)
		if err != nil {
			return err
		}
		restore.Credentials = bundle.Credentials
		restore.BaselineCredentials = bundle.BaselineCredentials
	}
	return socketCommand(ctx, *socket, input, outputFile, out)
}
