# Member logos

Use **Members → View member Logo** for staff, or **My network → Member Logo**
for the selected organization. Admins/operators and that organization's member
administrators may upload, replace, remove, or change publication. Ordinary
members and viewers have read access only. No remote-image URL is accepted.
The main MoeDove application mark is independent and is never changed here.

PNG and JPEG uploads are actually decoded and re-encoded to passive PNG, removing
metadata and trailing content. SVG and other image formats are rejected. Limits:
5 MiB input and normalized output, 4096 pixels per side, 8 million pixels per
image. Two image-processing requests may run simultaneously. The resulting
private blobs share the document/LOA quota; replacement removes the obsolete
blob only after the metadata transaction commits.

New images are private. Publication is explicit and only active members have a
public image response. Replacement retains the reviewed publication choice;
deleting or unpublishing removes subsequent public access. Previously saved
public copies cannot be recalled. Member identity is bound to its creation
generation, not just its reusable ID or logo URL. Use `ixp-backup` for complete
private backups: generic resource JSON deliberately excludes logo content and
publication state.

## Offline legacy migration

Legacy source is the `logos` table and `cust` relationship. A legacy file lives
under the explicitly supplied **old public directory**:

```text
logos/<stored_name[0]>/<stored_name[1]>/<stored_name>
```

`stored_name` must be exactly a lowercase SHA-1 digest plus `.png`, and the bytes
must match that original digest. The converter does not derive paths from
`original_name`, inspect `.env`, resolve a URL, or follow symlinks/hard links.
Missing files, conflicting current logos, unresolved customers, or unsafe image
content stop the conversion. `uploaded_at` / later `created_at` and uploader
text remain source descriptions, not newly authorized users or historical audit
events. Actual pixel dimensions are measured from the decoded image.

1. Generate and import the normal resource JSON first. When the source contains
   patch attachments, use the existing patch bundle to import its resource
   inventory. Normal JSON and patch bundles warn that Logo and Docstore archives
   require their own imports; no manual table removal is needed for those two
   domains. `--strict` rejects these omission warnings.
2. Generate a separate private Logo bundle:

   ```sh
   ixp-migrate --input /offline/tables.json \
     --logo-bundle --logo-public-root /offline/old-public \
     --output /private/member-logos.json
   ```

3. In **Settings → Offline member Logo migration**, select the bundle, preview
   the target members, then confirm private import. The reviewed member identities
   and logo versions are checked again at commit. A changed target requires a new
   preview. Existing logo history is never overwritten by a legacy batch; use
   the ordinary Logo editor for an explicit replacement.
4. Review publication per member. Old static-file public access is not imported.

The bundle is a bounded JSON envelope with kind `ixp-manager-member-logos`,
version `1`, and `items` containing fixed source metadata and base64 normalized
PNG. It contains no resource inventory, paths, publication grants or credentials.
Limits are 1000 members, 32 MiB combined PNG bytes, 64 million combined decoded
pixels, and 48 MiB encoded bundle. Output files are private (0600) and never
overwrite existing files. Each member receives an independent blob even when
the old PNG was shared by several source records.

Browser endpoints are `/api/members/{id}/logo` (metadata and mutations),
`/api/member/logo` (selected-member alias), and `/logo/content` (private GET/HEAD).
Mutations require the displayed `expectedRevision`; POST uses multipart `file`
and optional `published`, while PUT requires explicit `published` and DELETE
only the revision. Missing review is 428; changed review is 409. Public images
use `/api/public/members/{id}/logo`. All content responses are `no-store` and
recheck current authorization after reading the blob.

The import preview/commit endpoints are `/api/import/member-logos/preview` and
`/api/import/member-logos`, with multipart `bundle`; commit additionally requires
the returned `reviewToken`. They are administrator-browser-only and audit the
actual importing account and time. An unsuccessful batch leaves no new blobs
or partial member logos.
