# Dedicated AS112 operation

`as112ops` installs an approved DNS/BGP pair and supervises **a dedicated local
PowerDNS Authoritative process plus BIRD 2, BIRD 3, or FRR bgpd**. It does not
attach to an existing daemon, operate a shared routing instance, or invoke the
older apply/watchdog rollback paths. Those paths can restore a previous active
configuration after failure, which is unsuitable for health-triggered withdrawal.

OpenBGPD's privilege-separated process tree and remote Junos confirmed-commit
leases do not yet have operational adapters here. Their existing AS112 generation
and validation support does not imply support by this supervisor.

## Reviewed input and installation

The private canonical plan binds the router ID and creation timestamp, service
and address families, complete DNS node/address tuple, exact DNS bundle and BGP
configuration checksums, native executable paths and SHA256 hashes, dedicated
root path, health policy, current Linux boot ID, and network namespace identity.
It contains the unredacted active BGP configuration, including any MD5 keys, and
must be stored as an owner-only regular file. Do not log or publish it.

The active artifact must be the exact current generated AS112 profile. For
BIRD 2/3 and FRR this is model version 2, with graceful restart disabled explicitly
and no LLGR negotiation. Historical model version 1 remains eligible for exact
secret-safe preview, but cannot be installed or activated by this package.
Regenerate and review it first.

Standby is derived from the same exact model with no BGP peers. It has no sessions
through which it can advertise. Installation requires explicit acknowledgment of
exclusive instance ownership and the exact plan, active, standby, and DNS
checksums. This is local operator approval; it is not a cryptographic attestation
that an upstream web approval occurred.

Installation writes a new private version directory, checks every DNS zone and
PowerDNS settings with pinned native tools, and checks both BGP configurations.
Only successful validation permits publication. Files and directories are synced
before publication; the final directory rename and the `current` symlink rename
are separate atomic filesystem operations. The process lifecycle is **not an
atomic transaction** with those file operations. A durable pending-step journal
records each mutation before it is attempted and records its result afterward.

The package creates no addresses, routes, users, services, or external accounts.
Before starting, the operator must provide:

- A dedicated instance root owned by the service user with mode `0700`.
- Every exact anycast and node address in `requiredLocalAddresses`, already
  configured on this host, plus a separately reviewed return path.
- Native ELF executables and installed shared libraries at the reviewed paths.
  Shell wrappers, symlinked executable paths, writable executables, set-id ELF
  files, and ELF file capabilities are rejected. The latter can invalidate
  Linux parent-death ownership. The runtime clears the child environment.
- Permission to bind the declared DNS/BGP ports and, if used, configure BGP TCP
  authentication. The runtime neither acquires privileges nor changes the host
  firewall or sysctl configuration.
- Continuous exclusive control of this dedicated instance and its files. The
  supervisor cannot prevent a privileged administrator from making concurrent
  out-of-band changes.

## Starting, failing, and recovering

After each controller start, a read-only inventory must prove absence of
observable dedicated-instance processes under the service UID. Existing processes
are never adopted or killed by
numeric PID. The runtime starts only its own foreground PDNS and standby BGP
children. Activation requires a fresh actual local-address check and successful
authoritative DNS checks over both UDP and TCP for every configured service
address, followed by the approved number of consecutive healthy checks.
The caller must explicitly permit recovery; a previous active journal is not
permission to resume announcements.

On DNS/address health failure, the controller stops its owned BGP child before
returning, records withdrawn status, and resets the healthy count. Once the same
approved DNS service passes the threshold again, an explicitly recovery-enabled
run may start a new BGP child from the same approved active bytes. It never
silently substitutes a newer artifact. Normal withdrawal/close sends SIGTERM,
then uses a bounded SIGKILL fallback only for the owned process handle if needed.

The native adapter pins each child generation and executable. The creating Go
goroutine locks its OS thread until `Wait` completes, preserving Linux
`PDEATHSIG=SIGKILL` ownership. If the supervisor exits or is killed, its direct
foreground children are killed as well. PID reuse cannot turn a stale child
record into permission to signal another process. An observed foreign process
under the same UID
causes refusal, not takeover.

