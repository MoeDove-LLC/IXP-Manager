// ixp-routeros exposes a fixed-target private local supervisor. Native gates
// remain owned by the live adapter; no command-line flag can override them.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/live"
)

const maxRequestBytes = 32 << 20

type credentialWire struct {
	Revision string `json:"revision"`
	Password string `json:"password"`
}

func (credentialWire) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "credential(redacted)") }

type stageWire struct {
	Plan                        routeros.Plan             `json:"plan"`
	KnownState                  routeros.KnownState       `json:"knownState"`
	Credentials                 map[string]credentialWire `json:"credentials"`
	BaselineCredentials         map[string]credentialWire `json:"baselineCredentials"`
	TimeoutSeconds              int                       `json:"timeoutSeconds"`
	ExclusiveAccessAcknowledged bool                      `json:"exclusiveAccessAcknowledged"`
}

func (stageWire) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "private stage request(redacted)")
}

type request struct {
	Version                     int          `json:"version"`
	Action                      string       `json:"action"`
	Stage                       *stageWire   `json:"stage,omitempty"`
	Restore                     *restoreWire `json:"restore,omitempty"`
	TransactionID               string       `json:"transactionId,omitempty"`
	Checksum                    string       `json:"checksum,omitempty"`
	ExclusiveAccessAcknowledged *bool        `json:"exclusiveAccessAcknowledged,omitempty"`
	MaxBatches                  *int         `json:"maxBatches,omitempty"`
}

func (request) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "private control request(redacted)")
}

