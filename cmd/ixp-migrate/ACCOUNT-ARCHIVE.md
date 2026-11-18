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

The next authorization step is separate: an administrator explicitly maps source
organization IDs to current organization entities and source identities to a
reviewed invitation. An email match is a candidate, never proof or an automatic
merge. New accounts receive fresh IDs and passwords through mailbox invitation
acceptance; existing accounts must sign in and reauthenticate with their current
password and enabled MFA. Disabled/unknown or ambiguous source rows must remain
on hold. Current global roles and MFA secrets are never derived from this file.
Even rows with no review findings still require this explicit process.
