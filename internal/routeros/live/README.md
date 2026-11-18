# Fixed-target RouterOS live adapter

`live.Open(ctx, Config)` implements `control.Runtime`. It authenticates API-SSL
with certificate chain, server name and SPKI verification, and starts one pinned,
persistent SSH terminal. The context bounds startup; call `Close` to close both
channels. `Target(Config)` derives the transport binding from the actual local
pin files. A browser request cannot select transport addresses, files or trust
flags.

The private JSON configuration contains:

```json
{
  "schemaVersion": 1,
  "routerId": "router-a",
  "version": "7.24.6",
  "deviceId": "PREPROVISIONEDSYSTEMID",
  "identitySource": "license-system-id",
  "api": {
    "address": "192.0.2.1:8729",
    "serverName": "router.example.net",
    "username": "manager",
    "passwordFile": "/private/routeros/api-password",
    "caFile": "/private/routeros/ca.pem",
    "spkiSha256": "LOWERCASE_HEX_SHA256_OF_LEAF_SUBJECT_PUBLIC_KEY_INFO"
  },
  "ssh": {
    "host": "192.0.2.1",
    "port": 22,
    "username": "manager",
    "identityFile": "/private/routeros/ssh_identity",
    "knownHostsFile": "/private/routeros/known_hosts",
    "hostKeySha256": "LOWERCASE_HEX_SHA256_OF_DECODED_SSH_PUBLIC_KEY_WIRE_BYTES"
  }
}
```

These addresses and placeholder pins are examples, not usable credentials. The
configuration and credential files must be regular, singly linked files owned
by the service UID and readable only by that UID. CA and known-hosts inputs may
also be root-owned, but cannot be group/world writable. Every parent directory
is opened relative to a fixed descriptor with `O_NOFOLLOW`; the opened leaf is
checked against its original inode, owner, permissions, link count and bounded
size before and after reading. `LoadConfig` rejects unknown fields.

Private `Config.supervisor` contains the local controller's separate
`operationTimeoutSeconds` (1–30, default 10), `prepareTimeoutSeconds` (1–900,
default 600), and `maxConfirmationTimeoutSeconds` (10–300, default 300).
Zero/omitted values retain those defaults. `ixp-routeros serve` maps them to
`control.Config`; clients cannot override them on the control socket. They do
not change top-level API/SSH command, live observation or SSH lifetime limits.
See the [CLI budget example](../../../cmd/ixp-routeros/README.md) for the explicit
30/420/120 controller and 15/180/600 transport values used by the large fixture.
Production capability gates remain independent and closed.

A known-hosts file must contain exactly one literal entry for the target host
and port. Ambiguous entries, hashed names, wildcards, negation, multiple target
keys and certificate-authority markers are rejected. Only the verified target
entry and a private key copy go into a private runtime directory under `TMPDIR`.
SSH uses those copies with ambient configuration, agents, forwarding, passwords
and host-key updates disabled. The same target's API stable identifier must
match the SSH query's SHA-256 fingerprint. Supported identity sources are
`license-system-id`, `license-software-id` and `routerboard-serial`; identity
names are not device identity.

`Observe` obtains the four fixed routing menus, explicitly reads missing
properties, and uses the parent package's exact-version normalization. Unknown
values remain unknown unless the exact 7.24.6 getter semantics have been tested.
Two consecutive normalized configurations and credential observations must
agree. This detects changes during observation; it is not a device lock or an
atomic RouterOS snapshot. The returned status digest excludes credentials and
volatile operational flags. It does not assert BGP/session health.

The API transport pipelines non-sensitive missing-property getters in groups
of at most 64. Each native ID/property still needs its own tagged completion;
an unknown, missing or contradictory reply fails the entire observation. Both
complete inventory/evidence rounds remain required. Credentials stay on their
separate authorized getter path. This reduces round trips, but does not make
large inventories constant-cost or extend transaction deadlines.

A strict observation first validates the complete owned namespace and its
non-secret attributes without credentials. Only then may it read credentials
from the verified native IDs. A matching connection name alone grants no
sensitive read authority. During preparation, partial observations may read
previously verified IDs and newly acknowledged IDs whose exact names and owned
instance/table/role agree. After process restart, a partial namespace does not
automatically gain credential authority. The supervisor may issue a per-read
`CredentialAuthority` from its authenticated journal: exact native ID, exact
known peer name and the complete canonical non-secret property checksum must
match the actual observation, including owned instance/table/role. Any mismatch
aborts before the sensitive getter; this authority is not cached for later
reads. It permits observing an authenticated prepared checkpoint without
pretending that the generation is complete. Unknown credentials stay unknown.

