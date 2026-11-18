# Explicit mailing-list synchronization

`ixp-sync mailing-plan` previews the explicit subscription and unsubscription
intentions exported by IXP Manager. `mailing-apply` can apply that reviewed plan
to **Mailman Core 3.3.10, REST API 3.1**. Other versions and Mailman 2 are rejected
by this adapter. The original PeeringDB `ixp-sync` flags continue to work.

The legacy PHP templates called Mailman 2 `add_members -w n -a n` and
`remove_members -n -N`. This bridge preserves their no-notification and
leave-unmanaged-members-alone behavior without invoking arbitrary commands.
It does not import external membership as new consent: an unset IXP preference
does nothing. A retired email is an explicit unsubscribe; a current account's
explicit subscription takes precedence when an address is reused.

## Configure one fixed source and target

Create an owned private directory (0700) and files (0600). The source token needs
the administrator-only `mailing-lists:read` scope. The target credential is a
Mailman Core REST administrator credential. Keep that API on a private network
behind authenticated HTTPS; the CLI rejects plaintext HTTP and redirects and
does not use environment proxy settings. Certificate chains and hostnames are
verified. A private CA file is optional; it adds trust roots, never disables
certificate verification.

Example `/var/lib/ixp-mailing/config.json` (replace the identities with the
reviewed source export and target list; passwords are separate files):

```json
{
  "version": 1,
  "source": {
    "url": "https://manager.example.net",
    "tokenFile": "/var/lib/ixp-mailing/manager.token",
    "listId": "mailing_list_record_id",
    "listKey": "members",
    "listEmail": "members@lists.example.net",
    "listCreatedAt": "2026-11-15T00:00:00Z"
  },
  "target": {
    "url": "https://mailman-admin.example.net/3.1",
    "username": "ixp-rest-admin",
    "passwordFile": "/var/lib/ixp-mailing/mailman.password",
    "listId": "members.lists.example.net",
    "listEmail": "members@lists.example.net"
  }
}
```

`caFile` may be set separately on `source` and `target`. Credentials must be
nonempty single lines. Plans, journals and credentials may contain private
addresses and must not be published. Input paths reject symlinks, hard links,
other owners and writable-by-other-user non-sticky parent directories.

Preview reads the target's read-only `/config/created_at` field and fixes that
exact timestamp in the plan, so deletion and recreation under the same list
name invalidate approval. The target config may optionally supply
`listCreatedAt` as an additional expected pin; normally preview captures it for
review without requiring a separate manual API query. Only that single field
is read, not the complete Mailman configuration.

## Preview, review and apply

```sh
ixp-sync mailing-plan --config /var/lib/ixp-mailing/config.json \
  --out /var/lib/ixp-mailing/plan.json
```

This reads both services and writes a **new** private plan; it cannot change
membership. Review its `actions` and source/target identity locally. The console
summary contains counts and checksums, not addresses or credentials. The source
checksum is independently recomputed over its canonical version, list and two
sorted intent arrays. Source `generatedAt` is informational, not a freshness
grant. The source list ID, creation timestamp, key and email are pinned; list
revision and all explicit intentions are part of the source checksum.

Pause other administrators/automation changing this target list from preview
through the apply window. Mailman does not provide a membership compare-and-swap transaction;
the explicit exclusive-access acknowledgement describes this operational
requirement, not a remote lock acquired by the CLI.

New subscriptions also require the target list's `admin_notify_mchanges`
setting to be **false** throughout that window. Stock Mailman 3.3.10 cannot
override administrator subscription notices per request. An administrator must
configure that setting before applying; the bridge never changes it. It reads
and verifies the setting immediately before every new subscription and refuses
the write if the setting is enabled, missing or unreadable. Unchanged memberships
and explicit unsubscriptions do not require that subscription-only prerequisite.

```sh
ixp-sync mailing-apply --config /var/lib/ixp-mailing/config.json \
  --plan /var/lib/ixp-mailing/plan.json \
  --journal /var/lib/ixp-mailing/apply.json \
  --checksum REVIEWED_PLAN_SHA256 --exclusive-access-acknowledged
```

Before every mutation, the bridge fetches a fresh source export, compares its
checksum, verifies the Mailman version/list identity and compares the address's
exact membership to the preview. It never enumerates unrelated memberships for
bulk deletion and never removes an owner or moderator role. Existing membership
must be bound to the explicit address (`as_address`); preferred-address-tracking
memberships (`as_user`) require separate human review and are rejected.

Each write intent is synced to the journal before the request. The CLI issues
one mutation, then independently reads back its result with a separate bounded
deadline. Redirects and transport replay of the mutation body are disabled.
A lost response stops the run even if readback confirms success; an explicit
subsequent apply can continue already confirmed work without repeating it.
Source or target drift stops subsequent writes. Partial completion is retained.

Read-only observations of unchanged members are checkpointed together at the
next write intent or at the end of the round. Every remote mutation still has
its own durable intent and independent readback checkpoint. If persistence
fails, the returned summary describes only the last successfully persisted
journal; an observation held only in memory cannot report recorded completion.

