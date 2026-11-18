# RouterOS local supervisor CLI

`ixp-routeros` connects one private, fixed target to the RouterOS observer and durable controller. Transport addresses, TLS/SSH trust and credential-file paths are configured at service startup. Requests on the local socket cannot select a different target or supply commands.

The live adapter owns native capability gates. There is no flag to override an unverified preparation or activation gate. The current deployment workflow is still being validated; `capabilities` reports the exact current evidence and blockers. The manager's RouterOS platform remains preview-only.

```sh
ixp-routeros capabilities
ixp-routeros inspect --config /etc/moedove/routeros.json
ixp-routeros serve --config /etc/moedove/routeros.json \
  --state /var/lib/moedove/routeros/router-one
ixp-routeros status --socket /var/lib/moedove/routeros/router-one/control.sock
ixp-routeros review --socket /var/lib/moedove/routeros/router-one/control.sock
```

Private configuration follows `live.Config`: fixed RouterID, exact version, stable device ID/source, literal API/SSH targets, API CA and SPKI pin, SSH identity and expected host-key pin. Credentials remain in private local files. The observer verifies the two channels' stable device identities and versions; a system display name is not identity evidence. See [live adapter](../../internal/routeros/live/README.md) for its current fields and limits.

The optional private `supervisor` object configures controller budgets at service
startup. Its `operationTimeoutSeconds` is 1–30 (default 10),
`prepareTimeoutSeconds` is 1–900 (default 600), and
`maxConfirmationTimeoutSeconds` is 10–300 (default 300). Omitting a field or
setting it to zero preserves that existing default. Unknown fields and values
outside these bounds are rejected before connecting. Stage requests cannot
override these service settings.

Set `supervisor.manualCleanup` to the JSON boolean `true` to leave confirmed
cleanup for an explicit operator request. The default `false` keeps automatic
cleanup. This scheduling policy is read from the private service config.

For the already exercised 1,000-prefix library fixture, the corresponding
explicit private settings are:

```json
{
  "operationTimeoutSeconds": 15,
  "observationTimeoutSeconds": 180,
  "sessionLifetimeSeconds": 600,
  "maxObjects": 10000,
  "maxGetterCalls": 50000,
  "supervisor": {
    "operationTimeoutSeconds": 30,
    "prepareTimeoutSeconds": 420,
    "maxConfirmationTimeoutSeconds": 120
  }
}
```

Merge these settings with the required fixed-target configuration. The top-level
15-second limit still bounds each API/SSH command; the supervisor's 30-second
limit bounds a controller operation, including a complete observation. The
180-second live observation limit does not bypass the shorter enclosing budget.
Preparation is bounded separately and does not extend a request's confirmation
window: a request specifying 90 seconds still has that same window starting at
Safe Mode acquisition. The SSH lifetime also remains 600 seconds. Seven minutes
of preparation plus a two-minute confirmation allowance and one 15-second
command leave 45 seconds for other setup, but elapsed time remains authoritative:
`Take` rejects a deadline that no longer fits the actual session lifetime.
No automatic owner replacement or timeout expansion is implied. In particular,
the default 600-second preparation upper bound is not a promise that a full
600 seconds can elapse and still leave time for confirmation. These settings
wire the service to the tested budgets; they do not claim that a real CLI
large-plan fixture or the remaining native publication gates have passed.

`inspect` requests a strict, read-only snapshot and reports status plus that verified snapshot. For an existing managed namespace, supply `--known /private/manifest.json` and, when needed, `--baseline-credentials /private/credentials.json`. Unknown or partial state is rejected instead of being returned as an empty trusted baseline. Inspection does not stage or take Safe Mode.

`review` reads through the running supervisor and returns the private generation
manifest together with a fresh strict snapshot. After confirmation and completed
cleanup it obtains that manifest from the authenticated journal, so operators do
not have to reconstruct it by hand. It verifies actual state and credential
fingerprints, returns no password values, and performs no write, owner reset or
takeover. A pending transaction, incomplete cleanup or discard, damaged journal
or state drift is refused; status remains available during a long operation.
The returned snapshot is planning input, not permission to execute a plan.

After a verified rollback of an update to an already managed namespace, a fresh
`serve` can also review the complete prepared checkpoint before discarding it.
The result retains the original active generation and credential revisions,
plus the authenticated unreferenced prepared generation and any new disabled
peers, so a later reviewed plan can reuse those rules. The new unowned SSH
session must differ from the cancelled owner and have a known zero undo count;
the old process is not automatically reset. A cancelled bootstrap's reject roots
are not an empty trusted baseline and remain unavailable for planning until the
separate discard finishes.