`BindPlan` accepts only the parent canonical compiler output. `Apply` rebuilds
the exact operation against a verified API resolver, checks operation order,
phase, target, deadline and owner, and sends it once. Any uncertain response
poisons further writes. Acknowledgement is only a receipt: the supervisor must
independently verify the complete resulting state. No helper sends an arbitrary
RouterOS command or automatically retries a mutation. Activation now uses
`terminal.ExecuteOwned` on the same owned SSH stream as cancellation and
confirmation. API is independent readback for that phase: there is no API-write
fallback. Closing an SSH owner while an independent API mutation is still in
flight would otherwise leave a possible late write after rollback.

A successful full `Observe` may retain an immutable, private copy of its
non-secret raw inventory and explicit property evidence for the next planned
operation. Reuse is bound to the target, plan checksum, operation index, exact
session and ownership phase, and lasts no longer than one second or the
configured operation timeout, whichever is shorter. `Apply` consumes it even
when that attempt fails, rebuilds the canonical resolver/mutation, and still
queries the current owner and undo budget. Missing, expired or mismatched
evidence uses the original full internal read. Binding changes, owner changes,
closing, failed observations and other write paths invalidate it; a late
observation cannot publish across that invalidation epoch. Callers cannot
supply an evidence token or a flag to skip reading. This removes only the
driver's duplicate read in the original path: the supervisor retains its
complete before/after observations and final strict validation, and this evidence grants
no new authority to read or persist credentials.

The separately gated object-observation adapter can instead bind the exact
native target references from its own just-completed global checkpoint after
Safe Mode is taken. It accepts operation IDs from the canonical bound plan,
never caller-selected command paths or object selectors. Every target read
uses the fixed API `ReadObject` and independently verified identity metadata,
with two actual attribute/property/credential reads. Missing properties still
need explicit getters. Before reading a BGP secret, the exact native ID, name,
instance/table/role and complete non-secret attribute checksum must match both
the private expected target and the controller's authenticated-journal grant.

`ApplyObject` consumes a fresh one-use target proof, rechecks actual owner and
undo budget, and uses only the same owned SSH mutation stream. Unknown outcomes
are not resent, and the next operation cannot proceed before exact actual
target readback. Reference identities remain bound to the earlier global
checkpoint; no cached record is returned as a current global Snapshot.
`ObjectObservationVerified` is code-owned and defaults false. The controller
still requires complete global/Strict checks at the activation boundary and
before/after confirmation; off-target drift is detected at those boundaries,
so this is not a whole-device atomic view for every individual operation.

`ApplyRuleBatch` handles only one complete, newly created and unreferenced
generation of filter rules. It verifies the full canonical sequence once,
checks the API Safe Mode state before each write and requires durable per-item
intent/result callbacks. The supervisor then independently verifies the whole
group delta and rule order. Root rules and connection changes cannot use this
path. `BindCleanup`/`ApplyCleanup` accept only the original confirmed plan's
remaining removal suffix, protect the active configuration and require exact
retired IDs/attributes; the controller additionally checks its complete HMAC,
including credentials, before and after every removal. These two optional
capabilities retain independent native gates.

The SSH channel lifetime is bounded (default and maximum 600 seconds).
`Status.sessionExpiresAt` exposes the actual channel expiry. `Take` requires
the confirmation deadline plus an operation timeout to fit within the remaining
lifetime, including time already spent preparing. A preparation that consumes
that budget is refused before taking Safe Mode; prepared artifacts remain inert.
The runtime does not promise that a maximum-size plan fits one session, or
silently extend a taken session.

A long-running supervisor can explicitly call `ResetIdle(ctx)` only at the start
of a new transaction after its prior transaction is terminal. It requires actual
API and new SSH evidence of no active Safe Mode owner, repeats both identity
checks, and assigns a new session ID. The call never resumes an old owner.
Confirmed transactions can reset without executing optional cleanup operations;
new staging still requires a fresh baseline. Lost, cancelled or uncertain
transactions require closing this runtime and using independent read-only
recovery before any new runtime is considered. `Confirm` and `Recover` must not
call `ResetIdle`. A concurrent owner-close wins over a pending reset.

One SSH channel owns Safe Mode. `CloseOwner` never creates a replacement;
`Cancel` reports channel closure, not successful rollback. `Confirm` reports
release only; the supervisor must verify the retained configuration separately.
After the original owner closes, the same authenticated API may still read back
against the identity already proven on that pinned SSH channel. The observation
reports the owner as no longer alive. An unknown floating-undo count cannot
satisfy the activation budget.