type response struct {
	OK              bool                   `json:"ok"`
	Status          control.Status         `json:"status"`
	Error           string                 `json:"error,omitempty"`
	Review          *control.Review        `json:"review,omitempty"`
	RestoreReview   *control.RestoreReview `json:"restoreReview,omitempty"`
	RestoreChecksum string                 `json:"restoreChecksum,omitempty"`
	ReviewSaved     bool                   `json:"reviewSaved,omitempty"`
	Message         string                 `json:"message,omitempty"`
	ContinueCommand []string               `json:"continueCommand,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func strictDecode(data []byte, dst any) error {
	controlRequest, ok := dst.(*request)
	var fields map[string]bool
	if ok {
		fields = map[string]bool{}
	}
	if !uniqueControlJSON(data, fields) {
		return errors.New("invalid bounded control JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid bounded control JSON")
	}
	if controlRequest != nil && !validRestoreEnvelope(*controlRequest, fields) {
		return errors.New("invalid bounded control JSON")
	}
	return nil
}

// A second decoder performs the typed check. This pass rejects duplicate keys,
// excessive nesting and differently-cased aliases of the socket envelope keys.
// Dynamic credential-map keys remain case-sensitive and are not folded.
func uniqueControlJSON(data []byte, controlFields map[string]bool) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return true
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if controlFields != nil && depth == 0 {
					switch name {
					case "version", "action", "stage", "restore", "transactionId", "checksum", "exclusiveAccessAcknowledged", "maxBatches":
						controlFields[name] = true
					default:
						return false
					}
				}
				if controlFields != nil && depth == 0 && name == "maxBatches" {
					token, err := d.Token()
					number, ok := token.(json.Number)
					if err != nil || !ok {
						return false
					}
					count, err := strconv.Atoi(string(number))
					if err != nil || count < 1 || count > max(control.MaxDiscardRequestBatches, control.MaxCleanupRequestBatches) {
						return false
					}
					continue
				}
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func canonicalHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func privateRead(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("private input requires an absolute clean path")
	}
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() < 1 || before.Size() > maxRequestBytes {
		return nil, errors.New("input must be a private regular file within32MiB")
	}
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, errors.New("private input unavailable")
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(before, opened) {
		return nil, errors.New("private input changed")
	}
	st, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return nil, errors.New("private input ownership invalid")
	}
	data, e := io.ReadAll(io.LimitReader(f, maxRequestBytes+1))
	after, se := f.Stat()
	if e != nil || se != nil || len(data) > maxRequestBytes || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		clear(data)
		return nil, errors.New("private input read changed or exceeded bound")
	}
	return data, nil
}
func credentials(wire map[string]credentialWire) routeros.Credentials {
	out := routeros.Credentials{}
	for id, value := range wire {
		out[id] = routeros.Credential{Revision: value.Revision, Password: value.Password}
	}
	return out
}
func validRequest(r request) bool {
	if r.Version != 1 {
		return false
	}
	if r.MaxBatches != nil && (*r.MaxBatches < 1 || *r.MaxBatches > requestBatchLimit(r.Action)) {
		return false
	}
	if r.Action != "restore-stage" && r.Restore != nil ||
		r.Action != "discard-prepared" && r.Action != "restore-stage" && r.ExclusiveAccessAcknowledged != nil {
		return false
	}
	switch r.Action {
	case "status", "recover", "review":
		return r.Stage == nil && r.TransactionID == "" && r.Checksum == ""
	case "stage":
		return r.Stage != nil && r.TransactionID == "" && r.Checksum == "" && r.Stage.TimeoutSeconds >= 10 && r.Stage.TimeoutSeconds <= 300
	case "confirm", "cleanup":
		return r.Stage == nil && len(r.TransactionID) == 32 && len(r.Checksum) == 64 &&
			(r.MaxBatches == nil || canonicalHex(r.TransactionID, 32) && canonicalHex(r.Checksum, 64))
	case "cancel":
		return r.Stage == nil && len(r.TransactionID) == 32 && r.Checksum == ""
	case "restore-review":
		return r.Stage == nil && canonicalHex(r.TransactionID, 32) && canonicalHex(r.Checksum, 64)
	case "restore-stage":
		return r.Stage == nil && r.Restore != nil && canonicalHex(r.TransactionID, 32) && canonicalHex(r.Checksum, 64) &&
			canonicalHex(r.Restore.RestoreChecksum, 64) && r.Restore.RestoreChecksum != r.Checksum &&
			r.Restore.TimeoutSeconds >= 10 && r.Restore.TimeoutSeconds <= 300 && r.ExclusiveAccessAcknowledged != nil && *r.ExclusiveAccessAcknowledged
	case "discard-prepared":
		return r.Stage == nil && canonicalHex(r.TransactionID, 32) && canonicalHex(r.Checksum, 64) && r.ExclusiveAccessAcknowledged != nil && *r.ExclusiveAccessAcknowledged
	}
	return false
}
func dispatch(ctx context.Context, supervisor *control.Supervisor, r request) response {
	if !validRequest(r) {
		return response{Status: supervisor.Status(), Error: "invalid-control-request"}
	}
	var status control.Status
	var err error
	var review *control.Review
	var restoreReview *control.RestoreReview
	switch r.Action {
	case "status":
		status = supervisor.Status()
	case "recover":
		status, err = supervisor.Recover(ctx)
	case "review":
		var observed control.Review
		observed, err = supervisor.Review(ctx)
		status = supervisor.Status()
		if err == nil {
			review = &observed
		}
	case "stage":
		s := r.Stage
		status, err = supervisor.Stage(ctx, control.Request{Plan: s.Plan, KnownState: s.KnownState, Credentials: credentials(s.Credentials), BaselineCredentials: credentials(s.BaselineCredentials), Timeout: time.Duration(s.TimeoutSeconds) * time.Second, ExclusiveAccessAcknowledged: s.ExclusiveAccessAcknowledged})
	case "restore-review":
		var observed control.RestoreReview
		observed, err = supervisor.ReviewRestore(ctx, r.TransactionID, r.Checksum)
		status = supervisor.Status()
		if err == nil {
			restoreReview = &observed
		}
	case "restore-stage":
		status, err = supervisor.StageRestore(ctx, control.RestoreRequest{
			TransactionID: r.TransactionID, PlanChecksum: r.Checksum, RestoreChecksum: r.Restore.RestoreChecksum,
			Credentials: credentials(r.Restore.Credentials), BaselineCredentials: credentials(r.Restore.BaselineCredentials),
			Timeout: time.Duration(r.Restore.TimeoutSeconds) * time.Second, ExclusiveAccessAcknowledged: *r.ExclusiveAccessAcknowledged,
		})
	case "confirm":
		status, err = supervisor.Confirm(ctx, r.TransactionID, r.Checksum)
	case "cleanup":
		if r.MaxBatches == nil {
			status, err = supervisor.Cleanup(ctx, r.TransactionID, r.Checksum)
		} else {
			status, err = supervisor.CleanupBounded(ctx, control.CleanupRequest{
				TransactionID: r.TransactionID, PlanChecksum: r.Checksum, MaxBatches: *r.MaxBatches,
			})
		}
	case "cancel":
		status, err = supervisor.Cancel(ctx, r.TransactionID)
	case "discard-prepared":
		status, err = supervisor.DiscardPrepared(ctx, control.DiscardRequest{TransactionID: r.TransactionID, PlanChecksum: r.Checksum, ExclusiveAccessAcknowledged: *r.ExclusiveAccessAcknowledged, MaxBatches: discardBatchLimit(r)})
	}
	out := response{OK: err == nil, Status: status, Review: review, RestoreReview: restoreReview}
	if restoreReview != nil {
		out.RestoreChecksum = restoreReview.Plan.Checksum
	}
	if err != nil {
		out.Error = "control-request-failed"
		var failure *control.Failure
		if errors.As(err, &failure) {
			out.Error = failure.Code
		}
	}
	if r.Action == "discard-prepared" {
		annotateDiscardResponse(&out, "", discardBatchLimit(r))
	} else if status.LastCode == control.UnconfirmedCandidateRetainedCode || out.Error == control.UnconfirmedCandidateRetainedCode {
		out.Message = "最近一次只读检查仍看到未确认的候选配置。原 Safe Mode 会话已释放，请先在独占维护窗口核对设备和原基线。"
	}
	annotateCleanupResponse(&out, "", r)
	return out
}

// Legacy socket envelopes default to one verified work unit. Zero is never a
// product request for unbounded work, and this does not alter any time budget.
func discardBatchLimit(r request) int {
	if r.MaxBatches == nil {
		return 1
	}
	return *r.MaxBatches
}

func annotateDiscardResponse(result *response, socket string, maxBatches int) {
	result.ContinueCommand = nil
	result.Message = ""
	p := result.Status.PreparedDiscard
	if p.Total < 1 || p.Completed < 0 || p.Completed > p.Total {
		return
	}
	if result.OK && p.Phase == control.PreparedDiscardComplete && p.Completed == p.Total {
		result.Message = fmt.Sprintf("已验证 %d/%d，准备物清理已完成。", p.Completed, p.Total)
		return
	}
	if result.OK && p.Phase == control.PreparedDiscardPending && p.Completed < p.Total && p.LastCode == "discard-awaiting-explicit-continuation" && result.Status.RollbackVerified && result.Status.PreparedArtifactsRetained {
		result.Message = fmt.Sprintf("已验证 %d/%d，尚未完成；不会在后台自动继续。再次确认独占维护窗口后，运行 continueCommand 中的命令继续。", p.Completed, p.Total)
		if socket != "" && canonicalHex(result.Status.TransactionID, 32) && canonicalHex(result.Status.PlanChecksum, 64) && maxBatches >= 1 && maxBatches <= control.MaxDiscardRequestBatches {
			result.ContinueCommand = []string{"ixp-routeros", "discard-prepared", "--socket", socket, "--id", result.Status.TransactionID, "--checksum", result.Status.PlanChecksum, "--exclusive-access-acknowledged", "--max-batches", strconv.Itoa(maxBatches)}
		}
	} else if !result.OK || p.Phase == control.PreparedDiscardUncertain {
		result.Message = fmt.Sprintf("已验证 %d/%d，尚有结果未核实；先检查状态，并在新的连接上只读 recover，不要重发未知操作。", p.Completed, p.Total)
	}
}

func unixPeerAllowed(conn *net.UnixConn) bool {
	raw, e := conn.SyscallConn()
	if e != nil {
		return false
	}
	allowed := false
	if raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		allowed = err == nil && cred.Uid == uint32(os.Geteuid())
	}) != nil {
		return false
	}
	return allowed
}
func socketPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 100 || strings.ContainsAny(path, "\x00\r\n") {
		return errors.New("socket requires a bounded absolute clean path")
	}
	resolved, e := filepath.EvalSymlinks(filepath.Dir(path))
	if e != nil || resolved != filepath.Dir(path) {
		return errors.New("socket directory must exist without symlinks")
	}
	info, e := os.Lstat(resolved)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("socket directory must be private0700")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		return errors.New("socket directory owner mismatch")
	}
	return nil
}
func serveSocket(ctx context.Context, path string, supervisor *control.Supervisor) error {
	ctx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	if err := socketPath(path); err != nil {
		return err
	}
	if info, e := os.Lstat(path); e == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if info.Mode()&os.ModeSocket == 0 || !ok || st.Uid != uint32(os.Geteuid()) {
			return errors.New("existing socket path is not an owned socket")
		}
		// The supervisor already holds this directory's exclusive journal lock.
		if e = os.Remove(path); e != nil {
			return errors.New("stale private socket unavailable")
		}
	} else if !os.IsNotExist(e) {
		return errors.New("socket path unavailable")
	}
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		return errors.New("private socket could not be opened")
	}
	if e = os.Chmod(path, 0600); e != nil {
		listener.Close()
		return errors.New("private socket permissions failed")
	}
	var wg sync.WaitGroup
	defer func() { stopServer(); _ = listener.Close(); wg.Wait() }()
	slots := make(chan struct{}, 8)
	go func() { <-ctx.Done(); _ = listener.Close() }()
	for {
		conn, e := listener.AcceptUnix()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("private socket accept failed")
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer conn.Close()
			if !unixPeerAllowed(conn) {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			data, e := io.ReadAll(io.LimitReader(conn, maxRequestBytes+1))
			if e != nil || len(data) > maxRequestBytes {
				return
			}
			defer clear(data)
			var input request
			if strictDecode(data, &input) != nil {
				return
			}
			call, cancel := context.WithTimeout(ctx, 16*time.Minute)
			defer cancel()
			output := dispatch(call, supervisor, input)
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_ = json.NewEncoder(conn).Encode(output)
		}()
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ixp-routeros inspect|serve|status|review|plan|stage|confirm|cancel|recover|restore-review|restore-stage|cleanup|discard-prepared|capabilities [flags]")
	}
	mode := args[0]
	if mode == "plan" {
		return planCommand(args[1:], out)
	}
	if mode == "restore-review" || mode == "restore-stage" {
		return restoreCommand(ctx, args, out)
	}
	if mode == "capabilities" {
		if len(args) != 1 {
			return errors.New("capabilities accepts no flags")
		}
		return json.NewEncoder(out).Encode(live.CapabilityProfile())
	}
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "private fixed-target live configuration")
	state := flags.String("state", "", "private local state directory")
	socket := flags.String("socket", "", "private supervisor socket")
	file := flags.String("file", "", "private stage request JSON")
	id := flags.String("id", "", "transaction ID")
	checksum := flags.String("checksum", "", "exact plan checksum")
	knownFile := flags.String("known", "", "private local known manifest for inspection")
	baselineFile := flags.String("baseline-credentials", "", "private expected baseline credentials for inspection")
	outputFile := flags.String("output", "", "new private review response file")
	maxBatches := flags.Int("max-batches", 1, "limit verified cleanup or discard batches and single removals to 1..16; discard defaults to 1, cleanup processes the remainder when omitted")
	exclusiveAccess := flags.Bool("exclusive-access-acknowledged", false, "acknowledge a new exclusive maintenance window for discarding verified prepared artifacts")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return errors.New("invalid command flags")
	}
	exclusiveAccessSupplied, maxBatchesSupplied := false, false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "max-batches" {
			maxBatchesSupplied = true
		}
		if f.Name == "exclusive-access-acknowledged" {
			exclusiveAccessSupplied = true
		}
	})
	if maxBatchesSupplied && requestBatchLimit(mode) == 0 {
		return errors.New("only cleanup and discard-prepared accept --max-batches")
	}
	if maxBatchesSupplied && (*maxBatches < 1 || *maxBatches > requestBatchLimit(mode)) {
		return fmt.Errorf("%s max-batches must be between 1 and %d", strings.TrimSuffix(mode, "-prepared"), requestBatchLimit(mode))
	}
	if exclusiveAccessSupplied && mode != "discard-prepared" {
		return errors.New("only discard-prepared accepts --exclusive-access-acknowledged")
	}
	if *outputFile != "" && mode != "review" {
		return errors.New("only review accepts this output option")
	}
	if mode == "inspect" {
		if *config == "" || *state != "" || *socket != "" || *file != "" || *id != "" || *checksum != "" {
			return errors.New("inspect requires --config and optional private --known/--baseline-credentials")
		}
		cfg, e := live.LoadConfig(*config)
		if e != nil {
			return errors.New("private live configuration rejected")
		}
		runtime, e := live.Open(ctx, cfg)
		if e != nil {
			return errors.New("fixed target inspection unavailable")
		}
		defer runtime.Close()
		known := routeros.KnownState{Target: runtime.Binding().Target}
		if *knownFile != "" {
			raw, e := privateRead(*knownFile)
			if e != nil {
				return e
			}
			e = strictDecode(raw, &known)
			clear(raw)
			if e != nil {
				return errors.New("private known manifest rejected")
			}
		}
		baseline := map[string]credentialWire{}
		if *baselineFile != "" {
			raw, e := privateRead(*baselineFile)
			if e != nil {
				return e
			}
			e = strictDecode(raw, &baseline)
			clear(raw)
			if e != nil {
				return errors.New("private expected credentials rejected")
			}
		}
		observed, e := runtime.Observe(ctx, control.ReadRequest{KnownState: known, BaselineCredentials: credentials(baseline), Strict: true})
		if e != nil {
			_ = json.NewEncoder(out).Encode(runtime.Status())
			return errors.New("strict target inspection incomplete; no write was requested")
		}
		return json.NewEncoder(out).Encode(struct {
			Status   live.Status        `json:"status"`
			Snapshot *routeros.Snapshot `json:"snapshot"`
		}{runtime.Status(), observed.Snapshot})
	}
	if *knownFile != "" || *baselineFile != "" {
		return errors.New("inspection inputs are not accepted by this action")
	}
	if mode == "serve" {
		if *config == "" || *state == "" || *socket != "" || *file != "" || *id != "" || *checksum != "" {
			return errors.New("serve requires only --config and --state")
		}
		cfg, e := live.LoadConfig(*config)
		if e != nil {
			return errors.New("private live configuration rejected")
		}
		runtime, e := live.Open(ctx, cfg)
		if e != nil {
			return errors.New("fixed target connection unavailable")
		}
		defer runtime.Close()
		supervisor, e := openSupervisor(cfg, *state, runtime)
		if e != nil {
			return errors.New("private supervisor state unavailable")
		}
		defer supervisor.Close()
		return serveSocket(ctx, filepath.Join(*state, "control.sock"), supervisor)
	}
	if *config != "" || *state != "" || *socket == "" {
		return errors.New("client actions require only a fixed --socket and action-specific input")
	}
	input := request{Version: 1, Action: mode, TransactionID: *id, Checksum: *checksum}
	if mode == "discard-prepared" || (mode == "cleanup" && maxBatchesSupplied) {
		input.MaxBatches = maxBatches
	}
	if exclusiveAccessSupplied {
		input.ExclusiveAccessAcknowledged = exclusiveAccess
	}
	if mode == "stage" {
		if *file == "" {
			return errors.New("stage requires a private --file")
		}
		data, e := privateRead(*file)
		if e != nil {
			return e
		}
		defer clear(data)
		input.Stage = &stageWire{}
		if strictDecode(data, input.Stage) != nil {
			return errors.New("private stage input rejected")
		}
	} else if *file != "" {
		return errors.New("only stage accepts --file")
	}
	return socketCommand(ctx, *socket, input, *outputFile, out)
}

func socketCommand(ctx context.Context, socket string, input request, outputFile string, out io.Writer) error {
	if !validRequest(input) {
		return errors.New("invalid action or identifiers")
	}
	if e := socketPath(socket); e != nil {
		return e
	}
	info, e := os.Lstat(socket)
	if e != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return errors.New("private socket unavailable")
	}
	conn, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if e != nil {
		return errors.New("supervisor unavailable")
	}
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	if !unixPeerAllowed(conn) {
		return errors.New("supervisor peer owner mismatch")
	}
	deadline := time.Now().Add(16 * time.Minute)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if json.NewEncoder(conn).Encode(input) != nil || conn.CloseWrite() != nil {
		return errors.New("request outcome unknown; inspect status, do not retry writes")
	}
	responseLimit := 1 << 20
	if input.Action == "restore-review" {
		responseLimit += routeros.MaxPlanBytes
	}
	data, e := io.ReadAll(io.LimitReader(conn, int64(responseLimit)+1))
	if e != nil || len(data) > responseLimit {
		return errors.New("response unavailable; inspect status, do not retry writes")
	}
	var result response
	if strictDecode(data, &result) != nil {
		return errors.New("response invalid; inspect status before any further action")
	}
	if input.Action == "restore-review" && result.OK && !validRestoreReviewResponse(input, result) {
		return errors.New("restore review response did not match the requested transaction and plan")
	}
	if input.Action == "restore-stage" && result.OK && !validRestoreStageResponse(input, result) {
		return errors.New("restore stage response did not contain the new pending transaction; inspect status before proceeding")
	}
	if outputFile != "" && result.OK {
		if input.Action == "review" && result.Review == nil {
			return errors.New("review response did not contain verified planning input")
		}
		if e := writePrivateNew(outputFile, data); e != nil {
			return e
		}
		result.Review = nil
		result.RestoreReview = nil
		result.ReviewSaved = true
	}
	if input.Action == "discard-prepared" && result.Status.TransactionID == input.TransactionID && result.Status.PlanChecksum == input.Checksum {
		annotateDiscardResponse(&result, socket, discardBatchLimit(input))
	}
	annotateCleanupResponse(&result, socket, input)
	if e = json.NewEncoder(out).Encode(result); e != nil {
		return errors.New("result could not be written")
	}
	if !result.OK {
		return errors.New("supervisor rejected action; inspect returned state")
	}
	return nil
}
