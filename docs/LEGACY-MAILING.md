# Migrating legacy mailing choices

The private source archive preserves the old mailing choices. Importing it does not subscribe an address, replace a current preference, send mail, or synchronize a list manager. An administrator explicitly maps source accounts and list keys, and each current account reviews and accepts its selected choices for its current email address.

Create the archive offline from the original table-oriented JSON export:

```sh
install -d -m 700 ./private-migration
ixp-migrate --mailing-archive --input legacy-tables.json \
  --output ./private-migration/mailing-sources.json
```

The converter reads the original `user.prefs.mailinglist` JSON branch and historical `user_pref` rows with an exact `mailinglist.<key>` attribute. The original PHP migration `database/migrations/2021_04_14_125742_user_pref.php` used these historical rows. JSON strings in `user.prefs`, null branches and PHP empty arrays are handled. Arbitrary `users` or `user_preferences` schemas are rejected rather than mapped by assumption; produce an explicit reviewed conversion to the original fields first.

Only users with mailing source choices, selected identity labels, mailing values and the corresponding historical row facts enter this artifact. Other preference branches, passwords and security secrets are excluded. String `"1"`/`"0"` and integer `1`/`0` are recognized, reflecting the old profile form and initialization code. Booleans, null and other bounded scalar values remain unknown. Duplicate choices for the same source account and key remain ambiguous even when they agree. Unknown or ambiguous values cannot be accepted as subscriptions; the account can make a new explicit choice in current mailing settings. Complex values, malformed JSON, duplicate JSON keys and invalid source identities are rejected.

The source JSON is limited to 128 MiB, the selected archive to 16 MiB and the archive records to 50,000. The output must name a new file in a directory owned by the current user and not writable by other users. It is created with mode `0600`, without following or replacing an existing output symlink or file. The converter refuses stdout and incompatible conversion modes. `--strict` refuses this workflow because every source choice requires review.

The archive checksum covers its canonical selected source representation. It is not a signature or the checksum of the original database export. Scalar types, string whitespace and original identity precision are preserved; original JSON whitespace and unrelated fields are not. Keep the original database export privately under the operator's existing retention policy when complete original bytes are needed.

## Review workflow

1. An administrator imports the archive with `POST /api/import/mailing-archive`. Duplicate checksums are rejected. Global source archives and downloads are restricted to administrator browser sessions.
2. The administrator creates current list definitions and explicitly maps a source account to a current account's `id` and `createdAt`, and each selected source key to a current list's `id`, `createdAt` and current revision. Email equality never creates a mapping or grants consent. A missing historical source user remains archived but cannot be mapped until reviewed source evidence supplies its identity.
3. The mapped current account opens `GET /api/account/mailing-migrations`, selects known choices and requests a five-minute review. The response states the current delivery email, list destinations, source choices and whether each choice is available. Unknown raw source values and historical identity labels remain administrator-only.
4. The same browser session explicitly accepts the review token. The token is bound to the mapping revision, account entity, current email, session, source choices, list identity/revision and preference revisions. Any relevant change requires another review. Acceptance records intent only; external application still uses the separately authorized mailing synchronization workflow.

Current-email explicit preferences cannot be overwritten by migration, including choices made before mapping. A preference for a former account email can be replaced only if unchanged since mapping and review; the previous email remains a withdrawal intent. A new account reusing an ID or an address never inherits source authority. Disabled lists cannot gain subscriptions.

Mappings are immutable. To retarget a pending mapping, cancel it using its current revision and create another mapping. Accepted choices retain their receipt and cannot be mapped again from the same archive. Accepting a subset consumes the review and mapping; unselected keys remain source-only and can be explicitly mapped again later. Pending mappings reserve all their selected source keys, accepted mappings reserve only accepted choices, and cancelled mappings reserve none.

## HTTP contract

All migration endpoints require a current browser session. Mutations require CSRF. API tokens, including `mailing-lists:read`, cannot read or modify migration sources.

| Endpoint | Request or response |
| --- | --- |
| `GET /api/mailing-lists/legacy` | Administrator import batches and mapping receipts |
| `GET /api/mailing-lists/legacy/archives/:checksum` | Administrator `{batch, archive, readOnly}`; `?download=1` downloads the canonical archive |
| `POST /api/mailing-lists/legacy/mappings` | `{archiveChecksum, sourceUserId, targetUserId, targetUserCreatedAt, lists:[{sourceKey,listId,listCreatedAt,listRevision}]}` |
| `POST /api/mailing-lists/legacy/mappings/:id/cancel` | `{revision}` |
| `GET /api/account/mailing-migrations` | Own pending mappings, safe choice descriptions and current email |
| `POST /api/account/mailing-migrations/:id/review` | `{revision,email,choices:[{sourceKey,listRevision,preferenceRevision}]}`; returns `{mapping,email,choices,reviewToken,expiresAt,externalApplied:false}` |
| `POST /api/account/mailing-migrations/:id/accept` | `{revision,reviewToken,acknowledge:true}`; returns mapping receipt and current preferences |

Source IDs are decimal strings to preserve signed 64-bit identity precision in browsers. Revisions are JSON-safe integers. Source archives and mapping state have cumulative byte and record bounds. They are excluded from ordinary inventory exports and public feeds, and included in complete private backups. Restore preserves exact canonical sources, receipts and explicit preferences; it does not restore browser sessions, so pending review tokens require a new review after login. Opening or backing up invalid persisted source checksums or bindings fails.