The code-owned profile records the isolated CHR 7.24.6 API-property, SSH
owner/cancel/release, actual strict observer, post-owner-close API readback, and
new idle-session identity proofs. The live observer and idle reset passed on the
disposable CHR. The fixed dual-family bootstrap and complete supervisor
Cancel/Confirm fixtures passed with explicit local ASN, full-property predicted
readback, strict desired snapshots and zero-difference desired-plan rebuilding.
Cancellation matches the prepared configuration and opaque credentials;
confirmation retained the exact desired configuration after owner close. Those
historical fixtures used API activation writes. The current zero-prepend and
owned-SSH activation implementation is being revalidated; their old success
flags do not automatically carry over to the changed path.
These are fixed-fixture proofs, not general BGP-forwarding evidence. Separately,
the fixed SSH-process-disconnect fixture and a hand-written
single-peer IPv4 cached-route policy/rollback fixture passed; neither proves
half-open-network recovery, generated dual-family BGP semantics, or arbitrary
cached-route handling. Preparation and activation remain disabled until
the live generated-policy, undo-budget and execution gates are independently
verified. Neither configuration JSON nor a successful preview can enable them.
A local journal/flock is not an exclusive device lock. An exclusive, audited
maintenance window remains a separate operational requirement; concurrent
administrators, schedulers and other writers are not certified absent here.

Run synthetic local tests with `go test -race ./internal/routeros/live`.
`TestNativeCHRLiveObserve`, `TestNativeCHRLiveResetIdle` and
`TestNativeCHRLiveGeneratedPrepare` run only with
`MOEDOVE_CHR_NATIVE_TEST=1` in the fixed disposable CHR harness. They require zero
Linux effective/bounding capabilities, a loopback-only network, the pinned image
hash and fixed `/scratch` credentials/context. The preparation probe creates
only compiler-generated, disabled/unreferenced objects, verifies exact native
readback, and cleans up by received native IDs. It never activates peers or
claims to prove Safe Mode rollback. No native test accepts an environment host
or a production target.

The opt-in `TestNativeCHRLiveSupervisorCancel` and
`TestNativeCHRLiveSupervisorConfirm` separately exercise the complete durable
supervisor against that same fixed guest. Only their test binary overrides the
closed capability gates. Each actual write records bounded floating-undo counts
before and after. Cancellation must independently match the prepared checkpoint;
confirmation must pass a strict final snapshot and retain it after owner close.
Received-ID cleanup occurs only after those assertions. Their presence or a
synthetic test pass does not declare that these native tests have passed.

The fixed supervisor cancellation fixture has now passed with actual API writes:
preparation had no floating-undo entries, and the five activation writes each
added one entry (three filter updates, two connection enables). Cancellation
matched the prepared configuration and opaque credentials before any cleanup.
The confirmation fixture also passed with the same five observed history
entries and a strict desired snapshot retained after owner close. These results
do not certify an 80-action budget or arbitrary plans.

The independent `TestNativeCHRLiveSupervisorRestartReadOnly` fixture passed in
58.75 seconds on the same fixed CHR 7.24.6 lab. It closed the original runtime
and its connections, opened a completely new runtime with the same verified
identity source and transport pins, and authenticated the existing HMAC journal.
The prepared configuration and opaque credentials matched exactly. The old
confirmation was rejected; no bind, reset, take, apply, cancel or confirm call
reached the fresh native runtime. Cleanup occurred only after that proof.
This verifies fresh-runtime read-only recovery following that owner disconnect,
not a guest reboot, half-open connection, arbitrary process crash or a general
rollback-time guarantee. Production mutation gates remain closed.

The fixed 1,000-prefix `TestNativeCHRLiveRuleBatch1000Cancel` subsequently passed
through the repository harness on CHR 7.24.6: 2,004 generation rules were added
in one journaled batch, Stage took 265,637 ms, and the full test took 366.42 s.
The original 90-second confirmation window was unchanged. Stage used 43 full
Inventory calls and 16 actual object plus 16 identity reads; the final Strict
global proof passed, and cancellation matched the complete prepared checkpoint
and opaque credentials before cleanup. Three normal preparation writes reused
the private one-use observation with zero internal Inventory calls, taking
207/202/289 ms. Stage includes preparation; it is not a 90-second total-stage
latency claim. This case does not prove bulk BGP UPDATE throughput or large-plan
Confirm behavior.

An earlier run of the same layered path failed a combined pre-write owner
guard. Splitting only diagnostic codes produced the subsequent successful run,
without changing behavior or the one-second proof bound, so that earlier cause
remains unclassified. No guessed renewal or wider timeout was introduced.
The rule-batch/object-observation and production mutation gates stay closed;
the exact evidence and remaining boundaries are recorded in
[the native results](../NATIVE-7.24.6.md).
