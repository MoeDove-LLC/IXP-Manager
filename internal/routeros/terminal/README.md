# RouterOS terminal foundation

This independent package implements one persistent terminal channel. It has no
dependency on the parent `routeros` package and exposes no `Run(string)` or script
upload. Its new `ExecuteOwned` mutation surface is closed and version-gated by
the live caller. Safe Mode release is a separate
bounded operation; it never claims configuration commit/readback verification.

`ExecuteOwned(ctx, api.Mutation)` accepts only Set/Unset/Enable/Disable on the
existing filter-rule, BGP-instance and BGP-connection objects used by activation.
It rejects Add, Remove, table writes, expressions/display indices as selectors,
unknown attributes and resets beyond the two address-family limits. The live
adapter must first rebuild and compare the canonical plan operation and resolve
the exact owned native ID; this lower-level method does not infer namespace
ownership from a name.

The fixed CLI encoder sorts attributes and quotes string data, escaping literal
backslashes, quotes and dollar signs. It rejects control bytes and non-ASCII
input, bounds the physical command, and validates portable MD5 keys including
explicit clearing. The internal synchronous do/on-error wrapper prints only
split random completion/failure markers. Raw command echoes, errors, passwords
and command hashes are never returned. Ordinary prompts, echo-only markers or
unknown output cannot establish completion.

The mutation uses the same Client.operation writer as owner queries, Confirm and
Cancel, with known ownership and actual floating-undo counts before and after.
An ambiguous or timed-out command closes this original stream and is never
replayed. This removes the independent API activation-write channel; API is
still required for authoritative configuration read-back. It does not prove
that RouterOS has finished rollback, that all asynchronous routing effects have
converged, or that ownership contention is safe.

The owned mutation syntax and queued-command/disconnect cases require native
7.24.6 verification before any production gate opens. In particular, closing a
different API connection cannot retract a mutation already in transit while SSH
Safe Mode is being cancelled. A matching baseline observed before such a late
mutation would not prove that no later write can occur.

`TestNativeCHRSSHOwnedMutations` is an opt-in disposable-lab test covering filter
text with a literal AS-path dollar anchor, instance/connection changes, MD5
set/clear, AF unset and enable/disable. Its inert setup uses API before ownership;
after Take, every mutation uses SSH and API only reads back. Cancellation must
restore the checkpoint before explicit fixture cleanup. The test defaults to
skip; compilation/unit fixtures are not native evidence.

```go
session, err := terminal.DialSSH(ctx, terminal.SSHOptions{
    Host: "192.0.2.1", Port: 22, User: "automation",
    Identity: "/private/routeros/id_ed25519",
    KnownHosts: "/private/routeros/known_hosts",
}, terminal.DefaultLimits())
// Or: terminal.Open(serialSession, limits) for an already authenticated CHR
// console. Preserve the initial prompt for the adapter to consume.
```

`Session` is an injected reader/writer/closer; closing it must unblock concurrent
I/O. The codec handles fragmented ANSI colors, bounded horizontal line repaint,
DECID/primary-device-attributes and cursor-position requests, recognized paging
prompts, and normal/Safe Mode prompts. Unsupported screen control is rejected.
The initial terminal negotiation completes before any read command is sent.

Queries are the fixed `Probe`, `Version`, `ActiveUsers`, `History`,
`SafeModeOwner`, `LicenseSystemID`, `LicenseSoftwareID`, `RouterBoardSerial`
and `FloatingUndoCount`
operations. Commands execute serially, each framed by random
start/end markers whose complete literals never appear in the command echo.
Each frame is one physical command line, so a paging response cannot consume
already queued subsequent commands. Unknown commands are never interpolated.
Only a strictly parsed version, line count and state categories are returned;
raw output, owner names, private identity paths and stderr are not returned.
Owner/user strings from the optional owner menu are represented by hashes.
Device identifier queries accept exactly one nonempty ASCII identifier of at
most 128 bytes and return only its SHA-256 as `IdentifierFingerprint`;
`Result.Query` identifies the source field. `LicenseSystemID` treats its value
as opaque unpadded Base64-alphabet data (`A–Z`, `a–z`, `0–9`, `+`, `/`, including
a leading `+` or `/`), without decoding or normalization. Native CHR 7.24.6
has returned an eleven-character System ID containing `/`. Whitespace,
padding, quotes, semicolons and additional data lines are rejected.
`LicenseSoftwareID` and `RouterBoardSerial` retain the stricter letters/digits
and hyphens rule with an alphanumeric first character. Missing, ambiguous or
malformed identifiers fail closed. Compare the fingerprint with
the same field obtained over an independently authenticated API connection;
these queries do not make a display name into device identity.
`FloatingUndoCount` uses the fixed read-only
`/system/history/print count-only where floating-undo` command. Only one exact
canonical decimal line in the range 0–100 sets `HistoryKnown=true` and
`HistoryActions`; missing, malformed, repeated or unsupported output leaves
the count unknown rather than inferring zero. It returns no history commands.

`EnterSafeMode` requires a fresh normal prompt and the taken acknowledgement
followed by a Safe Mode prompt. A second enter cannot toggle/commit it. An owner
hijack question is answered only with `d`, leaving the other session unchanged.
An unexpected normal prompt or explicit ownership-loss event poisons the channel.
`CancelSafeMode` sends Ctrl-D after a fresh probe of this channel's Safe Mode
prompt. EOF/login proves only that the terminal session ended. Its result always
has `RollbackVerified: false`; native configuration readback belongs to the
parent controller. `/quit` is never used to cancel.