After `preparedDiscard.phase` is `complete`, review uses precisely the original
manifest and baseline, checks their current complete state and the original base
snapshot checksum, and can return either that old managed baseline or a verified
empty bootstrap namespace. A pending, running or uncertain discard is never
treated as a new planning baseline. Starting another transaction still has its
own fresh-runtime and native capability requirements; review does not revive an
old confirmation.

`plan` turns that private review response and a small desired-state file into a
canonical stage file. It runs offline, preserves an existing peer's
authentication reference when `authentication` is omitted, and requires an
explicit disabled reference to clear it. Supplying a new non-empty credential
selects its revision; contradictory or unused credentials are rejected.

### Plan from IXP Manager

Open a current RouterOS draft in the configuration center and select the
RouterOS planning download. The JSON contains the router inventory ID, draft
review identifier, enabled peers, limits, prefix/origin policy, and explicit TCP
MD5 revision references. The service rechecks the draft against its current
routing inputs and encrypted artifact before download.

```sh
ixp-routeros review --socket /var/lib/moedove/routeros/router-one/control.sock \
  --output /private/review.json
ixp-routeros plan --review /private/review.json \
  --handoff /private/routeros-handoff-DRAFT.json --output /private/reviewed-stage.json
```

The local planner compares the inventory ID with the fixed target in the review
and checks the downloaded intent digest. An empty namespace proven by the
review selects bootstrap. The default confirmation window is 60 seconds.
`--timeout` can select a window from 10 to 300 seconds. Changing the local ASN
or BGP router ID requires `--allow-identity-change`. The output records exclusive
access only if the operator explicitly specifies `--exclusive-access`.

Use `--credentials /private/credentials.json` for a separate owned `0600` file
with `credentials` and `baselineCredentials` maps, both keyed by peer ID. Each
entry carries `revision` and `password`. New or changed keys require the local
password for the exact exported revision. The handoff contains no passwords
and the tool never asks for them on the command line. Treat the intent digest
as a transfer integrity check and obtain the file directly from the trusted
configuration center. The local supervisor remains responsible for target
identity, current device state and execution capability checks.

### Plan from a manually prepared intent

```sh
ixp-routeros review --socket /var/lib/moedove/routeros/router-one/control.sock \
  --output /private/review.json
ixp-routeros plan --review /private/review.json --file /private/desired.json \
  --output /private/reviewed-stage.json
```

For a new empty namespace, `desired.json` has this shape:

```json
{
  "asn": 64500,
  "routerId": "192.0.2.1",
  "localIpv4": "198.51.100.1",
  "peers": [{
    "id": "peer-a", "asn": 64501, "address": "198.51.100.2",
    "maxPrefixes": 100, "allowedPrefixes": ["203.0.113.0/24"]
  }],
  "options": {"bootstrap": true},
  "timeoutSeconds": 60,
  "exclusiveAccessAcknowledged": false
}
```

Updates use `options.bootstrap=false`. `options.allowIdentityChange` is required
for a reviewed local ASN/router-ID change. The optional private `credentials`
and `baselineCredentials` maps use `{revision,password}` per peer; existing
authenticated baselines still require their expected private values at Stage.
The operator supplies `exclusiveAccessAcknowledged` according to the actual
maintenance window. Planning does not assert that condition automatically.
Output is atomically created with mode0600 in an owned directory, never
overwrites a file, and stdout contains only checksums/counts. Stage independently
rechecks the target and baseline; editing a review cannot bypass those checks or
the native capability gates.

`serve` takes the controller's exclusive state-directory lock, then exposes only `control.sock` with mode0600 inside a mode0700 directory. Both sides verify Unix peer credentials match the service UID. Existing non-socket or foreign-owned paths are refused. One bounded JSON document is accepted per connection; duplicate keys, unknown envelope keys, case aliases and excessive nesting are rejected. There are no arbitrary host/path/command arguments. The controller writes private integrity-protected journal records before mutations and validates readback independently.

When the exact-version native gates are available, a local operator can submit a private0600 stage file:

```sh
ixp-routeros stage --socket /var/lib/moedove/routeros/router-one/control.sock \
  --file /private/reviewed-stage.json
ixp-routeros confirm --socket /var/lib/moedove/routeros/router-one/control.sock \
  --id TRANSACTION_ID --checksum EXACT_PLAN_CHECKSUM
ixp-routeros cancel --socket /var/lib/moedove/routeros/router-one/control.sock \
  --id TRANSACTION_ID
ixp-routeros recover --socket /var/lib/moedove/routeros/router-one/control.sock
ixp-routeros cleanup --socket /var/lib/moedove/routeros/router-one/control.sock \
  --id TRANSACTION_ID --checksum EXACT_PLAN_CHECKSUM
```

