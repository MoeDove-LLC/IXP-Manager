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

	"github.com/moedove/ixp-manager/internal/routeros/control"
)

func discardAck(value bool) *bool { return &value }

func TestDiscardPreparedRequestRequiresOriginalBindingAndFreshAcknowledgement(t *testing.T) {
	good := request{Version: 1, Action: "discard-prepared", TransactionID: strings.Repeat("a", 32), Checksum: strings.Repeat("b", 64), ExclusiveAccessAcknowledged: discardAck(true)}
	if !validRequest(good) {
		t.Fatal("bound, explicitly acknowledged discard request rejected")
	}
	for name, change := range map[string]func(*request){
		"missing-ack":     func(r *request) { r.ExclusiveAccessAcknowledged = nil },
		"false-ack":       func(r *request) { r.ExclusiveAccessAcknowledged = discardAck(false) },
		"missing-id":      func(r *request) { r.TransactionID = "" },
		"missing-hash":    func(r *request) { r.Checksum = "" },
		"nonhex-id":       func(r *request) { r.TransactionID = strings.Repeat("z", 32) },
		"nonhex-hash":     func(r *request) { r.Checksum = strings.Repeat("g", 64) },
		"uppercase-hash":  func(r *request) { r.Checksum = strings.Repeat("B", 64) },
		"stage-authority": func(r *request) { r.Stage = &stageWire{ExclusiveAccessAcknowledged: true} },
	} {
		t.Run(name, func(t *testing.T) {
			r := good
			change(&r)
			if validRequest(r) {
				t.Fatal("unbound or ambiguous discard authority accepted")
			}
		})
	}
	for _, action := range []string{"status", "review", "recover", "stage", "confirm", "cancel", "cleanup"} {
		r := request{Version: 1, Action: action, ExclusiveAccessAcknowledged: discardAck(false)}
		if validRequest(r) {
			t.Fatal("discard acknowledgement was accepted by another action", action)
		}
	}
}

func TestDiscardPreparedJSONRejectsDuplicateUnknownAndAmbiguousFields(t *testing.T) {
	prefix := `{"version":1,"action":"discard-prepared","transactionId":"` + strings.Repeat("a", 32) + `","checksum":"` + strings.Repeat("b", 64) + `"`
	for _, tail := range []string{
		`,"exclusiveAccessAcknowledged":true,"exclusiveAccessAcknowledged":false}`,
		`,"exclusiveAccessAcknowledged":true,"Action":"status"}`,
		`,"exclusiveAccessAcknowledged":true,"operationIds":["arbitrary"]}`,
		`,"exclusiveAccessAcknowledged":true,"command":"remove"}`,
		`,"exclusiveAccessAcknowledged":true,"target":"other-router"}`,
		`,"exclusiveAccessAcknowledged":"true"}`,
		`,"exclusiveAccessAcknowledged":true} {"version":1}`,
	} {
		var input request
		if strictDecode([]byte(prefix+tail), &input) == nil {
			t.Fatal("ambiguous or caller-selected discard input decoded")
		}
	}
	for _, tail := range []string{`}`, `,"exclusiveAccessAcknowledged":false}`, `,"exclusiveAccessAcknowledged":null}`} {
		var input request
		if strictDecode([]byte(prefix+tail), &input) == nil && validRequest(input) {
			t.Fatal("absent, false or null acknowledgement authorized discard")
		}
	}
	var input request
	if strictDecode([]byte(prefix+`,"exclusiveAccessAcknowledged":true}`), &input) != nil || !validRequest(input) {
		t.Fatal("canonical discard request was rejected")
	}
	var object map[string]any
	if strictDecode([]byte(`{"outer":{"duplicate":1,"duplicate":2}}`), &object) == nil || strictDecode([]byte(strings.Repeat("[", 66)+`0`+strings.Repeat("]", 66)), new(any)) == nil {
		t.Fatal("nested duplicate or unbounded structure was accepted")
	}
}

