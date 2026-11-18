# Legacy account source archive

`--account-archive` extracts private evidence from the actual PHP `user` and
`customer_to_users` tables. It creates no current account, organization
membership, password, MFA enrollment, or invitation. Ordinary inventory import
continues to reject user accounts. An archive checksum detects changes to the
artifact; it does not authenticate the old database or prove a person's identity.

The source is one UTF-8 JSON object with the exact `tables` key. Both table arrays
must be explicitly present, including an empty array when applicable. Other
tables and nonselected columns are ignored. The whitelist is:

| Table | Selected columns |
| --- | --- |
| `user` | `id`, `username`, `name`, `email`, `disabled`, `custid`, `created`, `created_at` |
| `customer_to_users` | `id`, `user_id`, `customer_id`, `privs`, `created_at` |

Use the columns that actually exist. The 2020 timestamp migration renamed
`user.created` to `user.created_at`; do not invent either value. `custid` is the
old default customer and never substitutes for a `customer_to_users` row. Original
PHP privileges are 0 public/non-user, 1 customer user, 2 customer administrator,
and 3 superuser. A source superuser is not automatically a new global admin.

`account-source.sql` is a read-only MySQL 5.7.22+/8 export template for the modern
column names. Run it against a consistent offline copy of the old database. If
the source uses nontransactional tables, obtain the consistent database copy
before exporting. For the older schema, replace the user object's `created_at`
key and `u.created_at` reference with `created` and `u.created`. The template has
not been executed against production data.

```sh
umask 077
set -C
mysql --defaults-extra-file=/private/client.cnf \
  --default-character-set=utf8mb4 --batch --raw --skip-column-names ixpmanager \
  < cmd/ixp-migrate/account-source.sql > /private/account-source.json
ixp-migrate --account-archive --input /private/account-source.json \
  --output /private/account-archive.json --source-timezone unknown
```

The client configuration is read only by the database client, never by
`ixp-migrate`. Do not put credentials on the command line. Use `UTC` or an explicit
IANA `--source-timezone` only after verifying the export session timezone and the
meaning of any old DATETIME values. The converter does not replace unknown dates
with export time or treat a date-only value as an exact creation instant. Explicit
RFC3339 offsets are usable independently; conflicting creation columns and
ambiguous/nonexistent local times remain unproven.

The output contract is version 1, kind `ixp-manager-account-sources`, source
`ixp-manager-legacy`, access `admin`, with `sourceTimezone`, `checksum`, `users`,
and `memberships`. User rows contain `legacyId` and selected scalar `facts`.
Membership rows also contain `sourceUserId` and `sourceCustomerId`. IDs use exact
positive signed-64-bit decimal strings, including the corresponding facts, so
IDs larger than JavaScript's safe integer range survive. Duplicate row IDs,
ambiguous JSON, aliases, invalid identifiers, complex selected facts, more than
100,000 combined rows, or an archive larger than 32 MiB are rejected. Source
input is limited to 128 MiB. Output requires a new 0600 file in an owned directory
not writable by other users; stdout, replacement, and symlink traversal are
refused. `--strict` refuses this mode because identity review is always required.

Missing/unknown state, disabled users, unusable/colliding email addresses,
unproven creation times, duplicate organization relationships, orphan source
user references, missing relationships, and public/superuser/unknown privileges
are retained with fixed review codes. Unknown scalar values stay in this private
artifact and are not printed in diagnostics. No passwords, password hashes,
TOTP seeds, recovery codes, API keys, remembered tokens, arbitrary preferences,
or login metadata are selected.

## Review and activate in the Go workspace

As a platform administrator, open **成员邀请 → 旧账号批量审核**. Select the original
archive file and the accounts and relationships to migrate. Associate each selected
old organization ID with its current organization. A batch can include up to 25
accounts and 32 selected relationships per account. The server checks the archive
checksum and presents the current organizations and roles for each recipient.

When the source email matches a current member account, verify and select the
specific account, then read a fresh preview. The review flags disabled or unknown
source states, ambiguous identities, duplicate relationships, unsupported
privileges, inactive organizations, and overlapping pending invitations.
Resolve held accounts before confirming the batch. A valid current password and
any enabled MFA are required to queue the reviewed invitations.

Each selected account receives one email covering its reviewed organizations.
The recipient checks all the organizations and roles on the acceptance page.
New accounts choose their own password and inherit a reviewed old default
organization if it was selected. Existing accounts sign in and reauthenticate
with their current password and enabled MFA. Acceptance applies the reviewed
relationships together. The account's global role, existing affiliations and
MFA settings are preserved. If an organization or account changes, read a fresh
preview or revoke the pending invitation and issue a new one as appropriate.

Keep the original archive in private storage. The service keeps the selected
source IDs, archive checksum and organization bindings with each invitation.
These records use the normal invitation retention period.