The stage document contains `plan`, `knownState`, `credentials`, `baselineCredentials`, `timeoutSeconds` (10–300), and `exclusiveAccessAcknowledged`. Credential maps are keyed by peer ID with `{revision,password}` values. Only this private input and local socket carry actual passwords; normal formatting and public output do not. They are resolved in memory, and the controller journal uses keyed fingerprints rather than plaintext or guessable password hashes. Do not put credentials in command arguments or logs.

Responses report the transaction's actual status and verification flags. A lost response is an unknown outcome: inspect status, then use recovery; do not automatically resend a write or toggle Safe Mode. Recovery never takes over or confirms a lost owner session. Confirmation is valid only for the exact pending transaction/checksum and its live owner. Prepared artifacts may remain after cancellation and require explicit reconciliation; cancellation does not silently delete unrelated objects.

After an interruption, `status` or `recover` may report
`unconfirmed-candidate-retained`. The latest authenticated readback matched the
complete pending candidate and the old Safe Mode session was released. The
supervisor keeps the transaction in `rollback-pending`, with both verification
flags false. Inspect the device and the original baseline in an exclusive
maintenance window before proceeding. `review` remains blocked until the
baseline is verified again.

## Restoring a retained candidate to the prior policy

When a fresh supervisor proves a retained candidate and the journal still
contains its previous managed policy, `restore-review` derives a read-only
restoration plan. Start `serve` with the same fixed target and journal after
the old process has exited. Use the transaction ID and plan checksum reported
by `status` or `recover` for the current recovery obligation.

```sh
ixp-routeros restore-review \
  --socket /var/lib/moedove/routeros/router-one/control.sock \
  --id CURRENT_TRANSACTION_ID --checksum CURRENT_PLAN_CHECKSUM \
  --output /private/restore-review.json

ixp-routeros restore-stage \
  --socket /var/lib/moedove/routeros/router-one/control.sock \
  --id CURRENT_TRANSACTION_ID --checksum CURRENT_PLAN_CHECKSUM \
  --restore-checksum REVIEWED_RESTORE_CHECKSUM \
  --exclusive-access-acknowledged --timeout 60
```

Review the plan before opening an exclusive maintenance window. The saved
response is an owned `0600` file, created without replacing an existing file.
The response identifies the original transaction in `status`, the reviewed
plan in `restoreReview`, and the new plan checksum in `restoreChecksum`. With
`--output`, stdout retains the references and reports `reviewSaved`.

Add `--file /private/restore-credentials.json` when the plan requires peer
credentials. Use the same owned `0600` credential bundle as for planning from
a handoff. `baselineCredentials` supplies the retained candidate's peer
credentials, and `credentials` supplies the reviewed prior policy's peer
credentials as needed. Each entry is keyed by peer ID and contains `revision`
and `password`.

The staging request carries the original transaction binding, the distinct
restore checksum, the exclusive-access acknowledgement, the timeout and any
private credential maps. The supervisor reconstructs the plan from its journal
and rechecks device state and native capability gates. The confirmation window
defaults to 60 seconds and accepts 10–300 seconds within the service limits.
Successful staging returns a new transaction in `awaiting-confirmation`, with
`restoreOf` referring to the transaction supplied to `restore-stage`. To confirm
it, use `confirm` with the new ID and its checksum. If the response is lost, use
`status` or `recover` before taking further action.

If the new restoration transaction is cancelled and its rollback is verified,
`rollbackVerified` refers to that restoration transaction. The device returns
to the still-unconfirmed candidate. For the next `restore-review`, start a fresh
supervisor and use the new restoration transaction's ID and plan checksum from
`status`, along with a fresh exclusive maintenance window before staging again.

## Discarding retained preparation after verified rollback

`discard-prepared` is a separate maintenance action for the preparation retained
by a cancelled transaction. It requires the **original transaction ID and plan
checksum**, a verified rollback, and a new explicit acknowledgement that the
maintenance window is exclusive. It accepts neither a new plan nor a caller's
list of native object IDs or commands. The controller derives the eligible
objects from its authenticated journal and checks actual target state.

After `status`/read-only recovery proves the rollback, stop the old `serve`
process and start a fresh `serve` using the same private configuration and state
directory. Cancellation can leave the old live adapter unusable; this command
does not silently reset or reconnect it. The state directory remains exclusively
locked, so only one service instance may run at a time.

