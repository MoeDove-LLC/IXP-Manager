package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/live"
)

func cleanupFixtureRequest() request {
	return request{Version: 1, Action: "cleanup", TransactionID: strings.Repeat("a", 32), Checksum: strings.Repeat("b", 64)}
}

func TestCleanupLimitEnvelopeRejectsInvalidAndPreservesOmission(t *testing.T) {
	legacy := cleanupFixtureRequest()
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var omitted request
	if strictDecode(raw, &omitted) != nil || !validRequest(omitted) || omitted.MaxBatches != nil {
		t.Fatal("legacy cleanup envelope gained a batch cap")
	}
	prefix := strings.TrimSuffix(string(raw), "}")
	for _, value := range []string{"1", "16"} {
		var input request
		if strictDecode([]byte(prefix+`,"maxBatches":`+value+`}`), &input) != nil || !validRequest(input) || input.MaxBatches == nil {
			t.Fatal("valid bounded cleanup rejected", value)
		}
	}
	for _, value := range []string{"null", "0", "-1", "17", "1.0", "1e0", "true", `"1"`} {
		if strictDecode([]byte(prefix+`,"maxBatches":`+value+`}`), new(request)) == nil {
			t.Fatal("ambiguous or out-of-bound cleanup limit decoded", value)
		}
	}
	for _, fields := range []string{`,"maxBatches":1,"maxBatches":2}`, `,"MaxBatches":1}`, `,"manualCleanup":true}`} {
		if strictDecode([]byte(prefix+fields), new(request)) == nil {
			t.Fatal("duplicate, alias or private supervisor policy accepted in the socket request", fields)
		}
	}
	invalid := legacy
	invalid.TransactionID = strings.Repeat("A", 32)
	if !validRequest(invalid) {
		t.Fatal("legacy cleanup identifier handling changed")
	}
	limit := 2
	invalid.MaxBatches = &limit
	if validRequest(invalid) {
		t.Fatal("a bounded request accepted a noncanonical transaction ID")
	}
	args := []string{"cleanup", "--socket", "/unavailable/control.sock", "--id", legacy.TransactionID, "--checksum", legacy.Checksum, "--max-batches"}
	for _, value := range []string{"-1", "0", "17"} {
		if err := run(context.Background(), append(append([]string{}, args...), value), io.Discard); err == nil || err.Error() != "cleanup max-batches must be between 1 and 16" {
			t.Fatal("invalid cleanup flag got past validation", value, err)
		}
	}
}

func confirmedPendingCleanup(input request) response {
	return response{OK: true, Status: control.Status{
		Phase: control.Confirmed, ConfirmationVerified: true, TransactionID: input.TransactionID, PlanChecksum: input.Checksum,
		Cleanup: control.CleanupStatus{Phase: control.CleanupPending, Completed: 7, Total: 11, LastCode: cleanupAwaitingContinuation},
	}}
}

