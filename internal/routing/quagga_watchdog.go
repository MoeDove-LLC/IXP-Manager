package routing

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type QuaggaOptions struct {
	StateDir          string
	Binary            string
	BinaryChecksum    string
	BootstrapFile     string
	BootstrapChecksum string
	BGPPort           int
	ExclusiveInstance bool
}

type QuaggaStageRequest struct {
	Config         string `json:"config"`
	Checksum       string `json:"checksum"`
	BaseChecksum   string `json:"baseChecksum"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type QuaggaDecision struct {
	ID       string `json:"id"`
	Checksum string `json:"checksum"`
}

type QuaggaTransaction struct {
	Version        int       `json:"version"`
	ID             string    `json:"id,omitempty"`
	Status         string    `json:"status"`
	ActiveChecksum string    `json:"activeChecksum"`
	Checksum       string    `json:"checksum,omitempty"`
	BaseChecksum   string    `json:"baseChecksum,omitempty"`
	Deadline       time.Time `json:"deadline,omitzero"`
}

type quaggaJournal struct {
	Binding    string            `json:"binding"`
	ActiveSlot int               `json:"activeSlot"`
	Slots      [2]string         `json:"slotChecksums"`
	State      QuaggaTransaction `json:"transaction"`
}

// QuaggaWatchdog owns one foreground daemon and the private committed policy.
// Replacement deliberately interrupts BGP. The original policy remains the
// restart policy until the exact replacement transaction is confirmed.
type QuaggaWatchdog struct {
	mu      sync.Mutex
	host    *quaggaHost
	journal quaggaJournal
	child   *quaggaChild
	unlock  func()
	closed  bool
	faulted bool
}

func quaggaChecksum(value string) bool {
	return len(value) == 64 && quaggaHex(value)
}

func quaggaHex(value string) bool {
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func quaggaOptionsValid(opts QuaggaOptions) error {
	if !opts.ExclusiveInstance {
		return errors.New("explicit Quagga exclusive-instance and BGP interruption acknowledgment required")
	}
	if !safePath.MatchString(opts.Binary) || filepath.Clean(opts.Binary) != opts.Binary || !quaggaChecksum(opts.BinaryChecksum) || opts.BGPPort < 1 || opts.BGPPort >= 65535 {
		return errors.New("explicit Quagga binary path, binary SHA256 and BGP port required")
	}
	if opts.BootstrapFile != "" && (!safePath.MatchString(opts.BootstrapFile) || filepath.Clean(opts.BootstrapFile) != opts.BootstrapFile || !quaggaChecksum(opts.BootstrapChecksum)) || opts.BootstrapFile == "" && opts.BootstrapChecksum != "" {
		return errors.New("Quagga bootstrap requires an explicit private file and reviewed SHA256")
	}
	if len(filepath.Join(opts.StateDir, "bgpd.vty")) >= len(syscall.RawSockaddrUnix{}.Path) {
		return errors.New("Quagga private VTY path exceeds the Linux Unix socket limit")
	}
	if err := secureDir(opts.StateDir); err != nil {
		return err
	}
	info, err := os.Stat(opts.StateDir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0700 {
		return errors.New("Quagga state directory must be owned by the supervisor UID with mode 0700")
	}
	return nil
}

func OpenQuaggaWatchdog(ctx context.Context, opts QuaggaOptions) (*QuaggaWatchdog, error) {
	if err := quaggaOptionsValid(opts); err != nil {
		return nil, err
	}
	unlock, err := deploymentLock(filepath.Join(opts.StateDir, "quagga"))
	if err != nil {
		return nil, err
	}
	host, err := newQuaggaHost(opts)
	if err != nil {
		unlock()
		return nil, err
	}
	w := &QuaggaWatchdog{host: host, unlock: unlock}
	if err = w.open(ctx); err != nil {
		return nil, errors.Join(err, w.Close())
	}
	return w, nil
}

func (w *QuaggaWatchdog) open(ctx context.Context) error {
	opts := w.host.opts
	binding := Checksum(opts.StateDir + "\n" + opts.Binary + "\n" + opts.BinaryChecksum + "\n" + strconv.Itoa(opts.BGPPort) + "\n" + strconv.Itoa(os.Geteuid()))
	raw, err := quaggaPrivateRead(w.path("transaction.json"), 4096)
	if os.IsNotExist(err) {
		if opts.BootstrapFile == "" {
			return errors.New("Quagga initial startup requires an explicitly reviewed bootstrap")
		}
		if _, err = os.Lstat(w.slotPath(1)); !os.IsNotExist(err) {
			return errors.New("Quagga initial state has an unknown candidate file")
		}
		base, err := quaggaPrivateRead(opts.BootstrapFile, maxConfigSize)
		if err != nil || Checksum(string(base)) != opts.BootstrapChecksum {
			return errors.New("Quagga bootstrap file does not match the reviewed checksum")
		}
		if err = w.host.validate(ctx, string(base)); err != nil {
			return err
		}
		existing, err := quaggaPrivateRead(w.slotPath(0), maxConfigSize)
		if os.IsNotExist(err) {
			err = writePrivate(w.slotPath(0), base)
		} else if err == nil && !bytes.Equal(existing, base) {
			return errors.New("Quagga initial state has an unknown baseline file")
		}
		if err != nil {
			return err
		}
		initial := quaggaJournal{Binding: binding, State: QuaggaTransaction{Version: 1, Status: "idle", ActiveChecksum: opts.BootstrapChecksum}}
		initial.Slots[0] = opts.BootstrapChecksum
		if err = w.save(initial); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&w.journal) != nil || decoder.Decode(new(any)) != io.EOF || !w.journal.valid(binding) {
			return errors.New("Quagga recovery journal or fixed instance binding invalid")
		}
	}
	if w.pending() {
		return w.restoreLocked("recovered")
	}
	base, err := w.activeConfig()
	if err != nil {
		return err
	}
	if err = w.host.validate(ctx, string(base)); err != nil {
		return err
	}
	w.child, err = w.host.start(ctx, base)
	return err
}

func (j quaggaJournal) valid(binding string) bool {
	s := j.State
	if j.Binding != binding || s.Version != 1 || j.ActiveSlot < 0 || j.ActiveSlot > 1 || !quaggaChecksum(s.ActiveChecksum) || j.Slots[j.ActiveSlot] != s.ActiveChecksum {
		return false
	}
	if other := j.Slots[1-j.ActiveSlot]; other != "" && !quaggaChecksum(other) {
		return false
	}
	if s.Status == "idle" {
		return s.ID == "" && s.Checksum == "" && s.BaseChecksum == "" && s.Deadline.IsZero()
	}
	if len(s.ID) != 48 || !quaggaHex(s.ID) || !quaggaChecksum(s.BaseChecksum) || !quaggaChecksum(s.Checksum) || s.Checksum == s.BaseChecksum || s.Deadline.IsZero() {
		return false
	}
	switch s.Status {
	case "confirmed":
		return s.ActiveChecksum == s.Checksum && j.Slots[1-j.ActiveSlot] == s.BaseChecksum
	case "pending":
		return s.ActiveChecksum == s.BaseChecksum && j.Slots[1-j.ActiveSlot] == s.Checksum
	case "staging", "cancelled", "expired", "failed", "recovered":
		return s.ActiveChecksum == s.BaseChecksum
	}
	return false
}

func (w *QuaggaWatchdog) path(name string) string { return filepath.Join(w.host.opts.StateDir, name) }
func (w *QuaggaWatchdog) slotPath(slot int) string {
	return w.path("policy-" + strconv.Itoa(slot) + ".conf")
}

func (w *QuaggaWatchdog) save(journal quaggaJournal) error {
	if err := w.host.verifyDir(); err != nil {
		return err
	}
	raw, err := json.Marshal(journal)
	if err == nil {
		err = writePrivate(w.path("transaction.json"), raw)
	}
	if err != nil {
		w.faulted = true
		return errors.New("Quagga journal persistence failed; inspect private journal before restarting")
	}
	w.journal = journal
	return nil
}

func (w *QuaggaWatchdog) pending() bool {
	return w.journal.State.Status == "pending" || w.journal.State.Status == "staging"
}

func (w *QuaggaWatchdog) activeConfig() ([]byte, error) {
	return w.readSlot(w.journal.ActiveSlot, w.journal.State.ActiveChecksum)
}

func (w *QuaggaWatchdog) readSlot(slot int, sums ...string) ([]byte, error) {
	if err := w.host.verifyDir(); err != nil {
		return nil, err
	}
	raw, err := quaggaPrivateRead(w.slotPath(slot), maxConfigSize)
	for _, sum := range sums {
		if err == nil && sum != "" && Checksum(string(raw)) == sum || os.IsNotExist(err) && sum == "" {
			return raw, nil
		}
	}
	return nil, errors.New("Quagga policy slot differs from all exact authorized bytes")
}

func (w *QuaggaWatchdog) Status() QuaggaTransaction {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.journal.State
}

func (w *QuaggaWatchdog) Stage(ctx context.Context, request QuaggaStageRequest) (QuaggaTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := w.journal.State
	if w.closed || w.faulted || w.pending() {
		return state, errors.New("Quagga unavailable or a prior transaction awaits recovery")
	}
	if request.TimeoutSeconds < 60 || request.TimeoutSeconds > 3600 || !quaggaChecksum(request.Checksum) || request.Checksum == state.ActiveChecksum || Checksum(request.Config) != request.Checksum || request.BaseChecksum != state.ActiveChecksum {
		return state, errors.New("Quagga stage requires different exact candidate and base checksums and a bounded timeout")
	}
	if err := w.host.validate(ctx, request.Config); err != nil {
		return state, err
	}
	if _, err := w.activeConfig(); err != nil {
		return state, err
	}
	if err := w.host.verify(ctx, w.child); err != nil {
		return state, err
	}
	next := w.journal
	staged := 1 - next.ActiveSlot
	if _, err := w.readSlot(staged, next.Slots[staged]); err != nil {
		return state, err
	}
	var id [24]byte
	if _, err := rand.Read(id[:]); err != nil {
		return state, err
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	next.State = QuaggaTransaction{Version: 1, ID: hex.EncodeToString(id[:]), Status: "staging", ActiveChecksum: state.ActiveChecksum, BaseChecksum: state.ActiveChecksum, Checksum: request.Checksum, Deadline: time.Now().Add(time.Duration(request.TimeoutSeconds) * time.Second)}
	if err := w.save(next); err != nil {
		return state, err
	}
	if err := w.activateLocked(ctx, staged, []byte(request.Config)); err != nil {
		if !w.faulted {
			err = errors.Join(err, w.restoreLocked("failed"))
		}
		return w.journal.State, err
	}
	return w.journal.State, nil
}

func (w *QuaggaWatchdog) activateLocked(ctx context.Context, staged int, candidate []byte) error {
	if _, err := w.readSlot(staged, w.journal.Slots[staged]); err != nil {
		return err
	}
	if err := writePrivate(w.slotPath(staged), candidate); err != nil {
		return err
	}
	if _, err := w.activeConfig(); err != nil {
		return err
	}
	config, err := w.readSlot(staged, w.journal.State.Checksum)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = w.host.stop(w.child); err != nil {
		return err
	}
	w.child, err = w.host.start(ctx, config)
	if err != nil {
		return err
	}
	if !time.Now().Before(w.journal.State.Deadline) {
		return errors.New("Quagga candidate became ready after its confirmation deadline")
	}
	next := w.journal
	next.Slots[staged] = next.State.Checksum
	next.State.Status = "pending"
	return w.save(next)
}

func (w *QuaggaWatchdog) decision(decision QuaggaDecision) error {
	if w.closed || w.faulted || !w.pending() || decision.ID != w.journal.State.ID || decision.Checksum != w.journal.State.Checksum {
		return errors.New("Quagga decision does not identify the exact pending transaction")
	}
	return nil
}

func (w *QuaggaWatchdog) Confirm(ctx context.Context, decision QuaggaDecision) (QuaggaTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := w.journal.State
	if err := w.decision(decision); err != nil {
		return state, err
	}
	if state.Status != "pending" {
		return state, errors.New("Quagga candidate readiness is unverified")
	}
	if !time.Now().Before(state.Deadline) {
		return w.confirmExpiredLocked()
	}
	if _, err := w.activeConfig(); err != nil {
		return state, err
	}
	if _, err := w.readSlot(1-w.journal.ActiveSlot, state.Checksum); err != nil {
		return state, err
	}
	if err := w.host.verify(ctx, w.child); err != nil {
		return state, err
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	if !time.Now().Before(state.Deadline) {
		return w.confirmExpiredLocked()
	}
	next := w.journal
	next.ActiveSlot = 1 - next.ActiveSlot
	next.State.Status, next.State.ActiveChecksum = "confirmed", state.Checksum
	err := w.save(next)
	return w.journal.State, err
}

func (w *QuaggaWatchdog) confirmExpiredLocked() (QuaggaTransaction, error) {
	err := errors.Join(errors.New("Quagga confirmation expired"), w.restoreLocked("expired"))
	return w.journal.State, err
}

func (w *QuaggaWatchdog) Cancel(decision QuaggaDecision) (QuaggaTransaction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.decision(decision); err != nil {
		return w.journal.State, err
	}
	err := w.restoreLocked("cancelled")
	return w.journal.State, err
}

func (w *QuaggaWatchdog) restoreLocked(status string) error {
	base, err := w.activeConfig()
	if err != nil {
		return err
	}
	next := w.journal
	inactive := 1 - next.ActiveSlot
	staged, err := w.readSlot(inactive, next.Slots[inactive], next.State.Checksum)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err = w.host.validate(ctx, string(base)); err != nil {
		return err
	}
	if _, err = w.activeConfig(); err != nil {
		return err
	}
	if err = w.host.stop(w.child); err != nil {
		return err
	}
	w.child, err = w.host.start(ctx, base)
	if err != nil {
		return err
	}
	if len(staged) != 0 {
		next.Slots[inactive] = Checksum(string(staged))
	}
	next.State.Status = status
	return w.save(next)
}

func (w *QuaggaWatchdog) Tick() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.faulted {
		return errors.New("Quagga supervisor unavailable")
	}
	if w.pending() && (w.journal.State.Status == "staging" || !time.Now().Before(w.journal.State.Deadline)) {
		return w.restoreLocked("expired")
	}
	if w.child != nil {
		select {
		case <-w.child.done:
			if w.pending() {
				return w.restoreLocked("failed")
			}
			return errors.New("committed Quagga child exited; restart supervised instance")
		default:
		}
	}
	return nil
}

func (w *QuaggaWatchdog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	err := w.host.stop(w.child)
	if err != nil && w.child != nil {
		select {
		case <-w.child.done:
		default:
			return err
		}
	}
	w.closed = true
	w.host.binary.Close()
	w.unlock()
	return err
}
