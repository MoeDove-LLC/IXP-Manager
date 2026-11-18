package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

func TestRestoreSocketRequiresBoundAndActionSpecificAuthority(t *testing.T) {
	id, original, restored := strings.Repeat("a", 32), strings.Repeat("b", 64), strings.Repeat("c", 64)
	prefix := `{"version":1,"transactionId":"` + id + `","checksum":"` + original + `"`
	review := prefix + `,"action":"restore-review"`
	stage := prefix + `,"action":"restore-stage"`
	payload := `,"restore":{"restoreChecksum":"` + restored + `","timeoutSeconds":60`
	ack := `,"exclusiveAccessAcknowledged":true`
	for _, raw := range []string{review + `}`, stage + ack + payload + `}}`} {
		var input request
		if strictDecode([]byte(raw), &input) != nil || !validRequest(input) {
			t.Fatal("valid restore authority rejected")
		}
	}
	for name, raw := range map[string]string{
		"review-with-ack":        review + ack + `}`,
		"review-with-null-stage": review + `,"stage":null}`,
		"review-with-null-body":  review + `,"restore":null}`,
		"stage-with-null-body":   stage + ack + `,"restore":null}`,
		"wrong-mode-null-body":   `{"version":1,"action":"status","restore":null}`,
		"missing-ack":            stage + payload + `}}`,
		"false-ack":              stage + `,"exclusiveAccessAcknowledged":false` + payload + `}}`,
		"null-ack":               stage + `,"exclusiveAccessAcknowledged":null` + payload + `}}`,
		"duplicate-ack":          stage + ack + ack + payload + `}}`,
		"wrong-case-object":      stage + ack + strings.Replace(payload, `"restore"`, `"Restore"`, 1) + `}}`,
		"wrong-case-checksum":    stage + ack + strings.Replace(payload, `"restoreChecksum"`, `"RestoreChecksum"`, 1) + `}}`,
		"same-checksum":          stage + ack + strings.Replace(payload, restored, original, 1) + `}}`,
		"uppercase-checksum":     stage + ack + strings.Replace(payload, restored, strings.ToUpper(restored), 1) + `}}`,
		"excess-timeout":         stage + ack + strings.Replace(payload, ":60", ":301", 1) + `}}`,
		"supplied-plan":          stage + ack + payload + `,"plan":{}}}`,
		"supplied-known-state":   stage + ack + payload + `,"knownState":{}}}`,
		"supplied-stage":         stage + ack + payload + `},"stage":null}`,
		"wrong-case-password":    stage + ack + payload + `,"credentials":{"peer":{"revision":"r","Password":"fixture"}}}}`,
		"duplicate-password":     stage + ack + payload + `,"baselineCredentials":{"peer":{"password":"x","password":"y"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var input request
			if strictDecode([]byte(raw), &input) == nil && validRequest(input) {
				t.Fatal("ambiguous or caller-selected authority accepted")
			}
		})
	}
}

func TestRestoreFlagsRejectCrossModeInputsBeforeSocket(t *testing.T) {
	id, checksum, restore := strings.Repeat("a", 32), strings.Repeat("b", 64), strings.Repeat("c", 64)
	base := []string{"--socket", "/unavailable/control.sock", "--id", id, "--checksum", checksum}
	for _, item := range []struct {
		mode  string
		extra []string
	}{
		{"restore-review", []string{"--file", "/private/credentials.json"}},
		{"restore-review", []string{"--exclusive-access-acknowledged"}},
		{"restore-stage", []string{"--output", "/private/review.json"}},
		{"restore-stage", []string{"--restore-checksum", restore}},
		{"restore-stage", []string{"--restore-checksum", restore, "--exclusive-access-acknowledged=false"}},
		{"restore-stage", []string{"--restore-checksum", restore, "--exclusive-access-acknowledged", "--timeout", "0"}},
		{"stage", []string{"--restore-checksum", restore}},
	} {
		args := append(append([]string{item.mode}, base...), item.extra...)
		err := run(context.Background(), args, io.Discard)
		if err == nil || strings.Contains(err.Error(), "socket directory") {
			t.Fatal("invalid restore CLI got past input checks", item.mode, err)
		}
	}
}

type controlSocketExchange struct {
	input request
	err   error
}

func openTestControlSocket(t *testing.T, reply response) (string, <-chan controlSocketExchange) {
	t.Helper()
	dir, err := os.MkdirTemp("", "restore-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err = listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan controlSocketExchange, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		var input request
		if err == nil {
			defer conn.Close()
			err = conn.SetDeadline(time.Now().Add(5 * time.Second))
		}
		if err == nil {
			var raw []byte
			raw, err = io.ReadAll(io.LimitReader(conn, 4097))
			if err == nil {
				err = strictDecode(raw, &input)
			}
			clear(raw)
		}
		if err == nil {
			err = json.NewEncoder(conn).Encode(reply)
		}
		done <- controlSocketExchange{input, err}
	}()
	return path, done
}

func TestRestoreCLIReviewSavesBoundPlanInPrivateFile(t *testing.T) {
	id, checksum := strings.Repeat("a", 32), strings.Repeat("b", 64)
	target := cliTarget()
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "192.0.2.1"}
	// This synthetic canonical plan exercises the response projection without a device.
	plan, err := routeros.BuildPlan(target, router, nil, routeros.Snapshot{Target: target, Owner: routeros.OwnerName(target.RouterID), NamespaceEmpty: true}, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	reply := response{
		OK: true, Status: control.Status{Phase: control.RollbackPending, TransactionID: id, PlanChecksum: checksum},
		RestoreReview: &control.RestoreReview{TransactionID: id, PlanChecksum: checksum, Plan: plan, ObservedAt: time.Now().UTC()}, RestoreChecksum: plan.Checksum,
	}
	path, done := openTestControlSocket(t, reply)
	file := filepath.Join(filepath.Dir(path), "restore.json")
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"restore-review", "--socket", path, "--id", id, "--checksum", checksum, "--output", file}, &stdout); err != nil {
		t.Fatal(err)
	}
	wire := <-done
	if wire.err != nil || wire.input.Restore != nil || wire.input.Stage != nil || wire.input.Action != "restore-review" || wire.input.TransactionID != id || wire.input.Checksum != checksum {
		t.Fatal("read-only review request broadened authority", wire.err)
	}
	var saved response
	if strictDecode(stdout.Bytes(), &saved) != nil || !saved.ReviewSaved || saved.RestoreReview != nil || saved.Status.TransactionID != id || saved.Status.PlanChecksum != checksum || saved.RestoreChecksum != plan.Checksum {
		t.Fatal("review summary did not preserve both bindings or kept the full private plan")
	}
	raw, err := privateRead(file)
	if err != nil || strictDecode(raw, &saved) != nil || saved.RestoreReview == nil || saved.RestoreReview.Plan.Checksum != plan.Checksum {
		t.Fatal("private file did not preserve the reviewed plan", err)
	}
	if !validRestoreReviewResponse(wire.input, saved) {
		t.Fatal("original read-only review failed binding checks")
	}
	saved.RestoreReview.Plan.Owner = "edited-owner"
	if validRestoreReviewResponse(wire.input, saved) {
		t.Fatal("corrupt restore plan retained a valid-looking checksum")
	}
}

func TestRestoreCLIStageSendsOnlyHashesAndPrivateCredentials(t *testing.T) {
	id, checksum, restored, newID := strings.Repeat("a", 32), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 32)
	reply := response{OK: true, Status: control.Status{Phase: control.AwaitingConfirmation, TransactionID: newID, PlanChecksum: restored, RestoreOf: id}}
	path, done := openTestControlSocket(t, reply)
	file := filepath.Join(filepath.Dir(path), "credentials.json")
	secret := "fixture-restore-secret"
	if err := os.WriteFile(file, []byte(`{"credentials":{"peer":{"revision":"reviewed","password":"`+secret+`"}},"baselineCredentials":{"peer":{"revision":"active","password":"fixture-candidate"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	args := []string{"restore-stage", "--socket", path, "--id", id, "--checksum", checksum, "--restore-checksum", restored, "--exclusive-access-acknowledged", "--file", file}
	if err := run(context.Background(), args, &stdout); err != nil {
		t.Fatal(err)
	}
	wire := <-done
	input := wire.input
	if wire.err != nil || !validRequest(input) || input.Action != "restore-stage" || input.TransactionID != id || input.Checksum != checksum || input.Stage != nil || input.Restore.RestoreChecksum != restored || input.Restore.TimeoutSeconds != 60 ||
		input.Restore.Credentials["peer"].Password != secret || input.Restore.BaselineCredentials["peer"].Revision != "active" {
		t.Fatal("restore request lost its exact bindings or added caller-selected authority", wire.err)
	}
	if !validRestoreStageResponse(input, reply) || bytes.Contains(stdout.Bytes(), []byte(secret)) {
		t.Fatal("invalid staging projection or credential leak")
	}
	reply.Status.RestoreOf = newID
	if validRestoreStageResponse(input, reply) {
		t.Fatal("pending restore transaction referenced the wrong recovery obligation")
	}
	reply.Status.RestoreOf = id
	reply.Status.TransactionID = id
	if validRestoreStageResponse(input, reply) {
		t.Fatal("old transaction reused as the pending restore transaction")
	}
}

func TestRestoreDispatchCannotCreateAuthorityFromIdle(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	runtime := &blockedRuntime{target: cliTarget()}
	supervisor, err := control.Open(control.Config{Dir: dir, Target: runtime.target}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	input := request{Version: 1, TransactionID: strings.Repeat("a", 32), Checksum: strings.Repeat("b", 64), Action: "restore-review"}
	result := dispatch(context.Background(), supervisor, input)
	if result.OK || result.Error != "restore-fresh-runtime-required" || result.Status.Phase != control.Idle || result.RestoreReview != nil {
		t.Fatal("restore-review bypassed the controller's recovery state check")
	}
	ack := true
	input.Action, input.ExclusiveAccessAcknowledged = "restore-stage", &ack
	input.Restore = &restoreWire{RestoreChecksum: strings.Repeat("c", 64), TimeoutSeconds: 60}
	result = dispatch(context.Background(), supervisor, input)
	if result.OK || result.Error != "restore-fresh-runtime-required" || result.Status.Phase != control.Idle || runtime.writes != 0 {
		t.Fatal("restore-stage bypassed the controller's recovery state check")
	}
}
