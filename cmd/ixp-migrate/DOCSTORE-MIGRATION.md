# Offline Docstore migration

`ixp-migrate` reads an operator-supplied, table-oriented JSON export. It does not
connect to the old application, resolve storage configuration, read `.env`, or
fetch a remote disk. Export the original tables without transforming scalar
values: `docstore_directories`, `docstore_customer_directories`, `docstore_files`,
`docstore_customer_files`, and `docstore_logs`. Include `cust` when customer file
references should resolve to the same `legacy-members-N` IDs as the inventory.

First export and import resource inventory using the ordinary migration command.
It explicitly warns that Docstore archives and logo images need their separate
bundles; `--strict` refuses that omission. Existing patch-attachment rules remain
in force. Then create a separate private Docstore bundle from the same export:

```sh
ixp-migrate --input legacy-tables.json --output inventory.json
ixp-migrate --input legacy-tables.json --docstore-bundle \
  --docstore-disk-root docstore=/offline/storage/docstore \
  --docstore-disk-root docstore_customers=/offline/storage/docstore_customers \
  --source-timezone Europe/Dublin --output docstore.tar.gz
```

Each root is an explicit offline directory corresponding to the exact old `disk`
value. Additional disk aliases require additional `--docstore-disk-root` flags.
The old `path` is opened relative to that root. The display name, SHA-256, current
customer ID and directory hierarchy are never used to guess a storage path.
Paths must be canonical and relative; symbolic links in any component, hard
links, FIFOs and other special files are rejected. A source checksum, when
present, must match the actual bytes. If it is absent, a warning distinguishes a
newly calculated digest from evidence of a match with the original digest.

The output is a new mode-0600 file, published atomically without replacing an
existing destination. Source bytes are privately snapshotted before publication.
The manifest is the first tar entry, followed only by its declared regular files
at `files/shared-N` and `files/customer-N`. The reader checks each actual size and
SHA-256, physical tar headers, padding, end markers and the gzip footer. Duplicate,
missing, extra, linked and extension entries, concatenated gzip streams and
nonzero trailing data are rejected. A failed import must discard all provisional
blobs before committing any state.

Limits are 25 MiB per file, 32 MiB for the manifest, 1 GiB of decompressed archive
bytes, 1 GiB + 8 MiB compressed, 8,192 files and 100,000 total metadata records.
Larger sources fail explicitly; no truncation or silent file omission occurs.
The exporter supports positive signed-64-bit source IDs; the old SQL schema uses
unsigned bigint Docstore IDs, so larger values are rejected rather than rounded.

The archive preserves shared/customer namespaces, original folders, scalar
source dates and descriptions. Broken/cyclic directory trees and cross-customer
parent/file-directory relationships are rejected. Missing customer mappings and
missing/unknown `min_privs` remain staff-review records; an unknown privilege is
represented by `-1`, never the public level `0`. Public shared files retain level
0; levels 1, 2 and 3 retain authenticated member, current member administrator
and staff requirements respectively. Customer files additionally require the
same current member entity. Import does not grant access by matching a display
name, old uploader ID or old user account ID.

Legacy folders can contain files with different privileges. The application
therefore retains the source hierarchy in its staff-only archive and uses a
separate immutable document container for each imported file. Native imported
documents start at version 1, with no invented previous content versions. Source
names and dates stay in provenance; import timestamps identify the new import.

`docstore_logs` refers only to shared files. Its `downloaded_by` is an old user
ID, not a new account mapping; null remains unknown. The old controller records
both authenticated text views and download attempts before file I/O, and permits
log purging. Records have no outcome, action type, IP address or content version.
They are staff-only source records and never become successful-download events,
new audit events, or a claim of complete access history. Source dates remain raw
text with the declared source timezone; `unknown` is accepted with a warning.

These semantics follow `app/Models/Docstore*.php`, the two file policies, the
Docstore controllers and `database/schema/2021-as-at-end-v5.sql` in this source
tree. Patch-panel attachments use their existing independent migration path.