func TestDiscardPreparedCLIRejectsMissingOrMisplacedAcknowledgement(t *testing.T) {
	base := []string{"discard-prepared", "--socket", "/unavailable/control.sock", "--id", strings.Repeat("a", 32), "--checksum", strings.Repeat("b", 64)}
	for _, extra := range [][]string{nil, {"--exclusive-access-acknowledged=false"}, {"--exclusive-access-acknowledged", "--file", "/private/stage.json"}, {"--exclusive-access-acknowledged", "--operation-id", "caller-chosen"}} {
		if err := run(context.Background(), append(append([]string{}, base...), extra...), &bytes.Buffer{}); err == nil {
			t.Fatal("invalid discard CLI accepted")
		}
	}
	for _, action := range []string{"inspect", "serve", "status", "review", "recover", "stage", "confirm", "cancel", "cleanup"} {
		for _, ack := range []string{"--exclusive-access-acknowledged", "--exclusive-access-acknowledged=false"} {
			err := run(context.Background(), []string{action, ack}, &bytes.Buffer{})
			if err == nil || err.Error() != "only discard-prepared accepts --exclusive-access-acknowledged" {
				t.Fatal("another action consumed the new maintenance acknowledgement", action, err)
			}
		}
	}
}

func TestDiscardPreparedClientPreservesRollbackAndSeparateProgress(t *testing.T) {
	dir, err := os.MkdirTemp("", "discard-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	id, checksum := strings.Repeat("a", 32), strings.Repeat("b", 64)
	discardChecksum := strings.Repeat("c", 64)
	observed := make(chan request, 1)
	errors := make(chan error, 1)
	go func() {
		conn, e := listener.AcceptUnix()
		if e != nil {
			errors <- e
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		raw, e := io.ReadAll(io.LimitReader(conn, 4097))
		var input request
		if e == nil {
			e = strictDecode(raw, &input)
		}
		if e != nil {
			errors <- e
			return
		}
		observed <- input
		// This is a wire projection fixture, not a controller rollback proof.
		_, e = io.WriteString(conn, `{"ok":true,"status":{"phase":"rolled-back","transactionId":"`+id+`","planChecksum":"`+checksum+`","rollbackVerified":true,"preparedArtifactsRetained":false,"cleanup":{"phase":"none","completed":0,"total":0},"preparedDiscard":{"phase":"complete","checksum":"`+discardChecksum+`","completed":2,"total":2}}}`)
		errors <- e
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := run(ctx, []string{"discard-prepared", "--socket", path, "--id", id, "--checksum", checksum, "--exclusive-access-acknowledged"}, &output); err != nil {
		t.Fatal(err)
	}
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	input := <-observed
	if input.Action != "discard-prepared" || input.TransactionID != id || input.Checksum != checksum || input.Stage != nil || input.ExclusiveAccessAcknowledged == nil || !*input.ExclusiveAccessAcknowledged || input.MaxBatches == nil || *input.MaxBatches != 1 {
		t.Fatal("client changed or broadened explicit discard authority")
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result["ok"] != true {
		t.Fatal("discard response unavailable", err)
	}
	status := result["status"].(map[string]any)
	discard := status["preparedDiscard"].(map[string]any)
	cleanup := status["cleanup"].(map[string]any)
	if status["phase"] != string(control.RolledBack) || status["planChecksum"] != checksum || status["rollbackVerified"] != true || status["preparedArtifactsRetained"] != false || discard["phase"] != "complete" || discard["checksum"] != discardChecksum || discard["completed"] != float64(2) || cleanup["phase"] != "none" {
		t.Fatal("client replaced rollback evidence or mixed discard with confirmed cleanup")
	}
}

func TestDiscardPreparedDispatchCannotCreateAuthorityFromIdle(t *testing.T) {
	dir, err := os.MkdirTemp("", "discard-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	runtime := &blockedRuntime{target: cliTarget()}
	supervisor, err := control.Open(control.Config{Dir: dir, Target: runtime.target}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	input := request{Version: 1, Action: "discard-prepared", TransactionID: strings.Repeat("a", 32), Checksum: strings.Repeat("b", 64), ExclusiveAccessAcknowledged: discardAck(true)}
	result := dispatch(context.Background(), supervisor, input)
	if result.OK || result.Error == "" || result.Error == "invalid-control-request" || result.Status.Phase != control.Idle || result.Status.RollbackVerified || runtime.writes != 0 {
		t.Fatal("a client acknowledgement invented a verified rollback or reached mutation")
	}
}

func discardLimit(value int) *int { return &value }

func TestDiscardPreparedBatchLimitIsBounded(t *testing.T) {
	base := request{Version: 1, Action: "discard-prepared", TransactionID: strings.Repeat("a", 32), Checksum: strings.Repeat("b", 64), ExclusiveAccessAcknowledged: discardAck(true)}
	if discardBatchLimit(base) != 1 {
		t.Fatal("legacy product envelope bypassed default limit")
	}
	for _, n := range []int{1, 16} {
		r := base
		r.MaxBatches = discardLimit(n)
		if !validRequest(r) || discardBatchLimit(r) != n {
			t.Fatal("valid limit lost")
		}
	}
	for _, n := range []int{-1, 0, 17} {
		r := base
		r.MaxBatches = discardLimit(n)
		if validRequest(r) {
			t.Fatal("invalid product limit accepted")
		}
	}
	for _, action := range []string{"inspect", "serve", "status", "review", "recover", "stage", "confirm", "cancel"} {
		r := request{Version: 1, Action: action, MaxBatches: discardLimit(1)}
		if validRequest(r) {
			t.Fatal("limit accepted by another action")
		}
		if err := run(context.Background(), []string{action, "--max-batches", "1"}, &bytes.Buffer{}); err == nil || err.Error() != "only cleanup and discard-prepared accept --max-batches" {
			t.Fatal("misplaced CLI limit accepted", err)
		}
	}
	raw, _ := json.Marshal(base)
	prefix := strings.TrimSuffix(string(raw), "}")
	for _, value := range []string{"null", "0", "-1", "17", "1.0", "1e0", "true", `"1"`} {
		var r request
		if strictDecode([]byte(prefix+`,"maxBatches":`+value+`}`), &r) == nil {
			t.Fatal("ambiguous limit decoded", value)
		}
	}
	for _, tail := range []string{`,"maxBatches":1,"maxBatches":2}`, `,"MaxBatches":1}`} {
		if strictDecode([]byte(prefix+tail), new(request)) == nil {
			t.Fatal("duplicate or alias limit decoded")
		}
	}
}

func TestDiscardPreparedProgressMakesContinuationExplicit(t *testing.T) {
	result := response{OK: true, Status: control.Status{Phase: control.RolledBack, TransactionID: strings.Repeat("a", 32), PlanChecksum: strings.Repeat("b", 64), RollbackVerified: true, PreparedArtifactsRetained: true, PreparedDiscard: control.PreparedDiscardStatus{Phase: control.PreparedDiscardPending, Completed: 3, Total: 11, LastCode: "discard-awaiting-explicit-continuation"}}}
	annotateDiscardResponse(&result, "/private/socket with spaces", 1)
	if !strings.Contains(result.Message, "已验证 3/11") || !strings.Contains(result.Message, "尚未完成") || !strings.Contains(result.Message, "不会在后台自动继续") || len(result.ContinueCommand) != 11 || result.ContinueCommand[3] != "/private/socket with spaces" || result.ContinueCommand[10] != "1" {
		t.Fatal("partial progress omitted the explicit safe continuation", result)
	}
	result.OK = false
	result.Status.PreparedDiscard.Phase = control.PreparedDiscardUncertain
	result.ContinueCommand = nil
	annotateDiscardResponse(&result, "/private/socket", 1)
	if len(result.ContinueCommand) != 0 || !strings.Contains(result.Message, "不要重发未知操作") {
		t.Fatal("uncertain state offered write retry")
	}
}

func TestDiscardPreparedPendingSocketResponseOffersOnlyExplicitContinuation(t *testing.T) {
	dir, err := os.MkdirTemp("", "discard-pending-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	id, checksum := strings.Repeat("a", 32), strings.Repeat("b", 64)
	status := control.Status{Phase: control.RolledBack, TransactionID: id, PlanChecksum: checksum, RollbackVerified: true, PreparedArtifactsRetained: true, PreparedDiscard: control.PreparedDiscardStatus{Phase: control.PreparedDiscardPending, Completed: 7, Total: 11, LastCode: "discard-awaiting-explicit-continuation"}}
	received := make(chan request, 1)
	done := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		raw, err := io.ReadAll(io.LimitReader(conn, 4097))
		var input request
		if err == nil {
			err = strictDecode(raw, &input)
		}
		if err == nil {
			received <- input
			err = json.NewEncoder(conn).Encode(response{OK: true, Status: status})
		}
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := run(ctx, []string{"discard-prepared", "--socket", path, "--id", id, "--checksum", checksum, "--exclusive-access-acknowledged", "--max-batches", "2"}, &output); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	input := <-received
	var result response
	if json.Unmarshal(output.Bytes(), &result) != nil || input.MaxBatches == nil || *input.MaxBatches != 2 || result.Status.PreparedDiscard.Phase != control.PreparedDiscardPending || !strings.Contains(result.Message, "已验证 7/11，尚未完成") || len(result.ContinueCommand) != 11 || result.ContinueCommand[10] != "2" {
		t.Fatal("wire/client lost bounded request or truthful progress")
	}
}
