# Original notes and notification preferences

Create a separate private archive from the same offline table export used for
inventory migration:

```sh
ixp-migrate --input legacy-tables.json --note-archive \
  --source-timezone Europe/Dublin --output note-sources.json
```

This mode runs before ordinary resource conversion. It retains original
`cust_notes` values, including empty notes, unresolved customer references,
whitespace, line endings and source dates. The normal resource import remains
the path for creating editable notes and inventory in the new model. The archive
does not create or replace those records.

Only approved identity labels and note preferences are selected from `user` and
`user_pref`. Passwords, MFA, tokens and other preference branches are excluded.
Modern `prefs.notes` and historical `customer-notes.*` rows remain separate;
missing, null, empty arrays, objects and JSON-string representations are
distinguished. Unknown privacy flags are recorded as internal and need review.
The old UI and notification listener disagreed about an unset notification mode;
missing, `default`, or conflicting source values never turn on notifications.

The source export is limited to 128 MiB. Invalid UTF-8, duplicate JSON keys,
unpaired Unicode surrogate escapes, excessive nesting and oversized structures
are rejected before decoding can alter or discard values. The selected archive
is limited to 32 MiB and 100,000 source records. It is a new mode-0600 JSON file,
published atomically without replacing a destination or following a symlinked
parent. `--strict` refuses any remaining source-review warnings. The checksum
verifies the selected canonical archive, not the authenticity or original bytes
of the entire database.

In the application, an administrator imports this JSON in Settings. Staff can
inspect or download the immutable archive from **Account security → Original
note migration**. Importing does not change current notes, subscriptions, read
marks or the outbox. Source dates retain their declared timezone and precision;
an unknown timezone is not silently interpreted as UTC.

An administrator explicitly associates a source user with a current staff
account and its creation identity. Old IDs and email addresses are never used
to grant access automatically. Source IDs are strings in mapping responses so
JavaScript cannot round large integer identities.

The selected account then reviews its own pending association. Its new choice
starts at **none** and can explicitly select watched members/notes or all notes.
Review shows both current and proposed preferences and the current recipient
email. Acceptance requires an acknowledgement and a five-minute, single-use
review token bound to the current browser session, account identity, security
generation, email, preference revision and selected entities. Concurrent edits,
withdrawn associations, expiry, logout and recreated entities require a fresh
review. A source timestamp or account number cannot bypass those checks.

Acceptance preserves current read marks and never applies old read cursors or
old note bodies. It creates no email. Later note changes follow the chosen
current preferences and ordinary delivery checks; SMTP being disabled does not
queue historical changes for future delivery. Changing a subscription invalidates
previously queued messages bound to its old revision.

Use `ixp-backup` for complete recovery of archives, mappings, acceptance receipts,
current preferences and the encryption key. Resource JSON export excludes these
archives and receipts and reports that exclusion. Archive download preserves the
source package only. After full restore, old browser sessions and their review
tokens cannot be reused; a pending association can be reviewed again after login.

Validation includes bounded parser and CLI race tests, API authorization/CAS and
session-revocation tests, immutable archive checks, browser interaction and narrow
layout checks, and dedicated complete-backup round trips. All mail fixtures are
synthetic; no external email is sent by these tests.
