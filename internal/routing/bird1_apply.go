package routing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// BIRD1ApplyOptions is local operator authority, never HTTP input. Both exact
// artifact hashes and an explicit dedicated target/socket are required.
type BIRD1ApplyOptions struct {
	BirdBinary                    string
	Target                        string
	Socket                        string
	Checksum                      string
	BaseChecksum                  string
	ExclusiveInstanceAcknowledged bool
	TimeoutSeconds                int
	checkpoint                    func(string) // package-internal fault boundary; never a CLI option
}
type bird1Journal struct {
	Version         int            `json:"version"`
	Platform        string         `json:"platform"`
	Family          int            `json:"family"`
	Target          string         `json:"target"`
	Socket          string         `json:"socket"`
	BaseChecksum    string         `json:"baseChecksum"`
	NextChecksum    string         `json:"nextChecksum"`
	BaseFingerprint string         `json:"baseFingerprint"`
	NextFingerprint string         `json:"nextFingerprint"`
	Daemon          bird1DaemonPin `json:"initialDaemon"`
	Phase           string         `json:"phase"`
	CreatedAt       time.Time      `json:"createdAt"`
}

func bird1HashOK(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func bird1Options(opts BIRD1ApplyOptions) (BIRD1ApplyOptions, error) {
	if !opts.ExclusiveInstanceAcknowledged || !bird1HashOK(opts.Checksum) || !bird1HashOK(opts.BaseChecksum) || opts.Checksum == opts.BaseChecksum {
		return opts, errors.New("BIRD1 requires exclusive-instance acknowledgment and distinct exact base/candidate SHA256 values")
	}
	for _, path := range []string{opts.Target, opts.Socket, opts.BirdBinary} {
		if !safePath.MatchString(path) || filepath.Clean(path) != path || path == "/" {
			return opts, errors.New("BIRD1 target, socket and native checker must be explicit clean absolute paths")
		}
	}
	if len(opts.Socket) > 107 {
		return opts, errors.New("BIRD1 Unix socket path exceeds bound")
	}
	for _, dir := range []string{filepath.Dir(opts.Target), filepath.Dir(opts.Socket)} {
		if e := bird1PrivateDir(dir); e != nil {
			return opts, e
		}
	}
	if opts.TimeoutSeconds == 0 {
		opts.TimeoutSeconds = 60
	}
	if opts.TimeoutSeconds < 2 || opts.TimeoutSeconds > 300 {
		return opts, errors.New("BIRD1 native undo timeout must be 2..300 seconds")
	}
	return opts, nil
}
func readBIRD1Journal(target string) (bird1Journal, error) {
	var journal bird1Journal
	raw, _, e := bird1PrivateRead(target+".moedove-pending", 32<<10)
	if e != nil {
		return journal, e
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&journal) != nil || d.Decode(new(any)) != io.EOF {
		return journal, errors.New("invalid BIRD1 recovery journal")
	}
	canonical, _ := json.MarshalIndent(journal, "", "  ")
	if string(raw) != string(canonical)+"\n" || journal.Version != 1 || journal.Platform != "bird1" || journal.Target != target || (journal.Family != 4 && journal.Family != 6) || !bird1HashOK(journal.BaseChecksum) || !bird1HashOK(journal.NextChecksum) || journal.BaseChecksum == journal.NextChecksum || !bird1HashOK(journal.BaseFingerprint) || !bird1HashOK(journal.NextFingerprint) || journal.Daemon.PID <= 0 || journal.Daemon.StartTime == "" || !bird1HashOK(journal.Daemon.ExecutableSHA256) || journal.CreatedAt.IsZero() {
		return journal, errors.New("BIRD1 recovery journal identity invalid")
	}
	switch journal.Phase {
	case "prepared", "stage-intent", "configure-intent", "confirm-intent", "publish-intent", "rollback-intent":
	default:
		return journal, errors.New("unknown BIRD1 recovery phase")
	}
	return journal, nil
}
func bird1SaveJournal(state *bird1Journal, phase string, initial bool) error {
	path := state.Target + ".moedove-pending"
	var info os.FileInfo
	if !initial {
		current, e := readBIRD1Journal(state.Target)
		if e != nil || !reflect.DeepEqual(current, *state) {
			return errors.New("BIRD1 recovery journal changed outside this transaction")
		}
		_, info, e = bird1PrivateRead(path, 32<<10)
		if e != nil {
			return e
		}
	}
	next := *state
	next.Phase = phase
	raw, e := json.MarshalIndent(next, "", "  ")
	if e != nil {
		return e
	}
	raw = append(raw, '\n')
	if initial {
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(raw)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if e = syncDirectory(filepath.Dir(path)); e != nil {
			return e
		}
	} else if e = atomicConfig(path, raw, info); e != nil {
		return e
	}
	*state = next
	return nil
}
func bird1TargetEndpoint(opts BIRD1ApplyOptions, state bird1Journal) ([]byte, os.FileInfo, error) {
	raw, info, e := bird1PrivateRead(opts.Target, maxConfigSize)
	if e != nil {
		return nil, nil, e
	}
	sum := Checksum(string(raw))
	if sum != state.BaseChecksum && sum != state.NextChecksum {
		return nil, nil, errors.New("BIRD1 target bytes are neither approved endpoint; no overwrite permitted")
	}
	return raw, info, nil
}
func bird1ReadbackEndpoint(ctx context.Context, host *bird1Host, state bird1Journal) (string, error) {
	value, e := host.readback(ctx)
	if e != nil {
		return "", e
	}
	if value != state.BaseFingerprint && value != state.NextFingerprint {
		return "", errors.New("BIRD1 running configuration is neither reviewed endpoint")
	}
	return value, nil
}
func bird1FinishRollback(ctx context.Context, opts BIRD1ApplyOptions, state *bird1Journal, host *bird1Host) error {
	if e := bird1CandidateEvidence(opts.Target, state.NextChecksum); e != nil {
		return e
	}
	previous, _, e := bird1PrivateRead(opts.Target+".moedove-previous", maxConfigSize)
	if e != nil {
		return e
	}
	baseModel, modelErr := parseManagedBIRD1(string(previous))
	if modelErr != nil || baseModel.Family != state.Family {
		return errors.New("BIRD1 rollback profile differs from journal family")
	}
	fingerprint, e := bird1RuntimeFingerprint(string(previous))
	if e != nil || Checksum(string(previous)) != state.BaseChecksum || fingerprint != state.BaseFingerprint {
		return errors.New("BIRD1 rollback snapshot no longer matches reviewed bytes")
	}
	if e = ValidateBIRD1(ctx, opts.BirdBinary, string(previous)); e != nil {
		return e
	}
	current, info, e := bird1TargetEndpoint(opts, *state)
	if e != nil {
		return e
	}
	runtime, e := bird1ReadbackEndpoint(ctx, host, *state)
	if e != nil {
		return e
	}
	if host.pin.ExecutableSHA256 != state.Daemon.ExecutableSHA256 {
		return errors.New("BIRD1 native executable differs from recovery binding")
	}
	if e = bird1SaveJournal(state, "rollback-intent", false); e != nil {
		return e
	}
	if Checksum(string(current)) != state.BaseChecksum {
		if e = atomicConfig(opts.Target, previous, info); e != nil {
			return e
		}
	}
	actual, _, e := bird1PrivateRead(opts.Target, maxConfigSize)
	if e != nil || Checksum(string(actual)) != state.BaseChecksum {
		return errors.New("BIRD1 rollback disk bytes unverified")
	}
	if runtime != state.BaseFingerprint {
		if e = host.check(ctx, opts.Target); e != nil {
			return e
		}
		// An untimed baseline restoration cannot later undo back to the rejected
		// candidate. This is explicit recovery, never reissuing the candidate.
		if e = host.configure(ctx, false); e != nil {
			return e
		}
	}
	actualRuntime, e := host.readback(ctx)
	if e != nil || actualRuntime != state.BaseFingerprint {
		return errors.New("BIRD1 previous runtime configuration is not verified")
	}
	actual, _, e = bird1PrivateRead(opts.Target, maxConfigSize)
	if e != nil || Checksum(string(actual)) != state.BaseChecksum {
		return errors.New("BIRD1 previous disk configuration changed during recovery")
	}
	if e = bird1RemoveCandidate(opts.Target, state.NextChecksum); e != nil {
		return e
	}
	return bird1ClearVerifiedPending(opts.Target, *state)
}
func bird1CandidateEvidence(target, checksum string) error {
	raw, _, e := bird1PrivateRead(target+".moedove-next", maxConfigSize)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if Checksum(string(raw)) != checksum {
		return errors.New("BIRD1 candidate recovery material changed; no deletion or replacement permitted")
	}
	return nil
}
func bird1RemoveCandidate(target, checksum string) error {
	if e := bird1CandidateEvidence(target, checksum); e != nil {
		return e
	}
	if e := os.Remove(target + ".moedove-next"); e != nil {
		if os.IsNotExist(e) {
			return nil
		}
		return e
	}
	return syncDirectory(filepath.Dir(target))
}
func bird1ClearVerifiedPending(target string, want bird1Journal) error {
	current, e := readBIRD1Journal(target)
	if e != nil || !reflect.DeepEqual(current, want) {
		return errors.New("BIRD1 pending journal changed before completion")
	}
	return clearPending(target)
}

func ApplyBIRD1(ctx context.Context, config string, options BIRD1ApplyOptions) error {
	return applyBIRD1(ctx, config, options, newBird1Host)
}
func applyBIRD1(ctx context.Context, config string, options BIRD1ApplyOptions, openHost func(BIRD1ApplyOptions) (*bird1Host, error)) error {
	opts, e := bird1Options(options)
	if e != nil {
		return e
	}
	if Checksum(config) != opts.Checksum {
		return errors.New("BIRD1 candidate differs from the exact reviewed checksum")
	}
	nextModel, e := parseManagedBIRD1(config)
	if e != nil {
		return e
	}
	nextFingerprint, e := bird1RuntimeFingerprint(config)
	if e != nil {
		return e
	}
	if _, _, e = bird1PrivateRead(opts.Target+".moedove-lock", 0); e != nil && !os.IsNotExist(e) {
		return e
	}
	unlock, e := deploymentLock(opts.Target)
	if e != nil {
		return e
	}
	defer unlock()
	if _, e = os.Lstat(opts.Target + ".moedove-pending"); !os.IsNotExist(e) {
		return errors.New("BIRD1 pending recovery material exists; explicitly recover before another apply")
	}
	if _, e = os.Lstat(opts.Target + ".moedove-next"); !os.IsNotExist(e) {
		return errors.New("BIRD1 untracked candidate file exists; inspect recovery material before applying")
	}
	previous, info, e := bird1PrivateRead(opts.Target, maxConfigSize)
	if e != nil {
		return e
	}
	if Checksum(string(previous)) != opts.BaseChecksum {
		return errors.New("BIRD1 current file differs from the reviewed base checksum")
	}
	baseModel, e := parseManagedBIRD1(string(previous))
	if e != nil {
		return e
	}
	baseFingerprint, e := bird1RuntimeFingerprint(string(previous))
	if e != nil {
		return e
	}
	if baseModel.Family != nextModel.Family || baseModel.Router.ID == "" || baseModel.Router.ID != nextModel.Router.ID {
		return errors.New("BIRD1 replacement requires the same router entity and daemon address family")
	}
	for _, value := range []string{string(previous), config} {
		if e = ValidateBIRD1(ctx, opts.BirdBinary, value); e != nil {
			return e
		}
	}
	host, e := openHost(opts)
	if e != nil {
		return e
	}
	running, e := host.readback(ctx)
	if e != nil || running != baseFingerprint {
		return errors.New("BIRD1 running configuration does not match the reviewed v2 baseline")
	}
	if e = host.check(ctx, opts.Target); e != nil {
		return e
	}
	latest, _, e := bird1PrivateRead(opts.Target, maxConfigSize)
	if e != nil || Checksum(string(latest)) != opts.BaseChecksum {
		return errors.New("BIRD1 baseline changed during preflight")
	}
	backup := opts.Target + ".moedove-previous"
	if _, _, e = bird1PrivateRead(backup, maxConfigSize); e != nil && !os.IsNotExist(e) {
		return e
	}
	if e = atomicConfig(backup, previous, info); e != nil {
		return e
	}
	state := bird1Journal{Version: 1, Platform: "bird1", Family: nextModel.Family, Target: opts.Target, Socket: opts.Socket, BaseChecksum: opts.BaseChecksum, NextChecksum: opts.Checksum, BaseFingerprint: baseFingerprint, NextFingerprint: nextFingerprint, Daemon: host.pin, CreatedAt: time.Now().UTC()}
	if e = bird1SaveJournal(&state, "prepared", true); e != nil {
		return e
	}
	checkpoint := func(phase string) {
		if opts.checkpoint != nil {
			opts.checkpoint(phase)
		}
	}
	checkpoint("prepared")
	rollback := func(cause error) error {
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if e := bird1FinishRollback(recovery, opts, &state, host); e != nil {
			return errors.Join(cause, errors.New("BIRD1 rollback incomplete; preserve the journal and exact snapshots for explicit recovery"), e)
		}
		return errors.Join(cause, errors.New("BIRD1 previous disk and runtime configuration verified restored"))
	}
	if e = ctx.Err(); e != nil {
		return rollback(e)
	}
	if e = bird1SaveJournal(&state, "stage-intent", false); e != nil {
		return rollback(e)
	}
	latest, _, e = bird1PrivateRead(opts.Target, maxConfigSize)
	if e != nil || Checksum(string(latest)) != opts.BaseChecksum {
		return rollback(errors.New("BIRD1 baseline changed before candidate staging"))
	}
	if e = atomicConfig(opts.Target+".moedove-next", []byte(config), info); e != nil {
		return rollback(e)
	}
	checkpoint("candidate-staged")
	if e = bird1SaveJournal(&state, "configure-intent", false); e != nil {
		return rollback(e)
	}
	if e = host.check(ctx, opts.Target+".moedove-next"); e != nil {
		return rollback(e)
	}
	actual, _, e := bird1PrivateRead(opts.Target+".moedove-next", maxConfigSize)
	if e != nil || Checksum(string(actual)) != opts.Checksum {
		return rollback(errors.New("BIRD1 candidate changed before native configure"))
	}
	if e = host.configure(ctx, true); e != nil {
		return rollback(e)
	}
	checkpoint("configured")
	running, e = host.readback(ctx)
	if e != nil || running != nextFingerprint {
		return rollback(errors.New("BIRD1 candidate runtime is not ready with exact readback"))
	}
	if e = bird1SaveJournal(&state, "confirm-intent", false); e != nil {
		return rollback(e)
	}
	actual, _, e = bird1PrivateRead(opts.Target, maxConfigSize)
	if e != nil || Checksum(string(actual)) != opts.BaseChecksum {
		return rollback(errors.New("BIRD1 startup target changed before confirmation"))
	}
	staged, _, stageErr := bird1PrivateRead(opts.Target+".moedove-next", maxConfigSize)
	if stageErr != nil || Checksum(string(staged)) != opts.Checksum {
		return rollback(errors.New("BIRD1 staged candidate changed before confirmation"))
	}
	if e = host.confirm(ctx); e != nil {
		return rollback(e)
	}
	checkpoint("confirmed")
	running, e = host.readback(ctx)
	if e != nil || running != nextFingerprint {
		return rollback(errors.New("BIRD1 confirmed runtime does not match reviewed bytes"))
	}
	if e = bird1SaveJournal(&state, "publish-intent", false); e != nil {
		return rollback(e)
	}
	actual, _, e = bird1PrivateRead(opts.Target, maxConfigSize)
	if e != nil || Checksum(string(actual)) != opts.BaseChecksum {
		return rollback(errors.New("BIRD1 startup target changed before confirmed publication"))
	}
	if e = atomicConfig(opts.Target, []byte(config), info); e != nil {
		return rollback(e)
	}
	checkpoint("published")
	running, e = host.readback(ctx)
	if e != nil || running != nextFingerprint {
		return rollback(errors.New("BIRD1 runtime changed during startup-file publication"))
	}
	actual, _, e = bird1PrivateRead(opts.Target, maxConfigSize)
	if e != nil || Checksum(string(actual)) != opts.Checksum {
		return rollback(errors.New("BIRD1 confirmed disk bytes changed before journal completion"))
	}
	if e = bird1RemoveCandidate(opts.Target, opts.Checksum); e != nil {
		return errors.Join(errors.New("BIRD1 confirmed configuration retained; candidate cleanup uncertain, preserve journal"), e)
	}
	return bird1ClearVerifiedPending(opts.Target, state)
}

// RecoverBIRD1 is an explicit rollback request. It may pin a restarted dedicated
// daemon only after executable, arguments, socket credentials and current v2
// readback are reverified. It never adopts a PID to send a signal, starts a daemon,
// repeats the candidate mutation, or treats an unknown third config as owned.
func RecoverBIRD1(ctx context.Context, options BIRD1ApplyOptions) error {
	opts, e := bird1Options(options)
	if e != nil {
		return e
	}
	if _, _, e = bird1PrivateRead(opts.Target+".moedove-lock", 0); e != nil && !os.IsNotExist(e) {
		return e
	}
	unlock, e := deploymentLock(opts.Target)
	if e != nil {
		return e
	}
	defer unlock()
	state, e := readBIRD1Journal(opts.Target)
	if e != nil {
		return e
	}
	if state.Socket != opts.Socket || state.BaseChecksum != opts.BaseChecksum || state.NextChecksum != opts.Checksum || state.Daemon.Executable != opts.BirdBinary {
		return errors.New("BIRD1 recovery request differs from exact journal target, socket, executable or checksum pair")
	}
	host, e := newBird1Host(opts)
	if e != nil {
		return e
	}
	return bird1FinishRollback(ctx, opts, &state, host)
}
