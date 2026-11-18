# RouterOS durable supervisor

This Linux package implements the local transaction state machine. It does not
serve HTTP, accept remote transport targets, execute scripts or turn structural
RouterOS plans into native capability claims. The real runtime binds one private
configuration, exact target/version and independently tested adapter profile.
Its BGP activation gates must remain false until those semantics are proved.

## API

Open(Config, Runtime) locks one private 0700 directory and binds it permanently
to Config.Target. Stage(ctx, Request) accepts a full parent Plan, locally resolved
Credentials/BaselineCredentials, the local KnownState, a bounded confirmation
timeout and an explicit ExclusiveAccessAcknowledged precondition. It validates
the full plan and every bridge command before any write.

Confirm(ctx, transactionID, planChecksum) requires the same live owner session,
an unexpired deadline, actual desired-state read-back and proof that the owner
was released. Cancel(ctx, transactionID) uses only that original session. Recover
does observation only: it never calls Take, Apply or Confirm. Status is a
transaction receipt; verifiedAt identifies its actual verification time and does
not describe present BGP health.

One service can handle subsequent explicitly reviewed transactions after a
terminal result. It cannot overwrite a pending/uncertain journal or change the
target. Stage does not accept a runtime address, executable, API path or command
string. The runtime receives only a bridge-produced operation in the canonical
plan and independently verifies its fixed schema before transport.

If the runtime implements ResetIdle, only a new Stage after a terminal result
may call it, after plan/native-gate preflight and before binding the new plan.
The runtime must independently prove inactivity and identity on both channels,
and reject its old taken/lost/poisoned authority. Pending Stage, Confirm and
Recover never reset a channel. A new explicitly authorized transaction is not
permission to confirm its predecessor.

