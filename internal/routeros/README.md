# RouterOS typed generation plans

This package builds and compiles bounded, reviewable RouterOS BGP plans. It opens
no connection, executes no command, and changes no capability in the existing
RouterOS preview renderer. A successful Go test or Compile call is **structural
validation only**. NativeValidated and Budget.Verified are always false.

The object model begins at RouterOS 7.20, which introduced explicit BGP instances.
Each plan nevertheless names one exact stable version. A plan for 7.24.6 is not
evidence that 7.20, every 7.24 release, or another version behaves identically.

## Public contract

- BuildPlan(target, routing.Router, []routing.Peer, observed Snapshot,
  PlanOptions) produces a canonical Plan.
- ValidatePlan(plan) regenerates the closed operation list and compares the
  complete plan. Changing a rule, resource kind, safety flag or operation and
  recomputing its checksum does not bypass this check.
- Compile(plan, Credentials) produces fixed Resource/Action/Attributes commands.
  It has no arbitrary command/path/shell facility and performs no I/O.
- OwnerName(routerInventoryID) derives the dedicated namespace.
- BuildSnapshot(api.Inventory, KnownState, CredentialEvidence, TargetEvidence)
  strictly converts completed fixed API reads and a private local manifest to a
  Snapshot. It has no network or mutation side effects.
- ObservedAttributesChecksum(resource, record) canonicalizes explicit booleans
  and hashes only the closed, non-secret read dictionary.
- NewAPIBridge(plan, credentials) compiles and preflights all operations against
  the API transport's closed schema. Operations returns safe descriptors;
  Mutation(operationID, resolver) accepts only an operation in that plan.
- NewAPIResolver(inventory, target, evidence) privately copies completed fixed
  reads from the authenticated target. It resolves exact menu/name or
  menu/chain/comment matches to one observed internal ID.

Target includes the inventory ID, exact RouterOS version, stable device ID and
transportIdentitySha256. The last field is the canonical digest of the trusted
local transport configuration: literal endpoints, API and SSH ports, usernames,
API TLS SPKI pin **and** SSH host-key pin. It is not either individual pin.
A mutable system identity/name cannot establish that API writes and an SSH
Safe Mode owner control the same device. That evidence belongs to the target
inspector; unknown crossChannelIdentityVerified remains an execution blocker.

Plan includes desired typed inputs, owner, generation, policyChecksum,
baseChecksum, checksum, authentication revisions, operation phases and an
activation estimate. It carries no password. Credentials are resolved separately
by peer ID plus the exact revision. The shared portable password validation is
used. Deleted authentication retains its tombstone revision and emits an explicit
empty tcp-md5-key during activation for an existing connection. Every newly
created disabled connection explicitly initializes its key in preparation,
including the empty no-auth value. Both states are bound to review.

Compiled Attribute.Value intentionally remains accessible to the transport in
memory. JSON and ordinary fmt formatting redact sensitive values, and the name
tcp-md5-key forces redaction even if a caller sets Sensitive=false. Redacted JSON
is a review representation, not executable input. Do not log Value directly or
serialize raw transport frames.

The API bridge preserves every attribute byte and does not add CLI quotes or
escape dollar signs. Native 7.24.6 rejected an empty AF-limit Set; the compiler
now emits a separate typed Unset with Property naming the unused address-family
limit. Its native unset/read-back gate remains explicit. On that version both
API fib=no and fib=yes enabled the FIB switch. Table creation therefore omits fib
entirely and requires independently verified non-FIB read-back. Credential
clearing remains an explicit empty Set, whose getter result must be known.
Connection creation/update explicitly sets routing-table to the same owned table
as the instance. The tested target did not inherit the instance table as assumed;
inspection therefore requires both layers to match exactly, and the bridge keeps
an explicit read-back gate for that binding.
Connection Add/Set also specifies connect=yes and listen=yes while keeping
disabled=yes during preparation/configuration changes. An unset getter for either
flag remains unknown; the inspector does not infer these intended values.

All bridge results retain nativeValidated=false and require target and state
verification. Reference resolution is not baseline/CAS verification: original
rule hashes can legitimately change during an ordered activation, so a resolver
does not treat an old baseline hash as proof of each intermediate state. The
executor must verify those states separately. An Add requires absence, a later
mutation of its new object requires fresh read-back, and an existing object's
internal ID must still match the reviewed reference. Duplicate markers, moved
chains, unknown objects and display indices are refused. The bridge does not send
requests, advance a journal, retry uncertain writes or authorize confirmation.