func TestCleanupReceiptOffersContinuationOnlyAtVerifiedBoundedStop(t *testing.T) {
	input := cleanupFixtureRequest()
	limit := 2
	input.MaxBatches = &limit
	socket := "/private/socket with spaces"
	result := confirmedPendingCleanup(input)
	annotateCleanupResponse(&result, socket, input)
	want := []string{"ixp-routeros", "cleanup", "--socket", socket, "--id", input.TransactionID, "--checksum", input.Checksum, "--max-batches", "2"}
	if !slices.Equal(result.ContinueCommand, want) || !strings.Contains(result.Message, "已验证 7/11") {
		t.Fatal("safe bounded stop omitted its exact continuation command", result)
	}
	for _, change := range []func(*response){
		func(result *response) { result.OK = false },
		func(result *response) { result.Error = "cleanup-outcome-unresolved" },
		func(result *response) { result.Status.ConfirmationVerified = false },
		func(result *response) { result.Status.RollbackVerified = true },
		func(result *response) { result.Status.Phase = control.Uncertain },
		func(result *response) { result.Status.PlanChecksum = strings.Repeat("e", 64) },
		func(result *response) { result.Status.TransactionID = strings.Repeat("f", 32) },
		func(result *response) { result.Status.Cleanup.Phase = control.CleanupUncertain },
		func(result *response) { result.Status.Cleanup.Phase = control.CleanupRunning },
		func(result *response) { result.Status.Cleanup.Completed = 1 },
		func(result *response) { result.Status.Cleanup.Completed = result.Status.Cleanup.Total },
		func(result *response) { result.Status.Cleanup.LastCode = "cleanup-readback-unverified" },
	} {
		result := confirmedPendingCleanup(input)
		result.ContinueCommand = []string{"unverified-command"}
		change(&result)
		annotateCleanupResponse(&result, socket, input)
		if len(result.ContinueCommand) != 0 || result.Message != "" {
			t.Fatal("ambiguous result offered a write continuation", result)
		}
	}
	for _, action := range []string{"confirm", "status"} {
		result := confirmedPendingCleanup(input)
		result.Status.Cleanup.Completed = 0
		probe := input
		probe.Action, probe.MaxBatches = action, nil
		annotateCleanupResponse(&result, socket, probe)
		if len(result.ContinueCommand) != 0 || !strings.Contains(result.Message, "已验证 0/11") {
			t.Fatal("manual-cleanup status lacked a hint or invented a limit", result)
		}
	}
}

func TestCleanupCLIForwardsOnlyExplicitBatchLimits(t *testing.T) {
	for _, test := range []struct {
		name    string
		bounded bool
		refused bool
	}{
		{name: "legacy"},
		{name: "bounded", bounded: true},
		{name: "refused", bounded: true, refused: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := cleanupFixtureRequest()
			reply := confirmedPendingCleanup(input)
			if test.refused {
				reply.OK = false
				reply.Error = "cleanup-native-gate-unverified"
				reply.ContinueCommand = []string{"stale-server-hint"}
			}
			path, done := openTestControlSocket(t, reply)
			args := []string{"cleanup", "--socket", path, "--id", input.TransactionID, "--checksum", input.Checksum}
			if test.bounded {
				args = append(args, "--max-batches", "2")
			}
			var output bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := run(ctx, args, &output)
			if (err != nil) != test.refused {
				t.Fatal("unexpected cleanup exit", err, output.String())
			}
			wire := <-done
			if wire.err != nil || !validRequest(wire.input) || wire.input.Action != input.Action || wire.input.TransactionID != input.TransactionID || wire.input.Checksum != input.Checksum {
				t.Fatal("wire lost the original transaction binding", wire.err)
			}
			if (wire.input.MaxBatches != nil) != test.bounded || test.bounded && *wire.input.MaxBatches != 2 {
				t.Fatal("wire sent an implicit limit or lost the explicit one")
			}
			var result response
			if json.Unmarshal(output.Bytes(), &result) != nil || result.OK == test.refused || result.Error != reply.Error || result.Status.Cleanup != reply.Status.Cleanup {
				t.Fatal("client changed the authoritative receipt")
			}
			if (len(result.ContinueCommand) != 0) != (test.bounded && !test.refused) {
				t.Fatal("client offered continuation from the wrong receipt", result)
			}
		})
	}
}

func TestCleanupDispatchKeepsAuthorizationGateWithOrWithoutLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	runtime := &blockedRuntime{target: cliTarget()}
	supervisor, err := openSupervisor(live.Config{Supervisor: live.SupervisorConfig{ManualCleanup: true}}, dir, runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	limit := 2
	for _, cap := range []*int{nil, &limit} {
		input := cleanupFixtureRequest()
		input.MaxBatches = cap
		result := dispatch(context.Background(), supervisor, input)
		if result.OK || result.Error != "cleanup-not-authorized" || result.Status.Phase != control.Idle || runtime.writes != 0 || len(result.ContinueCommand) != 0 {
			t.Fatal("cleanup bypassed the controller's confirmation gate", result)
		}
	}
}