Observation errors, altered installed bytes, exhausted journal sequence, and
journal write failures enter `unknown`. The controller performs bounded emergency
termination of the child handles it still owns before returning the error,
including when the library caller remains alive. It retains the unknown journal
and does not claim that every external effect was verified. No configuration
operation is blindly repeated. Explicit reconciliation performs only reads and
can clear an unresolved attempt only after proving absence of those observable
same-UID instance processes and
exact installed bytes/pointer. A partially published version with no current
pointer requires operator review.

Health detection is periodic, and BGP convergence depends on the peer. It is not
instantaneous or globally atomic. No GR/LLGR capability is advertised in the
supported profile, preventing negotiated stale-route retention on this session.
`PDEATHSIG` handles owner death, not an owner that remains stopped or hung.
The CLI now integrates with systemd's independent service watchdog as described
below. A bare foreground invocation has no independent hang guard; the generated
unit must actually run under a correctly configured service manager.

## Independent systemd supervision

`ixp-as112 run` recognizes `NOTIFY_SOCKET`, `WATCHDOG_USEC` and an optional
`WATCHDOG_PID`. An explicit PID must name the running supervisor, and malformed
or incomplete watchdog configuration is rejected before opening the controller.
The CLI sends only fixed `READY=1`, `WATCHDOG=1` and `STOPPING=1` Unix datagrams.
The systemd receiver obtains the actual sender identity from kernel credentials;
no plan body, passwords, environment values or arbitrary status text is sent.
Native children retain their cleared environment and do not inherit notification
settings. [systemd notification protocol](https://www.freedesktop.org/software/systemd/man/latest/sd_notify.html)

The first readiness notification follows a complete successful controller Tick,
including an iteration that safely withdrew after failed DNS health. Readiness
means the supervision loop operates; it does not claim healthy DNS, established
BGP sessions or converged routes. Every later watchdog renewal likewise follows
a completed Tick. There is no independent heartbeat goroutine that could renew
while that loop is stuck. A send has bounded connection/write deadlines, and a
notification error exits through owned-process cleanup. Shutdown notification is
sent only after cleanup has returned. Watchdog configuration follows the documented
process-ID and microsecond timeout convention.
[systemd watchdog environment](https://www.freedesktop.org/software/systemd/man/latest/sd_watchdog_enabled.html)

The review-only unit uses `Type=notify`, `NotifyAccess=main`, `WatchdogSec=180`
and `TimeoutStartSec=180`. The deadline accommodates the existing 60-second DNS
probe, 1–30-second cadence and bounded process-stop work; indefinitely stalled
filesystem or process operations cease renewal. A working systemd manager owns
the deadline independently of the service. `WatchdogSignal=SIGKILL`,
`KillMode=control-group`, `SendSIGKILL=yes` and `FinalKillSignal=SIGKILL` bind
termination to that unit's processes, including a stopped main process. Normal
stopping retains SIGTERM followed by a 30-second forced-stop deadline, and core
dumps are disabled. The CLI itself never signals a PID supplied by notification
configuration. [systemd service watchdog](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html),
[systemd process termination](https://www.freedesktop.org/software/systemd/man/latest/systemd.kill.html)

The unit generator still installs or starts nothing. Sending a datagram proves
only that it was queued, not that a systemd manager accepted it or that its unit
has the required settings. With no notification configuration, foreground mode
continues without a watchdog. An external manager that is itself hung, a kernel
failure or uninterruptible kernel sleep is outside this service-level guarantee.
Startup, recovery and unknown-WAL checks remain unchanged after a watchdog kill;
a restart does not automatically authorize replay of an uncertain operation.

## Offline version replacement

An installed instance can now be updated in the same root through an explicit
offline replacement. The old and new plans must name the same platform and router
entity. The new plan must name the current approved execution boot and network
namespace; after a reviewed reboot rebind, the old plan retains its original pin. The old instance must have completed
withdrawal, have no pending process operation, and have no observable owned or
foreign process under the dedicated UID using its root. Approval binds the old plan checksum and exact current
pointer, as well as all four new plan/artifact checksums and exclusive ownership.

`Replace` validates a new private staging directory and retains the previous
version. A separate `replacement.json` journal records version publication,
pointer-switch intent, and completion. The completed status binds the new plan
and remains withdrawn. The old plan can no longer open the instance; running the
new plan still requires fresh DNS health and explicit permission to recover.

`OpenForReplacement`, `ReadStatus`, and `ReadReplacement` only inspect an existing
transaction. They do not finish it. After a crash or an uncertain file result,
`ReconcileReplacement` requires the same exact approved pair. It verifies actual
staging/final directory presence and bytes and the exact old/new pointer before
advancing the durable marker. Effects already present are not blindly repeated.
It may repeat read-only native validators, but does not start, stop, reload or
adopt DNS/BGP services. A third pointer value, changed bytes, unexpected process,
or ambiguous staging/final pair causes refusal. The ordinary process reconcile
action cannot resolve this separate file transaction.

Retention is limited to eight versions and 128 MiB of version files, counting
the proposed new version. There is no automatic pruning or deletion of the old
baseline. The explicit offline retention workflow below can free reviewed older
versions before another replacement.

## Reviewed offline retention

`ReviewRetention` requires the installed current plan, withdrawn state, no pending
service or replacement operation, and absence of observable dedicated-instance
processes under the service UID. The current version and both endpoints of the
latest replacement remain protected. An incomplete replacement or unknown
staging directory blocks cleanup; the tool does not erase recovery material.

Provide each candidate's original private historical plan. Directory names alone
are not ownership evidence. Historical plans bind the exact original artifact
hashes, instance and router entity, and safe generated file layout. Their old
native binaries need not remain installed, and cleanup does not execute a checker
or run old configurations through today's activation renderer. This historical
decoder is confined to retention and cannot authorize installation or activation.
Versions lacking a historical plan are retained with a reported reason.

The review contains no configuration bodies or credentials. It lists candidates,
protected reasons, file hashes and byte counts, directory/file identities and
permissions, the original current pointer, and status/replacement journal hashes.
Save it outside the instance root. Explicit application requires its exact
checksum plus deletion and exclusive-ownership acknowledgments. The controller
rechecks the actual objects and original ownership evidence before deleting.

Cleanup uses a separate durable `retention.json` cursor, with an intent and result
for every reviewed file or empty directory. It does not call recursive removal
on a version or on the root. Deletion uses confined directory handles and checks
the actual parent and leaf device/inode, owner, type, mode, link count and file
hash. Unexpected objects, symlinks, hardlinks, FIFO/device entries, mount aliases,
or permission changes cause refusal. Complete cleanup keeps the current instance
withdrawn and preserves the replacement journal and its baseline.

Opening the instance and reading status never resume deletion. If an attempted
entry is actually absent, `ReconcileRetention` can explicitly record that evidence
and advance the cursor without repeating the unlink. Applying the original
review can then continue only entries that were never attempted. If the uncertain
entry still exists, that old intent is not retried. `ReviewRetentionResume`
checks the exact remaining identities and bytes and issues a **new** review
checksum bound to the previous journal/cursor and its uncertain intent. Only a
new explicit approval of that checksum permits another deletion attempt. Changed
or unrecognized remaining material is refused rather than folded into new
authority.

## Explicit reboot and move review

New installations persist a random local instance ID and execution target in
`status.json`. Where a trusted regular `/etc/machine-id` is available, installation
also captures its SHA256 with an instance-specific salt and domain separator.
The raw machine ID is never returned or saved. This is a local administrative
identity, not hardware attestation: deliberately duplicated machine IDs and
privileged out-of-band changes remain outside the exclusive-ownership contract.

A host reboot does not grant execution authority. `OpenForRebind` only locks an
existing private root. `Review` produces a private, credential-free review of the
original plan checksum, unchanged active/standby/DNS hashes and tool pins, exact
current pointer, complete retained-file identities/hashes, status and completed
replacement/retention markers, old target and actual destination. It calls only
process observation. Closing this review session never signals stale journal
PIDs or calls runtime cleanup.

`same-host-reboot` requires an already established stable host identity, a matching
current identity and a different boot ID. A confirmed old `active` record is
eligible after proving actual process absence; pending or `unknown` process WAL,
incomplete replacement or retention, unrecognized staging, altered artifacts,
or an observable same-UID instance process all cause refusal. The review and
application require continuous exclusive control of this same local instance.

`move` is a separate explicit mode for a changed host, cloned instance, replaced
namespace, unavailable stable identity or legacy instance without an established
binding. It keeps the same absolute root and local instance ID when one exists.
Application additionally requires the operator's explicit source-isolation
acknowledgment: the operator must prevent the previous source and all clones from
running. This tool neither contacts the source nor proves that a remote host is
dead. Merely having matching files or machine IDs is not a takeover mechanism.

`Apply` requires the exact review checksum and exclusive-ownership acknowledgment.
It rereads the evidence and atomically replaces one complete status file, retaining
the original plan and every artifact/tool pin, resetting stale process records and
health to withdrawn. There is no separate remote effect or service command.
`Confirm` only reads and reports whether the exact old or approved new state is
present. After an uncertain persistence result, explicit application can recognize
the exact new state without rewriting it, or commit the exact old state only after
all preconditions are rechecked. Contradictory evidence or leftover publication
scratch is refused. This does not replay a pending service, replacement or cleanup
operation. A normal open never performs a rebind.

Old canonical plans and checksums remain unchanged. An older installation can
establish the optional identity with an explicitly approved, idempotent `Install`
on its original boot after process absence checks. If it has already rebooted
without this evidence, it cannot claim same-host proof and must use explicit
`move` review with the source-isolation declaration. A missing stable machine
identity similarly never becomes a successful same-host assertion.

Rebind checks the installed artifact bytes and preserves historical tool pins; it
**does not execute or revalidate old binaries**. This permits recovery from a reboot
that also upgraded the native packages. Ordinary startup still enforces the
original executable SHA256. If those tools changed, use a newly prepared plan with
new explicit checksums and ordinary offline `Replace`, which validates the new
native executables before publication. Never change old pins in place. Historical
retention continues to validate each original plan checksum across these host
bindings, without executing historical binaries. The local binding retains up to
128 distinct original plan target epochs; reaching that bound refuses further
replacement rather than discarding ownership evidence.

After rebind, `run --recover-ack` still requires fresh local address and dual UDP/TCP
DNS health. Rebind itself creates no address, validates no return path, starts no
process and grants no active announcement. The original plan path can remain in
the reviewed systemd unit; update it only if a separate replacement is approved.

## Current lifecycle limits

- Only offline same-platform replacement is supported. Updating a live instance
  or changing its router entity/platform is not implemented.
- Boot ID and network namespace pins remain strict for ordinary execution.
  A reboot requires the explicit review/apply workflow above. Moving an instance
  requires operator-controlled source isolation; the tool cannot verify remote
  shutdown. Rebinding an instance with pending recovery material is refused.
- Address ownership, automatic address installation/removal, OpenBGPD process
  containment and remote Junos lease renewal/readback are unfinished. The
  systemd watchdog requires an actually installed and running manager/unit; a
  standalone foreground process has no independent hang guard. No address is removed as part of withdrawal.

## Evidence

The resumed backend regression is recorded in
`.cache/as112ops-resume/race-results.json`: 12 operational tests plus the historical
v1/current-v2 test passed under the race detector, and both packages passed vet.
The frozen source aggregate was
`907f36697e29389ae1f3b5a4f12194a2b81cc8653e102ea4707b8e58959ffaf6`.

`.cache/as112ops-native/results.json` and `native.log` record the later native
integration snapshot
`b5101245e638e31921d13c86940fdc998471e7a5381c3521c5f7e40b91a1120c`.
Actual BIRD 2, BIRD 3, and FRR each passed installation, real dual-stack UDP/TCP
PDNS health, four BGP peer sessions, a DNS SIGSTOP health failure, owned BGP exit
and remote session/RIB removal, and recovery through new sessions after fresh
health. The peer checked that OPEN contained neither GR nor LLGR capability.
The same run passed the additional immediate-emergency-withdrawal and stale-PID
regressions and vet. It completed with exit 0 in 70.454 seconds.

These tests ran in private network namespaces with synthetic addresses and
peers, read-only source/tools/dependencies, bounded scratch/resources, zero target
capabilities, and `no_new_privs`. No production router, DNS server, host service,
or external network was contacted. They do not prove the unfinished lifecycle
features above.

The subsequent offline-replacement snapshot is recorded in
`.cache/as112ops-replacement/race-results.json`, aggregate
`0a9af126790122cd819e7bca36f2c0acec5fc75f573ab3e231d0918b4686efb6`:
18 operational tests and 31 subtests passed with race detection, with one
explicitly gated native integration test skipped, and vet passed. Six simulated
crash positions prove that opening is read-only and that only explicit
replacement reconciliation can finish the exact approved filesystem transaction.
That run did not repeat or extend the native DNS/BGP evidence above.

A subsequent targeted race regression and vet passed in
`.cache/as112ops-replacement-fault/race-results.json`, source aggregate
`28fd872c7de4a77e2cc25325dcc30fa9cb5198db9aeb37f8e39ccd812c1d23a7`.
It verifies that an uncertain first intent write preserves the exact staging
directory, rather than deleting data that a durable journal might reference.

Retention has separate evidence in `.cache/as112-retention-checks/`: eight backend
tests, two CLI tests and nineteen subtests passed under race detection, with vet
and a CGO-disabled CLI build. The filesystem fixture fills all eight version
slots, protects the current/latest-baseline pair, removes only six proven older
versions, and successfully replaces the instance again. It also covers missing
historical proofs, old checker compatibility, unknown unlink absence and renewed
approval for still-present targets. These tests use synthetic private files and
an observation-only runtime model; they do not start DNS/BGP services or extend
the earlier native-service evidence.

The final retention snapshot is recorded in
`.cache/as112-retention-final/results.json`, aggregate
`1374f99e4a921432b84f65240efcb2ea9eb150a83e2ac5e2e836d05265b6b24a`:
nine backend tests, two CLI tests and twenty-one subtests passed with race
detection, both packages passed vet, and the CGO-disabled CLI built successfully.
This adds the review-output boundary, special permission/FIFO checks and nested
mount-alias refusal. It is a separate increment from the earlier full-project
integration snapshot and ran no native service or VM.

The explicit reboot/move workflow and the complete AS112 ops/CLI regression passed
in `.cache/as112-rebind-final/results.json`, aggregate
`979a465a2e5ce22f7d7449b48b02bc4a1ee904bc53379b7167c8ccb6714ccfec`:
35 backend and 9 CLI top-level tests plus 80 subtests passed under race detection;
one explicitly gated native integration test was skipped. Both packages passed
vet and the CGO-disabled CLI built. This includes 7 new backend rebind tests,
2 new CLI tests and 24 rebind subtests. A private user namespace mapped the ordinary
host user to namespace UID 0, dropped all capabilities, isolated networking and
mounted a **synthetic** read-only machine ID to assert the full same-host reboot
path. The earlier ordinary-UID run is retained separately in
`.cache/as112-rebind-checks/results.json`. Both runs used frozen read-only sources
and dependencies with bounded scratch/resources; neither restarted a host nor
started DNS/BGP services or a VM. These results do not extend the previous real
DNS/BGP peer-convergence evidence.

Watchdog-specific evidence is recorded in
`.cache/as112-watchdog-final/results.json` and
`.cache/as112-watchdog-final-native/results.json`, source aggregate
`369b2ed18024fcca4194aa459485ebdfd676b37994fd8251e854550c63296695`.
Four targeted CLI tests and fifteen subtests passed with race detection; the
separately gated real-service test was skipped in that checks run. Both packages
passed vet, the CGO-disabled CLI built, and the generated unit passed stock
systemd 257 `systemd-analyze verify`. A private stock-manager probe refused to
start because that isolated environment was not booted with systemd. No host
systemd socket, service or cgroup was changed, so this is not a stock-manager
end-to-end service test.

The separate native run executed the actual CLI with one real PowerDNS and BIRD 2
instance in a private loopback-only namespace. It recorded seven authenticated
renewals and rejection of another sender, actual DNS failure withdrawal/recovery,
and external expiry after SIGSTOP of the CLI. The independent test watcher
signaled only its own `exec.Cmd` process handle; the owned native children then
exited through the existing parent-death mechanism. This demonstrates actual
notification and owned-process behavior, not the systemd cgroup kill path. There
were no remote peers, no repeated BIRD/FRR matrix and no VM. Sources/tools were
read-only, target capabilities were zero, `no_new_privs` was set and resources
were bounded. Updating the executable alone does not enable a watchdog in an old
unit: an operator must review and install the corresponding unit settings.