```sh
# Start a fresh service after the previous one has exited.
ixp-routeros serve --config /etc/moedove/routeros.json \
  --state /var/lib/moedove/routeros/router-one

# In another terminal under the same service UID, acknowledge the new window.
ixp-routeros discard-prepared \
  --socket /var/lib/moedove/routeros/router-one/control.sock \
  --id ORIGINAL_TRANSACTION_ID --checksum ORIGINAL_PLAN_CHECKSUM \
  --exclusive-access-acknowledged --max-batches 1
ixp-routeros status --socket /var/lib/moedove/routeros/router-one/control.sock
```

The socket request has exactly the action-specific authority below. Both IDs are
canonical lowercase hexadecimal strings (32 and 64 characters respectively),
and the acknowledgement must be an explicit boolean `true`:

```json
{
  "version": 1,
  "action": "discard-prepared",
  "transactionId": "0123456789abcdef0123456789abcdef",
  "checksum": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "exclusiveAccessAcknowledged": true,
  "maxBatches": 1
}
```

The hexadecimal values above are illustrative; use the original values reported
by the supervisor for the transaction being reconciled.

`--max-batches` / `maxBatches` accepts 1..16 and defaults to **one** for both CLI
and older socket envelopes. A unit is a fully verified generation-rule batch,
or one verified object removal when batching is unavailable or inapplicable.
It limits work in this request; it does not increase any deadline or authority.
Explicit zero, null, duplicate keys and use on other actions are rejected.

A successful bounded response can still be incomplete. Only after the actual
full tail and durable progress record may it return `preparedDiscard.phase =
"pending"` with `discard-awaiting-explicit-continuation`. The response reports
`已验证 X/Y，尚未完成；不会在后台自动继续` and provides `continueCommand` as
an argument array, retaining the original transaction/checksum and work limit.
Reconfirm the exclusive maintenance window, then explicitly run that command.
The partial state is not a valid Review or a new publication baseline.

After stopping the service at a verified boundary, a fresh service can run
read-only `recover` against the same journal, then await the next explicit
`discard-prepared` request. Recovery never sends Remove. An unknown result,
drift or failed journal write does not become a normal bounded return and does
not provide a retry command. Final `complete` still requires the exact original
baseline, credentials and Strict snapshot; acknowledgement counts alone do not
complete cleanup. The internal Go API preserves zero-limit legacy callers,
but the product socket never uses that compatibility mode.

Discard progress appears separately in `status.preparedDiscard`. The original
`rolled-back` phase and rollback verification remain facts about cancellation;
they are not replaced with a confirmation or repurposed as confirmed-plan
cleanup. `cleanup` remains the action for a successfully confirmed plan. A lost
discard response or uncertain removal still requires status and read-only
reconciliation; do not automatically repeat the write.

The live adapter's new native discard gate defaults to false and cannot be
overridden by configuration, this acknowledgement, or any CLI option. The
service budgets and command deadlines described above remain unchanged.

## Confirmed-plan cleanup

After a verified confirmation, the service runs the original plan's cleanup
automatically when its native gate is available and `supervisor.manualCleanup`
is omitted or `false`. With `manualCleanup: true`, `confirm` and `status` can
report verified progress awaiting an explicit cleanup request. Cleanup keeps
the recorded confirmation and advances its own progress separately.

`cleanup` without a limit still handles the remaining suffix of the confirmed
plan. Use `--max-batches` for an explicit limit from 1 to 16.

```sh
ixp-routeros cleanup --socket /var/lib/moedove/routeros/router-one/control.sock \
  --id CONFIRMED_TRANSACTION_ID --checksum EXACT_PLAN_CHECKSUM --max-batches 2
```

The limit counts fully verified rule-removal batches or verified single-object
removals. It leaves the service deadlines in effect. A successful limited
response with work remaining reports `cleanup.phase = "pending"` and
`cleanup.lastCode = "cleanup-awaiting-explicit-continuation"`. The CLI includes
the next `continueCommand` as an argument array containing the same socket,
confirmed transaction ID, plan checksum and limit. The service waits for that
operator request at the verified boundary.

The socket envelope still uses version 1. `maxBatches` is an optional integer
for `cleanup` and uses the same range as the flag. Omitting the key retains the
legacy cleanup request. `discard-prepared` keeps its separate default of one
work unit and its own exclusive-maintenance acknowledgement.

After a process restart, `recover` reconciles the last known outcome by reading
the exact target. For an unknown outcome or failed request, first inspect the
returned state and recover read-only before issuing a further cleanup request.

The source includes socket/UID, input privacy, unknown-field and native-gate regression tests. Native semantics are separately exercised by the [isolated CHR harness](../../tools/testing/routeros/README.md). Neither ordinary unit tests nor a successful SSH connection prove complete BGP publishing, data-plane behavior or failure recovery.
