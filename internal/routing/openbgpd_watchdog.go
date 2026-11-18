package routing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type OpenBGPDOptions struct {
	Target            string
	StateDir          string
	Validator         string
	BGPCTL            string
	DaemonPID         int
	ExclusiveInstance bool
}
type OpenBGPDTransaction struct {
	Version      int       `json:"version"`
	ID           string    `json:"id"`
	Target       string    `json:"target"`
	Status       string    `json:"status"`
	Checksum     string    `json:"checksum"`
	BaseChecksum string    `json:"baseChecksum"`
	Deadline     time.Time `json:"deadline"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Error        string    `json:"error,omitempty"`
}
type OpenBGPDStageRequest struct {
	Config         string `json:"config"`
	Checksum       string `json:"checksum"`
	BaseChecksum   string `json:"baseChecksum"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}
type OpenBGPDDecision struct {
	ID       string `json:"id"`
	Checksum string `json:"checksum"`
}
type openBGPDHost struct {
	verify   func() error
	validate func(context.Context, string) error
	reload   func(context.Context, []string) error
}
type OpenBGPDWatchdog struct {
	mu     sync.Mutex
	opts   OpenBGPDOptions
	host   openBGPDHost
	state  OpenBGPDTransaction
	now    func() time.Time
	unlock func()
	closed bool
}