## Observed state is evidence, not a caller assertion

Snapshot must be built by an authenticated, strict device inspector, never by
trusting a browser or an imported plan to assert NamespaceEmpty, Managed,
AuthenticationVerified or CrossChannelIdentityVerified.

Every observed object requires StateChecksum over its actual controlled,
non-secret attributes. Every filter additionally requires RuleSHA256 over its
actual rule body; disabled and configuration-validity flags enter the base checksum. Root bodies
must be the fixed jumps to ActiveGeneration. Reusing an existing generation
requires every ordered body hash, chain and comment to match the generated policy.
An object ID, name, owner comment or policy checksum in a comment is insufficient.

The inspector must reject unexpected attributes, templates, extra roots/rules,
outside references to managed chains/tables, and objects impersonating ownership.
Unknown values must not become invented default values. Sensitive state requires
a separate verified read/comparison against the locally known revision (including
verified absence); it must not enter ordinary hashes, snapshots or logs. RouterOS
menu-local object IDs may overlap across menus. Commands target verified internal
*HEX IDs, never display row numbers.

BaseChecksum binds the entire normalized observed state. Before executing,
the executor must re-read it, verify all dependencies and detect drift. After
preparation, it must separately verify unchanged baseline objects and the exact
newly created objects/returned IDs; the pre-preparation checksum alone does not
describe that intermediate state.

The inspector reconstructs every generation from the local full policy checksum
and typed desired state, checks actual rule text and order within each chain,
then maps remote object names back to local peer IDs. Bootstrap emptiness requires
all four configuration menus to have completed supported reads. It scans external
instances/connections/rules for references even when their names have no managed
prefix. A foreign connection using an uninspected template remains unresolved and
is refused; this fixed inventory cannot prove the template's dependencies.

Missing false/empty properties are unknown, requiring a separately verified
getter. TargetEvidence.Properties records the resource, internal ID, field,
Completed status and actual PropertyValue. Only the exact tested 7.24.6 schema
recognizes a successful getter with no return value as an unset property from
an explicit whitelist. Missing/failed getters and other versions do not inherit
that interpretation. The exact observed version suffix "7.24.6 (long-term)" is
recognized; arbitrary suffixes are not stripped.

For FIB and dynamic presence flags, print presence (even an empty value) plus a
getter with no return, or print absence plus a true getter, is a conflicting read
set and must be re-read. It is never normalized to false. MD5 is excluded from
this normalization: an initial getter with no return remains unknown.

Instance/connection inactive is a runtime flag, not proof of invalid configuration
or health. It and table used are excluded from configuration digests. Filter
inactive=false is the measured usable-rule flag and is included in its digest;
that menu has no invalid getter. Tables retain their explicit invalid flag.
Connection LocalASN is explicit in the typed model and every Add/Set. Active and
desired peers must match their instance ASN. A disabled connection retained until
cleanup may keep a valid old LocalASN only when its peer ID and old ASN are
proved by a retained known generation; it must still differ from RemoteASN.
Identity changes pin connection ASNs before changing the instance. Early filters
and export selection/network/redistribution outside the typed profile are refused.

CredentialEvidence must use the dedicated API getter's opaque result. Known
alone does not prove a key or its absence: the result must match the locally held
secret with the manifest revision. An unknown/mismatched key returns an inspection
error and leaves AuthenticationVerified false. Optional SSH identity evidence
stays unknown unless explicitly authenticated and equal to the API's stable
license/board identity source, exact version and transport binding. The display
name cannot supply that proof. Neither a read set nor BuildSnapshot is an atomic
device snapshot; the executor must handle concurrent changes and re-read state.

## Three distinct phases

1. **Prepare:** create the generation's unreferenced filter chains and missing
   disabled connections. Explicit Bootstrap additionally requires an independently
   verified empty namespace, creates a dedicated table with fib omitted plus its
   instance, and creates reject-only stable input/output roots. Each new disabled
   connection then has its unused AF limit explicitly unset and its authentication
   explicitly initialized before read-back verification. No preparation operation
   enables a peer or attaches a permissive generation to active connections.
