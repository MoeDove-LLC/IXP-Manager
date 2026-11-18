package live

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

// ResetIdle creates a NEW unowned SSH session, never a replacement owner. A
// supervisor may call this only when starting a new transaction after its prior
// transaction is terminal. Confirm and Recover must not call it. Lost, cancelled
// or uncertain sessions are deliberately ineligible; recover with a new runtime.
func (r *Runtime) ResetIdle(ctx context.Context) error {
	r.invalidateObjectActivation()
	defer r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	ctx, done, e := r.begin(ctx)
	if e != nil {
		return e
	}
	defer done()
	defer r.invalidateWriteEvidence()
	if r.taken || r.poisoned || r.removalTail != nil || !r.cleanupFinished() || !r.preparedDiscardFinished() || r.bridge != nil && !r.completed || r.dialSSH == nil {
		return fail("idle-session-reset-not-permitted")
	}
	before, e := r.api.Inventory(ctx)
	if e != nil || before.SafeMode == nil || before.SafeMode.Enabled || before.SafeMode.Current {
		return fail("idle-session-api-owner-not-clear")
	}
	device, ok := observedDevice(before, r.config.IdentitySource)
	if !ok || device != r.target.DeviceID || len(before.Resources) != 1 || !validVersion(before.Resources[0]["version"], r.target.Version) {
		return fail("idle-session-api-identity-mismatch")
	}
	r.ownerMu.Lock()
	e = r.closeOwnerLocked()
	epoch := r.ownerEpoch
	r.ownerMu.Unlock()
	if e != nil {
		return fail("idle-session-old-channel-close")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	expires := time.Now().Add(time.Duration(r.config.SessionLifetimeSeconds) * time.Second)
	candidate, e := r.dialSSH(lifetime)
	if e != nil {
		cancel()
		return fail("idle-session-ssh-connect")
	}
	valid := false
	defer func() {
		if !valid {
			cancel()
			_ = candidate.Close()
		}
	}()
	if _, e = candidate.Synchronize(ctx); e != nil {
		return fail("idle-session-ssh-synchronize")
	}
	owner, e := candidate.OwnerEvidence(ctx)
	if e != nil || owner.Closed || !owner.OwnerQueryAvailable || owner.Owner != terminal.OwnerNone {
		return fail("idle-session-ssh-owner-not-clear")
	}
	after, e := r.api.Inventory(ctx)
	if e != nil || after.SafeMode == nil || after.SafeMode.Enabled || after.SafeMode.Current {
		return fail("idle-session-api-owner-changed")
	}
	// Serialize the install against Close/CloseOwner. Any concurrent shutdown
	// wins; it never silently leaves a new terminal alive behind the supervisor.
	r.ownerMu.Lock()
	if r.closed.Load() || r.ownerEpoch != epoch {
		r.ownerMu.Unlock()
		return fail("runtime-closed")
	}
	r.terminal = candidate
	r.cancel = cancel
	r.ownerClosed.Store(false)
	r.ownerMu.Unlock()
	r.sshIdentityVerified = false
	r.sessionExpiresAt = expires
	if _, e = r.identity(ctx, after); e != nil {
		_ = r.CloseOwner()
		return e
	}
	if r.ownerClosed.Load() || candidate.Snapshot().Closed {
		return fail("idle-session-closed-during-reset")
	}
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		_ = r.CloseOwner()
		return fail("idle-session-randomness")
	}
	r.sessionID = hex.EncodeToString(nonce)
	r.deadline = time.Time{}
	r.bridge = nil
	r.boundPlan = nil
	r.operations = nil
	r.cleanup = nil
	r.operationIndex = 0
	r.completed = false
	r.mu.Lock()
	r.status = Status{Target: r.target, Profile: r.profile, LastCode: "new-unowned-session", SessionExpiresAt: expires}
	r.mu.Unlock()
	valid = true
	return nil
}