func checkOpenBGPDTarget(path string) error {
	if !safePath.MatchString(path) || filepath.Clean(path) != path || filepath.Base(path) != "bgpd.conf" {
		return errors.New("OpenBGPD target must be an absolute clean bgpd.conf path")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || resolved != filepath.Dir(path) {
		return errors.New("target parent must exist without symlinks")
	}
	_, err = openBGPDPrivateRead(path, maxConfigSize)
	return err
}
func pendingOpenBGPD(status string) bool {
	switch status {
	case "staging", "pending", "rolling-back", "rollback-failed":
		return true
	}
	return false
}

// OpenOpenBGPDWatchdog requires a dedicated Linux instance, parent PID and a
// pinned control socket. The daemon has no native confirmed commit or readable
// running-config hash: this is supervised local recovery, not a device transaction.
func OpenOpenBGPDWatchdog(ctx context.Context, opts OpenBGPDOptions) (*OpenBGPDWatchdog, error) {
	host, err := newOpenBGPDHost(opts)
	if err != nil {
		return nil, err
	}
	return openOpenBGPDWatchdog(ctx, opts, host)
}
func openOpenBGPDWatchdog(ctx context.Context, opts OpenBGPDOptions, host openBGPDHost) (*OpenBGPDWatchdog, error) {
	if !opts.ExclusiveInstance {
		return nil, errors.New("explicit exclusive-instance acknowledgment required; out-of-band runtime configuration is unsupported")
	}
	if err := checkOpenBGPDTarget(opts.Target); err != nil {
		return nil, err
	}
	if err := secureDir(opts.StateDir); err != nil {
		return nil, err
	}
	for _, path := range []string{opts.Validator, opts.BGPCTL} {
		if !safePath.MatchString(path) || filepath.Clean(path) != path {
			return nil, errors.New("native tool paths must be absolute and clean")
		}
	}
	unlock, err := deploymentLock(opts.Target)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*OpenBGPDWatchdog, error) { unlock(); return nil, err }
	w := &OpenBGPDWatchdog{opts: opts, host: host, now: time.Now, unlock: unlock, state: OpenBGPDTransaction{Version: 1, Target: opts.Target, Status: "idle"}}
	// Bind the target to one recovery journal and tool identity. A replacement
	// daemon PID may be supplied after reboot, but must prove the same file/binary.
	binding := opts.Target + ".moedove-openbgpd-state"
	expected := opts.StateDir + "\n" + opts.Validator + "\n" + opts.BGPCTL
	bound, e := openBGPDPrivateRead(binding, 4096)
	if os.IsNotExist(e) {
		e = writePrivate(binding, []byte(expected))
	} else if e == nil && string(bound) != expected {
		e = errors.New("target is bound to a different recovery journal or native tools")
	}
	if e != nil {
		return fail(e)
	}
	if err = host.verify(); err != nil {
		return fail(err)
	}
	raw, err := openBGPDPrivateRead(filepath.Join(opts.StateDir, "transaction.json"), 1<<20)
	if err == nil {
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.DisallowUnknownFields()
		if d.Decode(&w.state) != nil || d.Decode(new(any)) != io.EOF || !w.validState() {
			return fail(errors.New("invalid OpenBGPD recovery journal"))
		}
		if pendingOpenBGPD(w.state.Status) {
			if err = w.rollbackLocked("recovered"); err != nil {
				return fail(err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fail(err)
	}
	current, err := openBGPDPrivateRead(opts.Target, maxConfigSize)
	if err != nil {
		return fail(err)
	}
	if err = ValidateManagedOpenBGPD(string(current)); err != nil {
		return fail(err)
	}
	if err = host.validate(ctx, string(current)); err != nil {
		return fail(err)
	}
	return w, nil
}
func (w *OpenBGPDWatchdog) validState() bool {
	if w.state.Version != 1 || w.state.Target != w.opts.Target {
		return false
	}
	switch w.state.Status {
	case "idle":
		return w.state.ID == ""
	case "staging", "pending", "rolling-back", "rollback-failed", "confirmed", "cancelled", "expired", "failed", "recovered":
	default:
		return false
	}
	return len(w.state.ID) == 48 && validOpenBGPDHash(w.state.Checksum) && validOpenBGPDHash(w.state.BaseChecksum) && !w.state.Deadline.IsZero()
}
func validOpenBGPDHash(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && v == strings.ToLower(v)
}
func (w *OpenBGPDWatchdog) persist() error {
	w.state.UpdatedAt = w.now().UTC()
	raw, err := json.Marshal(w.state)
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(w.opts.StateDir, "transaction.json"), raw)
}
func (w *OpenBGPDWatchdog) Status() OpenBGPDTransaction {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}
func (w *OpenBGPDWatchdog) Stage(ctx context.Context, r OpenBGPDStageRequest) (OpenBGPDTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || pendingOpenBGPD(w.state.Status) {
		return w.state, errors.New("watchdog closed or an unconfirmed/recovery transaction exists")
	}
	if r.TimeoutSeconds < 60 || r.TimeoutSeconds > 3600 || !validOpenBGPDHash(r.Checksum) || Checksum(r.Config) != r.Checksum {
		return w.state, errors.New("invalid timeout or reviewed checksum")
	}
	if err := ValidateManagedOpenBGPD(r.Config); err != nil {
		return w.state, err
	}
	old, err := openBGPDPrivateRead(w.opts.Target, maxConfigSize)
	if err != nil {
		return w.state, err
	}
	if !validOpenBGPDHash(r.BaseChecksum) || Checksum(string(old)) != r.BaseChecksum {
		return w.state, errors.New("installed base checksum changed")
	}
	if err = ValidateManagedOpenBGPD(string(old)); err != nil {
		return w.state, err
	}
	reset, err := openBGPDChangedAuthentication(string(old), r.Config)
	if err != nil {
		return w.state, err
	}
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	if err = w.host.verify(); err != nil {
		return w.state, err
	}
	if err = w.host.validate(ctx, string(old)); err != nil {
		return w.state, err
	}
	if err = w.host.validate(ctx, r.Config); err != nil {
		return w.state, err
	}
	if err = writePrivate(filepath.Join(w.opts.StateDir, "previous.conf"), old); err != nil {
		return w.state, err
	}
	if err = writePrivate(filepath.Join(w.opts.StateDir, "candidate.conf"), []byte(r.Config)); err != nil {
		return w.state, err
	}
	var random [24]byte
	if _, err = rand.Read(random[:]); err != nil {
		return w.state, err
	}
	w.state = OpenBGPDTransaction{Version: 1, ID: hex.EncodeToString(random[:]), Target: w.opts.Target, Status: "staging", Checksum: r.Checksum, BaseChecksum: r.BaseChecksum, Deadline: w.now().Add(time.Duration(r.TimeoutSeconds) * time.Second)}
	if err = w.persist(); err != nil {
		return w.state, err
	}
	fail := func(cause error) (OpenBGPDTransaction, error) {
		w.state.Error = "stage failed"
		back := w.rollbackLocked("failed")
		return w.state, errors.Join(cause, back)
	}
	// Recheck the disk baseline after native validation and before replacement.
	latest, err := openBGPDPrivateRead(w.opts.Target, maxConfigSize)
	if err != nil || Checksum(string(latest)) != r.BaseChecksum {
		return fail(errors.New("target changed during validation"))
	}
	info, err := os.Stat(w.opts.Target)
	if err != nil {
		return fail(err)
	}
	if err = atomicConfig(w.opts.Target, []byte(r.Config), info); err != nil {
		return fail(err)
	}
	if err = w.host.reload(ctx, reset); err != nil {
		return fail(err)
	}
	if !w.now().Before(w.state.Deadline) {
		return fail(errors.New("confirmation deadline elapsed during reload"))
	}
	w.state.Status = "pending"
	if err = w.persist(); err != nil {
		return fail(err)
	}
	return w.state, nil
}
func (w *OpenBGPDWatchdog) decision(d OpenBGPDDecision) error {
	if w.closed || !pendingOpenBGPD(w.state.Status) || d.ID != w.state.ID || d.Checksum != w.state.Checksum {
		return errors.New("transaction ID/checksum does not identify the pending change")
	}
	return nil
}
func (w *OpenBGPDWatchdog) Confirm(ctx context.Context, d OpenBGPDDecision) (OpenBGPDTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.decision(d); err != nil {
		return w.state, err
	}
	if w.state.Status != "pending" {
		return w.state, errors.New("only an acknowledged pending reload may be confirmed")
	}
	if !w.now().Before(w.state.Deadline) {
		return w.state, errors.Join(errors.New("confirmation expired"), w.rollbackLocked("expired"))
	}
	installed, err := openBGPDPrivateRead(w.opts.Target, maxConfigSize)
	if err != nil || Checksum(string(installed)) != w.state.Checksum {
		return w.state, errors.New("disk configuration drift; cancel or let watchdog recover")
	}
	if err = w.host.verify(); err != nil {
		return w.state, err
	}
	if err = ctx.Err(); err != nil {
		return w.state, err
	}
	if !w.now().Before(w.state.Deadline) {
		return w.state, errors.Join(errors.New("confirmation expired during identity verification"), w.rollbackLocked("expired"))
	}
	w.state.Status = "confirmed"
	w.state.Error = ""
	if err = w.persist(); err != nil {
		w.state.Status = "pending"
	}
	return w.state, err
}
func (w *OpenBGPDWatchdog) Cancel(d OpenBGPDDecision) (OpenBGPDTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.decision(d); err != nil {
		return w.state, err
	}
	err := w.rollbackLocked("cancelled")
	return w.state, err
}
func (w *OpenBGPDWatchdog) Tick() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed && pendingOpenBGPD(w.state.Status) && (w.state.Status == "rollback-failed" || !w.now().Before(w.state.Deadline)) {
		return w.rollbackLocked("expired")
	}
	return nil
}
func (w *OpenBGPDWatchdog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	var err error
	if pendingOpenBGPD(w.state.Status) {
		err = w.rollbackLocked("cancelled")
	}
	w.closed = true
	w.unlock()
	return err
}
func (w *OpenBGPDWatchdog) rollbackLocked(final string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	w.state.Status = "rolling-back"
	if err := w.persist(); err != nil {
		w.state.Status = "rollback-failed"
		return err
	}
	fail := func(err error) error {
		w.state.Status = "rollback-failed"
		w.state.Error = "recovery failed; watchdog retry or restart required"
		_ = w.persist()
		return errors.Join(errors.New("OpenBGPD recovery incomplete"), err)
	}
	previous, err := openBGPDPrivateRead(filepath.Join(w.opts.StateDir, "previous.conf"), maxConfigSize)
	if err != nil || Checksum(string(previous)) != w.state.BaseChecksum {
		return fail(errors.New("recovery snapshot checksum mismatch"))
	}
	candidate, err := openBGPDPrivateRead(filepath.Join(w.opts.StateDir, "candidate.conf"), maxConfigSize)
	if err != nil || Checksum(string(candidate)) != w.state.Checksum {
		return fail(errors.New("candidate snapshot checksum mismatch"))
	}
	reset, err := openBGPDChangedAuthentication(string(candidate), string(previous))
	if err != nil {
		return fail(err)
	}
	if err = w.host.verify(); err != nil {
		return fail(err)
	}
	if err = w.host.validate(ctx, string(previous)); err != nil {
		return fail(err)
	}
	if err = checkOpenBGPDTarget(w.opts.Target); err != nil {
		return fail(err)
	}
	current, err := openBGPDPrivateRead(w.opts.Target, maxConfigSize)
	if err != nil {
		return fail(err)
	}
	if sum := Checksum(string(current)); sum != w.state.Checksum && sum != w.state.BaseChecksum {
		return fail(errors.New("unrelated target drift; refusing to overwrite outside changes"))
	}
	info, err := os.Stat(w.opts.Target)
	if err != nil {
		return fail(err)
	}
	if err = atomicConfig(w.opts.Target, previous, info); err != nil {
		return fail(err)
	}
	if err = w.host.reload(ctx, reset); err != nil {
		return fail(err)
	}
	w.state.Status = final
	w.state.Error = ""
	return w.persist()
}
func openBGPDChangedAuthentication(old, new string) ([]string, error) {
	a, err := parseManagedOpenBGPD(old)
	if err != nil {
		return nil, err
	}
	b, err := parseManagedOpenBGPD(new)
	if err != nil {
		return nil, err
	}
	previous := map[string]string{}
	for _, p := range a.Peers {
		previous[p.Address] = p.Password
	}
	addresses := []string{}
	for _, p := range b.Peers {
		if password, existed := previous[p.Address]; existed && password != p.Password {
			addresses = append(addresses, p.Address)
		}
	}
	if len(addresses) > 64 {
		return nil, errors.New("at most64 existing TCP-MD5 session changes per transaction; split rollout")
	}
	sort.Strings(addresses)
	return addresses, nil
}

// Linux O_NOFOLLOW/O_NONBLOCK implementation is in openbgpd_host.go.
func openBGPDToolError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s failed; native diagnostics withheld", stage)
}