2. **Activate:** first set the output root to reject, disable changed/removed
   connections, apply reviewed identity/connection/authentication changes, switch
   roots to the new generation and enable the intended connections. A local
   ASN/router-id change requires AllowIdentityChange. Policy-only changes use
   three root operations without resetting unchanged peers. The transition may
   interrupt service; it is not an atomic forwarding/RIB transaction.
3. **Cleanup:** remove only the verified old generation rules and removed
   connections **after** explicit confirmation and read-back verification.
   Keep the previous generation until rollback is no longer needed. Never run
   cleanup as part of preparation or before confirmation.

An unchanged, verified managed state produces no operations. Partial preparation
or a failed bootstrap requires its durable executor journal and verified
reconciliation; this library does not infer ownership from leftover names.

Input rules match the exact prefix, remote peer address/ASN, first AS and bounded
path length. Paired prefix-origin policies additionally match the final origin.
Output first verifies the recipient tuple, then repeats the current source policy
and rejects reflection to the same remote ASN. This is intended to prevent a
previously accepted but newly revoked route from escaping merely because it is
still cached. The actual target's cached-route/export re-evaluation, numeric
AS-path semantics and AS_SET handling remain required native tests.

Only the existing route-server typed policy is supported. Collector, runtime
RPKI and an explicit RFC1997 override are rejected rather than silently weakened.
No arbitrary filter expressions, imported scripts or free-form commands are
accepted.

## Safe Mode and required native gates

The activation limit is at most 80 **estimated** history actions, with a
conservative property/operation estimate. Prepare and cleanup are excluded and
must run outside that activation transaction. Do not silently split an oversized
activation into independently committed pieces. Concurrent operators can consume
history too: exclusive control and observing the actual owner/state are necessary.
The official configuration-management documentation describes a finite Safe Mode
history and loss of automatic undo on overflow; the estimate is not a verified
guarantee. See [Configuration management](https://manual.mikrotik.com/docs/getting-started/configuration-management/).

Before any live capability is enabled, the isolated CHR test must establish:

- Exact version/device and pinned dual-channel identity. Activation writes must
  use the owned SSH stream so cancellation cannot overtake an independent API
  mutation; verify queued-command and disconnect ordering. API may prepare
  inert objects, perform confirmed cleanup and independently read back state.
- All fixed attribute names, read-back properties/defaults, valid/disabled flags,
  internal IDs, and sensitive getter behavior. Empty AF-limit Set is rejected;
  the separate typed unset and its read-back still need target verification.
- Parse and semantic behavior of source/recipient filters, numeric first/last
  AS matches, jump/reject, exact prefixes, route-server attributes and non-FIB
  isolation. Use actual dual-stack peer fixtures with rejected/withdrawn routes.
- API reply framing and traps, SSH control bytes, confirm/cancel, disconnect,
  process death, competing owner, history exhaustion and read-back rollback.
- Durable journal ordering and supervisor restart behavior. Never mark an
  uncertain write successful, retry it blindly, or claim a bounded rollback
  deadline from an unmeasured connection timeout.

No Safe Mode API take/release/unroll method is inferred. The documented
[Safe Mode CLI reference](https://manual.mikrotik.com/docs/cli-reference/safe-mode/)
lists observational state; the fixed terminal adapter must prove its control
sequence on the tested version.

RouterOS import dry-run is a script syntax facility, not a NETCONF candidate or
semantic routing transaction. This package does not substitute a fake local
checker for a device check. See [Import reference](https://manual.mikrotik.com/docs/cli-reference/import/).

Filter properties, numeric property comparisons and default rejection are
documented in [Route selection and filters](https://help.mikrotik.com/docs/spaces/ROS/pages/74678285/Route+Selection+and+Filters).
The typed connection/instance fields come from [BGP reference](https://help.mikrotik.com/docs/spaces/ROS/pages/331612228/routing+bgp).
The received-route processing limit is approximate; do not present it as an exact
installed-route or memory bound.

## Local tests

The parent package tests use pure typed fixtures, not a device or a listening
socket. They cover inert preparation, export-gating order, source-policy rechecks,
credential revision/clearing/redaction, canonical JSON round trips, idempotency,
content drift, unknown ownership, unsupported inputs and activation-budget limits.
They deliberately never set nativeValidated or verified to true.

Run only these local tests with an existing offline Go toolchain:

    GOPROXY=off go test -race ./internal/routeros
    GOPROXY=off go vet ./internal/routeros

API/terminal transport subpackages have their own fixture and isolated integration
tests. Passing these tests does not authorize a production router connection.