## Explicitly authorize periodic synchronization

`mailing-watch` can replace an operator repeatedly approving ordinary changes
to users' explicit preferences. Its initial authorization applies to **future
explicit intentions** from the configured source list, including withdrawals.
It does not convert existing external membership or unset preferences into
consent. First create and review a normal `mailing-plan`, then authorize a new
private worker directory:

```sh
ixp-sync mailing-watch-init --config /var/lib/ixp-mailing/config.json \
  --plan /var/lib/ixp-mailing/plan.json --checksum REVIEWED_PLAN_SHA256 \
  --state-dir /var/lib/ixp-mailing/worker \
  --enable-automatic-sync --exclusive-access-acknowledged \
  --max-changes 25 --interval-seconds 60

ixp-sync mailing-watch --config /var/lib/ixp-mailing/config.json \
  --state-dir /var/lib/ixp-mailing/worker
```

The first command verifies the reviewed source snapshot and target entity,
creates the state directory with mode 0700, and writes private immutable
`policy.json` and `approved-plan.json` files. The policy binds the entire fixed
configuration checksum, the approved target creation timestamp, a per-round
change limit (1–1,000), and a polling interval (5–86,400 seconds). Keep its printed
policy checksum for explicit recovery and retention operations. It does not
apply the initial plan. The second command runs in the foreground; `--once`
performs one round for an existing supervisor. No host scheduler is installed.

Use one state directory for the entire lifetime of this source/target worker.
The worker holds an exclusive directory lock, including between polls. Keep
other target administrators and automation paused for its entire lifetime;
the acknowledgement is not a lock acquired at Mailman. Stop the worker before
changing its configuration or policy. Credential contents may be rotated at
the same private paths and are reread each round. Target recreation, configuration
identity changes, rejected source authority, invalid credentials, notification
prerequisites or an excessive batch stop the worker with a persisted reason.
Administrator notices still must be disabled before any new subscription;
the worker never changes that setting. Safe withdrawals remain possible while
the welcome/goodbye/administrator notice settings are enabled.

Each poll reads current source consent and actual exact memberships. A changed
consent or membership snapshot creates a new numbered directory containing an
immutable plan and a durable journal, published before `state.json` records it
as active and before `Apply` can send a mutation. Unchanged snapshots update the
observation timestamp without generating new plans or replaying work. Completed
rounds retain a receipt bound to their plan, policy and journal checksums.
Startup checks the retained history. A crash between publishing a round and
recording it can recover only that same completely planned journal; an
unreferenced attempted write or missing/altered history is rejected.

```sh
ixp-sync mailing-watch --config /var/lib/ixp-mailing/config.json \
  --state-dir /var/lib/ixp-mailing/worker --status

ixp-sync mailing-watch --config /var/lib/ixp-mailing/config.json \
  --state-dir /var/lib/ixp-mailing/worker --reconcile-only

ixp-sync mailing-watch --config /var/lib/ixp-mailing/config.json \
  --state-dir /var/lib/ixp-mailing/worker --resume --checksum POLICY_SHA256
```

Any failed round latches a halt. Restarting the process, restoring a credential,
or receiving a newer opposite source choice does not clear it. `intent`,
`unknown` and `not-observed` results permit only read-only reconciliation of
the existing journal. A late commit can be observed, but does not automatically
resume writes. After every uncertain result is resolved, explicit `--resume`
rechecks current authority, target identity, notification prerequisites and
the change limit, retires only known results, and permits a new fresh round.
Resume itself sends no membership mutations. This exceptional recovery needs
operator approval; ordinary later user preference changes do not.

Retained rounds are bounded to 10,000 and 128 MiB, with room reserved for each
journal's readback records. The initial authorization is retained separately.
There is no automatic deletion. Before capacity is reached, stop the worker,
archive completed history if needed, and explicitly remove older **closed**
rounds while keeping the latest desired count:

```sh
ixp-sync mailing-watch --config /var/lib/ixp-mailing/config.json \
  --state-dir /var/lib/ixp-mailing/worker --prune-keep 100 \
  --checksum POLICY_SHA256
```

Pruning validates closed receipts and never removes the initial authorization,
the active round, or an unresolved journal. At least one closed round is kept.
If the capacity limit already halted the worker, prune and then explicitly
resume. Incomplete `.preparing-*` directories from a crash were never used for
remote requests and may be inspected separately; do not remove numbered rounds
or reset/clone the state directory to bypass recovery. Status and stdout are
persisted observations, not a claim of current external membership or delivery.

## Unknown results and persisted status

```sh
ixp-sync mailing-status --config /var/lib/ixp-mailing/config.json \
  --plan /var/lib/ixp-mailing/plan.json --journal /var/lib/ixp-mailing/apply.json

ixp-sync mailing-reconcile --config /var/lib/ixp-mailing/config.json \
  --plan /var/lib/ixp-mailing/plan.json --journal /var/lib/ixp-mailing/apply.json \
  --checksum REVIEWED_PLAN_SHA256
```

