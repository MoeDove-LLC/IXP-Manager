# ixp-sync

The existing flag-based command fetches an allowlisted public PeeringDB snapshot
and optionally submits an IXP Manager review proposal. It never applies that
proposal. Existing `--provider-config`, `--asn`, `--out`, and optional submission
flags retain their behavior.

The explicit mailing subcommands synchronize a reviewed list of user intentions
to a fixed Mailman Core 3.3.10 target:

- `mailing-plan --config FILE --out NEW_FILE`: read-only preview, private plan.
- `mailing-apply --config FILE --plan FILE --journal FILE --checksum SHA256
  --exclusive-access-acknowledged`: explicitly apply the reviewed plan.
- `mailing-reconcile --config FILE --plan FILE --journal FILE --checksum SHA256`:
  read back an uncertain operation; never repeat a write.
- `mailing-status --config FILE --plan FILE --journal FILE`: persisted counts,
  without contacting either service or claiming current synchronization.
- `mailing-watch-init --config FILE --plan FILE --checksum SHA256 --state-dir
  NEW_DIR --enable-automatic-sync --exclusive-access-acknowledged --max-changes N
  --interval-seconds N`: authorize future explicit source choices for the first
  reviewed target entity and a bounded number of changes per round.
- `mailing-watch --config FILE --state-dir DIR [--once]`: foreground periodic
  synchronization, or one round under that saved policy. Ordinary user preference
  changes do not need another operator checksum approval.
- Watch modes `--status`, `--reconcile-only`, `--resume --checksum POLICY_SHA256`,
  and `--prune-keep N --checksum POLICY_SHA256` are mutually exclusive. A failure
  persists across restart; unknown results must be reconciled read-only before
  explicit resume. Pruning removes only validated closed rounds, preserving
  authorization and active/uncertain work.

See [configuration, notification suppression, recovery and verification
boundaries](../../internal/mailingsync/README.md). No credentials or addresses
are printed in the summary. Private plan/journal files contain addresses.