The local file lock excludes other instances using the same configured journal
directory. **It does not lock the router.** The operator must arrange exclusive
device configuration access for the transaction. Every activation write checks
the actual current owner and floating-undo count, reserves at least the number of
attributes plus one action (or the plan's larger estimate), and refuses unknown
counts or a result above 80. Concurrent changes in the observed routing menus are
detected; no claim is made that count-only observations identify every other
writer or provide an atomic whole-device snapshot.

## State and durability

The normal path is:

    preparing -> prepared -> taking-safe-mode -> activating
      -> awaiting-confirmation -> confirming -> confirmed

Cancellation or loss of the owner follows:

    cancelling -> rollback-pending -> rolled-back
                                  -> uncertain

Preparation creates only the parent plan's disabled/unreferenced objects.
Every mutation has an fsynced intent, result record and independent read-back
comparison. The controller predicts the exact allowed object change from the
typed API operation; an ACK or a driver's matched=true flag is not sufficient.
Missing IDs, unexpected extra objects, changed external routing configuration,
unknown credentials and extra field changes stop execution.

The optional `ObjectObservationVerified` adapter uses a separate two-level
activation path; the production capability remains false until independently
validated. After taking Safe Mode, a complete global observation must still
match the prepared journal checkpoint before the first activation write.
Only already-approved in-place instance/connection/entry-rule changes qualify:
`Set`, connection AF-limit `Unset`, and connection enable/disable. Add/remove,
table/FIB operations, new generations, identity/chain moves and cleanup do not
use this path. An ineligible plan retains the original complete-read path.

Each eligible operation obtains actual full target attributes and, for a BGP
connection, its authorized opaque credential before and after the owned SSH
write. `ObjectObservation` has no Inventory, Snapshot or Resolver. The journal
advances only the exact verified object delta, recording it as
`object-only-readback`; this is an expected global state, not a claim that the
rest of the device was just observed. The final desired Strict observation,
and the complete observations before and after confirmation, still compare
all controlled objects, credentials, filter-chain order and external-state
HMACs. An off-target change may therefore be detected at the next global
boundary rather than at the next target read. Before release, any mismatch
cancels the transaction; after release it remains uncertain. Exclusive device
access is still required, and no per-step whole-device atomicity is claimed.

The rollback checkpoint is captured **after preparation and before Safe Mode**.
Cancellation therefore verifies that checkpoint, retaining prepared disabled
connections and unreferenced generations. It must not require those objects to
disappear. After confirmation, the same process automatically executes only the
original plan's authorized Cleanup suffix when the independently verified cleanup
adapter is available. Cleanup has a separate pending/running/uncertain/complete
status and cursor; the committed phase remains confirmed. Cleanup failure never
calls Cancel, Take or the pre-commit failure path.

A restarted service or Recover remains read-only. Cleanup(ctx, transactionID,
checksum) continues the original approved suffix after current state, credentials
and released ownership are independently verified; it does not request another
configuration approval or rebind the plan from operation zero. A lost Remove
reply is reconciled by observation: exact absence can advance the cursor, while
a still-present or changed object is never automatically deleted again. A final
Strict snapshot uses the generation/peer manifest after the actual deletions.

The deadline and cancellation authorization are durable before activation.
An independent timer can cancel the original channel even while an operation
holds the state mutex. Cancellation owner checks use a fresh bounded context,
not an expired confirmation deadline. A timer from an earlier transaction cannot
close a later owner session. Unknown writes are never retried automatically.
If the same owner cannot be proved, only that local channel is closed and the
state remains pending until an independent read proves rollback.

On process death the service cannot run its timer. A process supervisor must
restart it; the reopened instance performs only read-back reconciliation.
RouterOS's own disconnect rollback can take time and is not a bounded rollback
guarantee from this package. A new terminal session is never allowed to confirm
the old transaction. Recovery may record confirmed only when a durable confirm
intent already exists and desired state plus released ownership are observed.
Otherwise matching prepared state is needed for rolled-back; unknown state stays
uncertain/pending.

Recovery credential authority comes only from authenticated journal objects:
exact internal ID, generated name and a checksum of all normalized non-secret
connection attributes. Current and prepared rollback states are tried separately
with bounded read-only observations. A name/prefix or frontend assertion grants
no access. Old generations remain explicit; old peers retain baseline
authentication in the prepared manifest, while the desired manifest promotes the
reviewed revisions.

Before awaiting confirmation and on both sides of release, a complete Strict
desired snapshot is required. A missing snapshot is not success. The new
generation and all retained generations are represented, and rule order within
every observed chain enters the state fingerprint.

Status publishes immutable atomic snapshots after durable phase/intent changes
and explicit failures. Readers do not wait behind a long preparation or API call.
Cancellation still follows the serialized transaction path; status responsiveness
is not a claim that all commands are interruptible.

An interrupted preparation whose completed writes and exact inert state can
still be proved is preparation-failed, with artifacts retained. A bootstrap
cancelled after preparation can leave reject-only roots: this is not silently
adopted as a new active generation. Reconciliation/cleanup must be reviewed.

## Private journal

The fixed files are:

- target.json: immutable target binding, 0600.
- journal.key: random 32-byte local HMAC key, 0600.
- journal.wal: bounded, append-only, hash-chained HMAC records, 0600.
- control.lock: lifetime exclusive advisory lock, 0600.

All files use directory-relative opens with no final symlink following, require
the current owner and one hard link, and are fsynced before dependent actions.
Frames have strict JSON schemas and sequence/link checks. The WAL is capped at
128 MiB, a record at 32 MiB, and Stage reserves capacity before beginning. It
does not delete old records or pending work to free space.

New unreferenced generation filter additions may use the optional verified
RuleBatchRuntime. Stable entry roots, connections, tables and instances never use
that path. One logical generation batch has its own start record, ordered
per-operation fsynced intent/result records and one complete difference proof.
The controller does not overwrite a single lastIntent with many outstanding
actions. Before the group is accepted it verifies exact new IDs/bodies/flags,
chain order, and unchanged prior objects/external state. Unknown results stop
the writer, never resend a rule, and never enter activation.

The authoritative state advances only after the group read-back record is
durable. Recovery can reconcile the already attempted inert prefix by observation
without repeating additions. Grouping every fixed small chunk with a full
inventory would still have quadratic growth; the logical generation has a
bounded total size and one before/after full reconciliation. The live adapter
must independently enforce the unreferenced-chain and exact-bridge constraints.

Neither passwords nor raw sensitive API results enter the WAL. Authentication is
stored as a keyed fingerprint, so ordinary password hashes are not exposed for
guessing. Foreign configuration is represented only by keyed digests. The
controller's ordinary JSON/fmt representations redact raw observations and
credential-bearing requests. Baseline and desired passwords are supplied in
memory; the journal does not create another plaintext password store.

An incomplete or invalid frame is not truncated or repaired automatically.
The directory becomes recovery-only and cannot accept new writes. Keep the
journal key with an offline encrypted backup of this directory; losing the key
means its fingerprints and log authenticity cannot be verified. Only archive or
replace the directory after a verified terminal result and explicit operational
review, never to bypass a pending transaction.

## Validation performed here

The fixtures are an in-memory device model with fixed API records, not native
BGP validation. Tests cover durable confirmation, prepared-state rollback,
healthy cancellation after expiry, lost ACK without retry, restart without
session adoption, lost confirm reply, journal write failure before mutation,
unknown/exhausted history, private permissions, torn/authenticated log records,
multiple transactions, concurrent Stage calls, unexpected side effects and
credential fingerprints. The actual observer/transport and native SSH/Safe Mode
tests remain independent evidence.

Earlier measured small native fixtures had two address families and about
thirteen filter rules; the ordinary per-operation path took roughly a minute.
The new grouped path and post-confirm cleanup have separate fixture/native gates.
Passing the small ordinary lifecycle does not establish large-scale throughput,
batch semantics or cleanup behavior. Runtime limits and measured native results
remain the boundary; no thousand-rule deployment claim follows from unit tests.

    GOPROXY=off go test -race ./internal/routeros/control
    GOPROXY=off go vet ./internal/routeros/control


Prepared discard supports explicit bounded continuation through
`DiscardRequest.MaxBatches` (1..16). Zero retains legacy internal call behavior;
the product CLI/socket defaults to one. The counter spans the whole request and
includes a single-object fallback as one unit. A normal early return is allowed
only after actual global readback, credential/HMAC checks and durable cursor
commit, with no pending intent or batch. It returns `pending` and
`discard-awaiting-explicit-continuation`; no background worker continues it and
Review remains unavailable. Fresh Open/Recover is read-only and the next suffix
requires another explicit request. The final unit still runs complete original
baseline/Strict proof. Unknown effects, drift and failed WAL remain errors.
