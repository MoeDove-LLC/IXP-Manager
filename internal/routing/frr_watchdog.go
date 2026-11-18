package routing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// FRROptions are fixed by the host administrator at service startup. Requests
// cannot choose executable paths, targets, sockets or command arguments.
type FRROptions struct {
	Target    string
	StateDir  string
	Validator string
	Reload    string
	Vtysh     string
	VtySocket string
}

type FRRTransaction struct {
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
type FRRStageRequest struct {
	Config         string `json:"config"`
	Checksum       string `json:"checksum"`
	BaseChecksum   string `json:"baseChecksum"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}
type FRRDecision struct {
	ID       string `json:"id"`
	Checksum string `json:"checksum"`
}

type FRRWatchdog struct {
	mu     sync.Mutex
	opts   FRROptions
	state  FRRTransaction
	unlock func()
	now    func() time.Time
	closed bool
}

func secureDir(path string) error {
	if !safePath.MatchString(path) || filepath.Clean(path) != path {
		return errors.New("directory must be an absolute clean path")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return errors.New("directory may not contain symlinks")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("state/socket directory must be private (mode 0700)")
	}
	return nil
}
func checkFRRTarget(target string) error {
	if !safePath.MatchString(target) || filepath.Clean(target) != target || filepath.Base(target) != "bgpd.conf" {
		return errors.New("FRR target must be an absolute clean per-daemon bgpd.conf path")
	}
	parent := filepath.Dir(target)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return errors.New("FRR target parent must exist without symlinks")
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigSize {
		return errors.New("FRR target must be a regular file no larger than 16 MiB")
	}
	return nil
}
func pendingFRR(status string) bool {
	switch status {
	case "staging", "pending", "rolling-back", "rollback-failed":
		return true
	}
	return false
}

// OpenFRRWatchdog acquires the target lock and restores any unconfirmed journal
// BEFORE serving requests. A supervisor must restart the service after a crash.
func OpenFRRWatchdog(ctx context.Context, opts FRROptions) (*FRRWatchdog, error) {
	if err := checkFRRTarget(opts.Target); err != nil {
		return nil, err
	}
	if err := secureDir(opts.StateDir); err != nil {
		return nil, err
	}
	if opts.Validator == "" {
		opts.Validator = "/usr/lib/frr/bgpd"
	}
	if opts.Reload == "" {
		opts.Reload = "/usr/lib/frr/frr-reload.py"
	}
	if opts.Vtysh == "" {
		opts.Vtysh = "/usr/bin/vtysh"
	}
	if opts.VtySocket == "" {
		opts.VtySocket = "/run/frr"
	}
	for _, path := range []string{opts.Validator, opts.Reload, opts.Vtysh, opts.VtySocket} {
		if !safePath.MatchString(path) || filepath.Clean(path) != path {
			return nil, errors.New("tool/socket paths must be absolute and clean")
		}
	}
	if filepath.Base(opts.Vtysh) != "vtysh" {
		return nil, errors.New("vtysh executable must be named vtysh")
	}
	unlock, err := deploymentLock(opts.Target)
	if err != nil {
		return nil, err
	}
	w := &FRRWatchdog{opts: opts, unlock: unlock, now: time.Now, state: FRRTransaction{Version: 1, Target: opts.Target, Status: "idle"}}
	binding := opts.Target + ".moedove-frr-state"
	bound, bindErr := readPrivate(binding, 4096)
	if os.IsNotExist(bindErr) {
		bindErr = writePrivate(binding, []byte(opts.StateDir))
	} else if bindErr == nil && string(bound) != opts.StateDir {
		bindErr = errors.New("target is bound to a different watchdog state directory; preserve the original recovery journal")
	}
	if bindErr != nil {
		unlock()
		return nil, bindErr
	}

	fail := func(err error) (*FRRWatchdog, error) { unlock(); return nil, err }
	raw, err := readPrivate(filepath.Join(opts.StateDir, "transaction.json"), 1<<20)
	if err == nil {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&w.state); err != nil {
			return fail(fmt.Errorf("invalid recovery journal: %w", err))
		}
		if w.state.Version != 1 || w.state.Target != opts.Target {
			return fail(errors.New("recovery journal belongs to another target or version"))
		}
		if pendingFRR(w.state.Status) {
			if err = w.rollbackLocked("recovered"); err != nil {
				return fail(err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fail(err)
	}
	current, err := os.ReadFile(opts.Target)
	if err != nil {
		return fail(err)
	}
	if err = ValidateManagedFRR(string(current)); err != nil {
		return fail(fmt.Errorf("target is not an owned managed profile; use explicit offline bootstrap: %w", err))
	}
	return w, nil
}

// Close is graceful: pending work is rolled back before releasing the lock.
func (w *FRRWatchdog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	var err error
	if pendingFRR(w.state.Status) {
		err = w.rollbackLocked("cancelled")
	}
	w.closed = true
	w.unlock()
	return err
}
func (w *FRRWatchdog) Status() FRRTransaction { w.mu.Lock(); defer w.mu.Unlock(); return w.state }
func (w *FRRWatchdog) Stage(ctx context.Context, request FRRStageRequest) (FRRTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.state, errors.New("watchdog is closed")
	}
	if pendingFRR(w.state.Status) {
		return w.state, errors.New("an unconfirmed or failed transaction already exists")
	}
	if request.TimeoutSeconds < 60 || request.TimeoutSeconds > 3600 {
		return w.state, errors.New("timeoutSeconds must be 60..3600")
	}
	if len(request.Checksum) != 64 || Checksum(request.Config) != request.Checksum {
		return w.state, errors.New("reviewed checksum mismatch")
	}
	if err := ValidateManagedFRR(request.Config); err != nil {
		return w.state, err
	}
	if err := checkFRRTarget(w.opts.Target); err != nil {
		return w.state, err
	}
	old, err := os.ReadFile(w.opts.Target)
	if err != nil {
		return w.state, err
	}
	targetInfo, err := os.Stat(w.opts.Target)
	if err != nil {
		return w.state, err
	}
	if err = privateAuthenticationTarget(request.Config+"\n"+string(old), targetInfo.Mode()); err != nil {
		return w.state, err
	}
	if len(request.BaseChecksum) != 64 || Checksum(string(old)) != request.BaseChecksum {
		return w.state, errors.New("installed base checksum changed")
	}
	if err = ValidateManagedFRR(string(old)); err != nil {
		return w.state, err
	}
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	if err = validateFRR(ctx, w.opts.Validator, string(old)); err != nil {
		return w.state, fmt.Errorf("rollback snapshot invalid: %w", err)
	}
	if err = validateFRR(ctx, w.opts.Validator, request.Config); err != nil {
		return w.state, err
	}
	if err = w.checkLive(ctx, w.opts.Target); err != nil {
		return w.state, fmt.Errorf("live configuration drift; refusing to touch it: %w", err)
	}
	// Recovery bytes are fsynced before the journal authorizes any mutation.
	if err = writePrivate(filepath.Join(w.opts.StateDir, "previous.conf"), old); err != nil {
		return w.state, err
	}
	if err = writePrivate(filepath.Join(w.opts.StateDir, "candidate.conf"), []byte(request.Config)); err != nil {
		return w.state, err
	}
	random := make([]byte, 24)
	if _, err = rand.Read(random); err != nil {
		return w.state, err
	}
	w.state = FRRTransaction{Version: 1, ID: hex.EncodeToString(random), Target: w.opts.Target, Status: "staging", Checksum: request.Checksum, BaseChecksum: request.BaseChecksum, Deadline: w.now().Add(time.Duration(request.TimeoutSeconds) * time.Second)}
	if err = w.persist(); err != nil {
		return w.state, err
	}
	fail := func(cause error) (FRRTransaction, error) {
		w.state.Error = cause.Error()
		back := w.rollbackLocked("failed")
		return w.state, errors.Join(cause, back)
	}
	info, err := os.Stat(w.opts.Target)
	if err != nil {
		return fail(err)
	}
	if err = atomicConfig(w.opts.Target, []byte(request.Config), info); err != nil {
		return fail(err)
	}
	if err = w.reload(ctx, w.opts.Target); err != nil {
		return fail(err)
	}
	if err = w.checkLive(ctx, w.opts.Target); err != nil {
		return fail(fmt.Errorf("live configuration did not converge: %w", err))
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
func (w *FRRWatchdog) Confirm(ctx context.Context, d FRRDecision) (FRRTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.decision(d); err != nil {
		return w.state, err
	}
	if w.state.Status != "pending" {
		return w.state, errors.New("only a fully applied pending transaction can be confirmed")
	}
	if !w.now().Before(w.state.Deadline) {
		err := w.rollbackLocked("expired")
		return w.state, errors.Join(errors.New("confirmation deadline expired"), err)
	}
	installed, err := os.ReadFile(w.opts.Target)
	if err != nil || Checksum(string(installed)) != w.state.Checksum {
		return w.state, errors.New("installed configuration drift; cancel or let the watchdog roll back")
	}
	if err = w.checkLive(ctx, w.opts.Target); err != nil {
		return w.state, fmt.Errorf("cannot confirm live drift: %w", err)
	}
	if !w.now().Before(w.state.Deadline) {
		err := w.rollbackLocked("expired")
		return w.state, errors.Join(errors.New("confirmation deadline expired"), err)
	}
	w.state.Status = "confirmed"
	w.state.Error = ""
	if err = w.persist(); err != nil {
		w.state.Status = "pending"
		return w.state, err
	}
	return w.state, nil
}
func (w *FRRWatchdog) Cancel(d FRRDecision) (FRRTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.decision(d); err != nil {
		return w.state, err
	}
	err := w.rollbackLocked("cancelled")
	return w.state, err
}
func (w *FRRWatchdog) decision(d FRRDecision) error {
	if w.closed {
		return errors.New("watchdog is closed")
	}
	if !pendingFRR(w.state.Status) || d.ID != w.state.ID || d.Checksum != w.state.Checksum {
		return errors.New("transaction ID/checksum does not identify the pending change")
	}
	return nil
}
func (w *FRRWatchdog) Tick() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if pendingFRR(w.state.Status) && (w.state.Status == "rollback-failed" || !w.now().Before(w.state.Deadline)) {
		return w.rollbackLocked("expired")
	}
	return nil
}
func (w *FRRWatchdog) rollbackLocked(finalStatus string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	w.state.Status = "rolling-back"
	if err := w.persist(); err != nil {
		return err
	}
	fail := func(err error) error {
		w.state.Status = "rollback-failed"
		w.state.Error = err.Error()
		_ = w.persist()
		return fmt.Errorf("FRR rollback incomplete; watchdog retry/restart required: %w", err)
	}
	previous, err := readPrivate(filepath.Join(w.opts.StateDir, "previous.conf"), maxConfigSize)
	if err != nil {
		return fail(err)
	}
	if Checksum(string(previous)) != w.state.BaseChecksum {
		return fail(errors.New("rollback snapshot checksum mismatch"))
	}
	if err = ValidateManagedFRR(string(previous)); err != nil {
		return fail(err)
	}
	if err = validateFRR(ctx, w.opts.Validator, string(previous)); err != nil {
		return fail(err)
	}
	if err = checkFRRTarget(w.opts.Target); err != nil {
		return fail(err)
	}
	info, err := os.Stat(w.opts.Target)
	if err != nil {
		return fail(err)
	}
	if err = atomicConfig(w.opts.Target, previous, info); err != nil {
		return fail(err)
	}
	if err = w.reload(ctx, w.opts.Target); err != nil {
		return fail(err)
	}
	if err = w.checkLive(ctx, w.opts.Target); err != nil {
		return fail(err)
	}
	w.state.Status = finalStatus
	return w.persist()
}
func (w *FRRWatchdog) persist() error {
	w.state.UpdatedAt = w.now().UTC()
	b, err := json.MarshalIndent(w.state, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(w.opts.StateDir, "transaction.json"), b)
}
func (w *FRRWatchdog) tool(ctx context.Context, mode, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{mode, "--daemon", "bgpd", "--bindir", filepath.Dir(w.opts.Vtysh), "--confdir", filepath.Dir(w.opts.Target), "--rundir", w.opts.StateDir, "--vty_socket", w.opts.VtySocket, "--stdout", "--log-level", "error", path}
	out, err := run(ctx, w.opts.Reload, args...)
	if err != nil {
		return "", nativeFailure("FRR reload helper failed", err)
	}
	return out, nil
}
func (w *FRRWatchdog) checkLive(ctx context.Context, path string) error {
	out, err := w.tool(ctx, "--test", path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return errors.New("frr-reload dry-run reports a delta or diagnostic")
	}
	return nil
}
func (w *FRRWatchdog) reload(ctx context.Context, path string) error {
	out, err := w.tool(ctx, "--reload", path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return errors.New("FRR reload emitted error diagnostics; raw output withheld")
	}
	return nil
}

func readPrivate(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, errors.New("private state must be a regular mode-0600 bounded file")
	}
	return os.ReadFile(path)
}
func writePrivate(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("state path is not a regular file")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".journal-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

var managedFRRLines = []*regexp.Regexp{
	regexp.MustCompile(`^! moedove-role role=route-collector policy=(observe|authorized-only)$`),
	regexp.MustCompile(`^route-map md_collect_export deny 10$`),
	regexp.MustCompile(`^neighbor [0-9a-fA-F:.]+ (enforce-first-as|route-map md_collect_export out|maximum-prefix [0-9]+ force)$`),
	regexp.MustCompile(`^! moedove-auth peer=md_[0-9a-f]{16} revision=[A-Za-z0-9_-]{22} state=(tcp-md5|disabled)$`),
	regexp.MustCompile(`^neighbor [0-9a-fA-F:.]+ password [A-Za-z0-9+/=_-]{1,80}$`),
	regexp.MustCompile(`^frr defaults traditional$`), regexp.MustCompile(`^hostname moedove-(rs|rc)$`),
	regexp.MustCompile(`^(ip|ipv6) prefix-list md_[0-9a-f]{16}(_o[0-9]+)? seq [0-9]+ permit [0-9a-fA-F:./]+$`),
	regexp.MustCompile(`^bgp as-path access-list md_[0-9a-f]{16} permit \^[0-9]+\(_\[0-9\]\+\)\{0,63\}\$$`),
	regexp.MustCompile(`^bgp as-path access-list md_[0-9a-f]{16}_o[0-9]+ permit \^[0-9]+\(_\[0-9\]\+\)\{0,62\}_[0-9]+\$$`),
	regexp.MustCompile(`^bgp as-path access-list md_[0-9a-f]{16}_o[0-9]+ permit \^\([0-9]+\|[0-9]+\(_\[0-9\]\+\)\{0,62\}_[0-9]+\)\$$`),
	regexp.MustCompile(`^route-map (md_authorized|md_ingress) (permit|deny) [0-9]+$`), regexp.MustCompile(`^match (ip|ipv6) address prefix-list md_[0-9a-f]{16}(_o[0-9]+)?$`), regexp.MustCompile(`^match as-path md_[0-9a-f]{16}(_o[0-9]+)?$`),
	regexp.MustCompile(`^router bgp [0-9]+ view MOEDOVE$`), regexp.MustCompile(`^bgp router-id [0-9.]+$`), regexp.MustCompile(`^no bgp default ipv4-unicast$`),
	regexp.MustCompile(`^neighbor [0-9a-fA-F:.]+ (remote-as [0-9]+|update-source [0-9a-fA-F:.]+|activate|route-server-client|prefix-list md_[0-9a-f]{16} in|filter-list md_[0-9a-f]{16} in|route-map (md_authorized|md_ingress) (in|out)|maximum-prefix [0-9]+)$`),
	regexp.MustCompile(`^address-family ipv[46] unicast$`), regexp.MustCompile(`^exit-address-family$`), regexp.MustCompile(`^exit$`),
	regexp.MustCompile(`^rpki$`), regexp.MustCompile(`^rpki (polling_period|retry_interval|expire_interval) [0-9]+$`),
	regexp.MustCompile(`^rpki cache tcp [0-9a-fA-F:.]+ [0-9]+ preference 1$`), regexp.MustCompile(`^match rpki valid$`),
	regexp.MustCompile(`^match peer [0-9a-fA-F:.]+$`),
	regexp.MustCompile(`^neighbor [0-9a-fA-F:.]+ soft-reconfiguration inbound$`),
}

// ValidateManagedFRR confines automatic writes to the renderer's vocabulary.
// Native parsing remains mandatory; this check is an ownership/scope gate.
func ValidateManagedFRR(config string) error {
	if isAS112Artifact("frr", config) {
		_, err := parseAS112Artifact("frr", config)
		return err
	}
	if err := rejectRedactedArtifact(config); err != nil {
		return err
	}
	collector := strings.HasPrefix(config, "! Generated by MoeDove IXP Manager; dedicated route-collector view.\n")
	if len(config) == 0 || len(config) > maxConfigSize || (!strings.HasPrefix(config, "! Generated by MoeDove IXP Manager; dedicated route-server view.\n") && !collector) {
		return errors.New("missing managed FRR profile marker")
	}
	routers := 0
	for i, line := range strings.Split(config, "\n") {
		if i == 0 {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" || line == "!" {
			continue
		}
		allowed := false
		for _, re := range managedFRRLines {
			if re.MatchString(line) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("line %d is outside the managed FRR profile", i+1)
		}
		if strings.HasPrefix(line, "router bgp ") {
			routers++
		}
	}
	if routers != 1 {
		return errors.New("managed FRR profile requires exactly one MOEDOVE BGP view")
	}
	if collector {
		return validateFRRCollectorShape(config)
	}
	return nil
}

// BootstrapFRR is deliberately OFFLINE: installs reviewed bytes on disk while
// preserving the unmanaged original. It never invokes the live reload helper.
func BootstrapFRR(ctx context.Context, target, validator, config, checksum, baseChecksum string) error {
	if err := checkFRRTarget(target); err != nil {
		return err
	}
	if Checksum(config) != checksum || len(checksum) != 64 {
		return errors.New("reviewed checksum mismatch")
	}
	if err := ValidateManagedFRR(config); err != nil {
		return err
	}
	unlock, err := deploymentLock(target)
	if err != nil {
		return err
	}
	defer unlock()
	old, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if ValidateManagedFRR(string(old)) == nil {
		return errors.New("target is already managed; use the watchdog transaction workflow")
	}
	if len(baseChecksum) != 64 || Checksum(string(old)) != baseChecksum {
		return errors.New("bootstrap base checksum mismatch")
	}
	if err = validateFRR(ctx, validator, config); err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if err = privateAuthenticationTarget(config+"\n"+string(old), info.Mode()); err != nil {
		return err
	}
	backup := target + ".moedove-bootstrap-original"
	if _, err = os.Lstat(backup); !os.IsNotExist(err) {
		return errors.New("bootstrap original already exists; refusing to overwrite")
	}
	if err = atomicConfig(backup, old, info); err != nil {
		return err
	}
	return atomicConfig(target, []byte(config), info)
}