Status is a persisted observation, **not current remote health or membership**.
Reconcile reads the exact target memberships and updates the private journal;
it never issues POST or DELETE. An unresolved intent/unknown result prevents
apply. If the intended change is not yet visible, it records `not-observed` and
still refuses apply: an earlier timed-out request might commit later. Repeat
read-only reconcile to observe a late commit. A fresh preview/review/journal is
only appropriate after an operator has independently established that the old
request is no longer in flight and restored exclusive target administration.
Do not delete a pending journal to work around this check.
`recordedComplete` means all explicit intentions have historical readback
observations, with `lastObservedAt` identifying the latest of those observations;
it cannot promise future state. An empty plan reports `noIntentions:true` and
does not claim completion. Command output separately reports
`commandSucceeded` and always `liveStateConfirmed:false`, so a failed fresh
authority/identity check cannot be mistaken for current successful application.

There is a 16 MiB document bound, 10,000 explicit intentions and 1,000 membership
changes per plan, a 15-minute command deadline and bounded individual requests.
The bridge does not silently split an oversized source into unaudited batches.

## Notification suppression prerequisites

For subscription, POST `/3.1/members` supplies `pre_verified`, `pre_confirmed`
and `pre_approved` as true, and `invitation` and `send_welcome_message` as false.
It supplies an email address, not a user ID. The stock endpoint **rejects** an
`admin_notify_mchanges` request parameter; administrator notices follow the
list's setting. The separate read immediately before the write must confirm
`admin_notify_mchanges:false`. The exclusive administration window also covers
that setting because Mailman offers no atomic check-and-subscribe operation.

For unsubscription, DELETE `/3.1/lists/LIST_ID/roster/member` contains exactly
one explicitly unsubscribed address in `emails`. Mailman 3.3.10's
`MembersOfList.on_delete` calls `SubscriptionService.unsubscribe_members`, which
filters the member role and directly calls `member.unsubscribe()` without the
goodbye/admin notification workflow. **Do not replace this with DELETE
`/members/ID`**: that route's unsubscription workflow can use the list's goodbye
and administrator notification settings despite an outdated source comment.
The bridge never changes global notification settings to suppress mail.

Reviewed release: [Mailman 3.3.10 source](https://pypi.org/project/mailman/3.3.10/),
sdist SHA-256 `0de478fcf326f25d931938c3744e61c5cd67596b46e5b1e3bb2abf99d5445632`.
Relevant implementation: `rest/members.py`, `rest/lists.py`,
`app/membership.py`, `model/subscriptions.py`, `model/member.py` in that release.
The exact stock version response is `GNU Mailman 3.3.10 (Tom Sawyer)`.
See the official
[membership API](https://docs.mailman3.org/projects/mailman/en/latest/src/mailman/rest/docs/membership.html)
and [API versions](https://docs.mailman3.org/projects/mailman/en/latest/src/mailman/rest/docs/basic.html).
A modified server/plugin can have additional side effects; the version check is
the supported stock-software contract, not attestation of server code.

Verification includes local TLS fixtures for private credentials, consent/target
drift, notification prerequisites, partial completion, lost responses and
crash-intent recovery, plus an isolated stock Mailman 3.3.10 Core acceptance run.
The real Core run exercised subscription, singleton unsubscription, independent
readback, repeat apply/status/reconcile, and actual list deletion/recreation.
Unmanaged memberships, another list's membership, and owner/moderator roles
retained their IDs. With administrator notices enabled, a new subscription was
refused before mutation, while an explicit unsubscribe succeeded without mail.
With administrator notices disabled, additions and removals succeeded silently
even with welcome/goodbye settings enabled.

An SMTP trap first received four real Core positive-control messages (welcome,
goodbye and administrator subscribe/unsubscribe notices); it then observed zero
messages from the adapter operations, with empty queues at completion. All
addresses were synthetic `.test` addresses, all traffic stayed in an isolated
network namespace, and Core code matched the pinned official wheel and sdist.
The Python 3.13 fixture used the `standard-nntplib` backport, SQLAlchemy 2.0, and
explicit file URIs to the unchanged shipped notification templates to avoid
upstream dependency/template compatibility failures. These fixture conditions
are not a claim of compatibility with arbitrary Python/dependency versions.
No production membership or external SMTP delivery was exercised.

The automatic worker also ran against that stock Core fixture: one approved
policy drove a real subscription and the user's subsequent withdrawal on the
next scheduled poll, without another plan approval or any mail. A competing
worker was refused; stopping and restarting preserved the same history without
replaying mutations. Source revocation and actual target recreation halted the
worker. Local TLS tests additionally cover unresolved late commits followed by
opposite consent, explicit recovery, published-round crash recovery, retention
of active journals, and failed checkpoints reporting only durable observations.