`ConfirmSafeMode` requires both a fresh framed prompt probe and a fresh owner
query reporting this channel as current. It sends exactly one Ctrl-X, then
accepts only the observed 7.24.6 line `Releasing Safe Mode... Success!`, a normal
prompt and a new owner query reporting disabled/current=no. Its
`Confirmation.Released` and `Evidence.Mode=Released` describe this release;
`CommitVerified`, `RollbackVerified` and `NativeValidated` remain false.
Pending release permits that normal prompt without misclassifying it as an
ownership loss. Explicit loss, an unexpected taken/owner message, unknown
response, timeout or disconnect closes the channel without retrying a toggle.
The caller must verify configuration over a separate authenticated channel.

All evidence currently has `NativeValidated: false`. Fixture success does not
open any parent approval/application gate. The `/safe-mode` read-only menu is
documented in the **7.26** CLI reference; it is not assumed available on earlier
versions. A rejected/missing owner menu leaves `OwnerQueryAvailable: false` and
`Owner: unknown`, while keeping a correctly framed read-only session usable.

The system-SSH transport requests one remote PTY with `-tt`. It uses a fixed
`/usr/bin/ssh`, explicit key and pinned host-key files, no ambient SSH config,
agent, proxy, multiplexing, password authentication or forwarding. The private
key must be a nonempty regular file accessible only to its owner; credential
path symlinks, option expansions and group/world-writable files are rejected.
Process environment is explicitly constructed, stdout is bounded by the client,
stderr is discarded with a 4096-byte limit, and context/lifetime expiry closes
the channel without reconnecting. Operators remain responsible for protecting
the configured credential directory from concurrent local changes.

Limits cover operation time, session lifetime, output bytes, line width, line
count, page continuations and terminal-negotiation replies. Errors contain only
fixed safe categories. Closing a transport during Safe Mode is **not** claimed
to have completed an undo; delayed device behavior, ownership contention and
history exhaustion require native target-version checks.

Official sources checked during implementation:

- [MikroTik SSH](https://manual.mikrotik.com/docs/management-tools/ssh/)
- [Console and login parameters](https://manual.mikrotik.com/docs/management-tools/console/)
- [Configuration management and Safe Mode](https://manual.mikrotik.com/docs/getting-started/configuration-management/)
- [Version 7.26 Safe Mode reference](https://manual.mikrotik.com/docs/cli-reference/safe-mode/)
- [CHR System ID](https://manual.mikrotik.com/docs/getting-started/routeros-licensing/chr/chr-licensing/)
- [Hardware/software licensing identifiers](https://manual.mikrotik.com/docs/getting-started/routeros-licensing/)
- [RouterBOARD serial number](https://manual.mikrotik.com/docs/getting-started/installation-and-upgrade/routerboot/)

The native lab owner separately observed RouterOS 7.24.6's `ESC Z` terminal
identification request, the `/safe-mode/print` menu and its exact
`Taking Safe Mode session... Success!` acknowledgement. The codec accepts that
acknowledgement only together with a Safe Mode prompt (not a generic success
substring). These fixture cases do not themselves certify a native SSH
transaction or prove rollback readback; the validation flags remain false.

The observed 7.24.6 prompt/logout presentation controls are handled narrowly:
`CSI 9999B` only at an empty line, same-line `ESC 7`/`ESC 8`, exact Home
(`CSI H` or `CSI 1;1H`), clear-to-end (`CSI J`/`CSI 0J`) only after an empty
line/Home, and `ESC c` reset. These operations discard presentation state;
they never flush pending text as a completed marker or prove configuration undo.

The trusted native harness subsequently passed `TestNativeCHRSafeModeRollback`
on CHR **7.24.6** in 2.50 seconds: verified API-SSL login/version, serial console
synchronization/version/current Safe Mode ownership, a separate API channel
reporting `current=false`, a typed API table add with native-ID readback, and
Ctrl-D followed by disappearance of both table name and ID through the original
API connection. That readback, not terminal EOF, established this cancellation
case. See [the reproducible harness](../../../tools/testing/routeros/README.md).
This evidence does **not** cover native SSH, transport-loss rollback, history
overflow or Confirm/commit. No production validation flag is changed by it.

A subsequent isolated CHR 7.24.6 experiment authenticated the actual SSH
channel using a newly generated user key and a host key exported over the
trusted serial console and cross-checked before pinning. It observed
`current=yes`, the exact release line above followed by a normal prompt, and
the test table retained after release and SSH close through independent serial
readback. This supplies the release syntax evidence, not a completed Go/API
commit test or a general transport-loss/history-overflow guarantee.

`TestNativeCHRSSHSafeModeRollback` passed in the actual isolated CHR 7.24.6 SSH
lab in 2.48 seconds. It requires the same fixed CHR context plus private
`/scratch/ssh_identity` (0600) and pinned `/scratch/known_hosts`, and connects only
to `admin@127.0.0.1:12222`. It checks version and System ID fingerprint against
TLS API readback, then owns Safe Mode through SSH and requires disappearance of
an API-created disabled, non-FIB table after Ctrl-D through that SSH channel.
This proves that bounded native cancellation case, not transport-loss behavior,
history overflow or all versions. Default skip and terminal fixture tests are
not native SSH evidence.

The separate `TestNativeCHRSSHConfirmRetains` is ready for native execution.
It requires known floating-undo counts before/after a disabled table add,
checks release through `ConfirmSafeMode`, then requires the exact table name,
native ID, disabled state and non-FIB state to remain through the original TLS
API connection both after release and after closing SSH. Only after these
observations may it explicitly clean up that freshly revalidated object.
That cleanup cannot stand in for either commit retention or cancellation proof.
