# IX-F Member Export 0.6 / 0.7 / 1.0

`Build(Input)` produces the default 1.0 public member feed from typed, preselected inventory. `BuildVersion(Input, version)` selects exactly 0.6, 0.7 or 1.0; unknown versions fail instead of silently selecting another format. Both reject invalid included data so a partial feed cannot silently remove an existing downstream participant. Inactive members and connections, excluded exchanges/VLANs, and connections without exported addresses are omitted. All interface speeds are Mbps; each physical member of a LAG stays in `if_list`. The app mapper must not duplicate a LAG as several independent IP connections.

The caller supplies the registered IX-F ID separately from the operator ASN, and stable numeric local IDs for exchanges, switches and VLANs. Values must fit the exact JSON integer range. The registry ID is optional in 0.6: a missing zero-valued ID is omitted, not invented. It is required in 0.7 and 1.0. The package cannot infer IX-F registry identities. Public publication and authentication belong to the HTTP layer; this package never reads a database or performs network requests.

Typed address `Services` describe a service hosted at that exact peering address, not a client connected to an RS. They become `service_type` strings in 0.7, `services` objects in 1.0, and are omitted in 0.6. Unknown service types fail. The inventory mapper does not infer service identities or daemon versions from a member's peer relationship; those optional service records need an explicit inventory association before they can be published.

Only an explicit field allowlist is exported. Contact emails, phone numbers, notes, management addresses, credentials, user records and internal tags are not fields of the input contract. `routeserver` is scoped to each address family. MAC values are normalized to lowercase colon-separated Ethernet addresses.

Schema references are the [current official schema](https://github.com/euro-ix/json-schemas/blob/master/ixp-member-list.schema.json) and Euro-IX's [0.6](https://github.com/euro-ix/json-schemas/blob/master/versions/ixp-member-list-0.6.schema.json) / [0.7](https://github.com/euro-ix/json-schemas/blob/master/versions/ixp-member-list-0.7.schema.json) archives. The unmodified fixtures have these SHA-256 values:

| Fixture | Retrieved | SHA-256 |
| --- | --- | --- |
| `schema-0.6.json` | 2026-11-15 | `ac338101651c15956627335f1740b29a2b1dd6c6d22cd47977be53989adbbf29` |
| `schema-0.7.json` | 2026-11-15 | `88bd4e10aea1f8a507d3488db7c50db65f3c5f02c29f014e9f1cc770e4267efe` |
| `schema-1.0.json` | 2026-11-06 | `9729877265cd48e1dba43eedec0f094402d42227d53c34157f56fb8b01a6e4fe` |

Tests validate output against these fixtures using a bounded schema walker, including dual-stack, LAG units, exclusion, invalid included data, version-specific services and empty arrays. The walker is not a general JSON Schema implementation. This provides versioned public-format compatibility, not byte-for-byte reproduction of all PHP optional metadata, contacts, tags, access-key behavior or stable numeric IDs from a previous installation. Existing downstream ID associations must be checked during migration.

## HTTP and settings page

Staff can download `/api/ixf/0.6`, `/api/ixf/0.7` or `/api/ixf/1.0` using a session or an `exports:read` API token. `/api/ixf` defaults to 1.0. The settings page selects the format and downloads **saved** inventory; selecting a format never enables public access.

These anonymous aliases all require the same explicit `publishIxf` setting:

- `/api/public/ixf/{version}`
- `/api/v4/member-export/ixf/{version}`
- `/apiv1/member-list/list/version/{version}`

Their unversioned forms (`/api/public/ixf`, `/api/v4/member-export/ixf`, `/apiv1/member-list/list`) return 1.0. Public aliases remain unavailable when publication is disabled, including for logged-in users; use the staff endpoint for private downloads. Tokens also use the staff endpoint. Publication is validated against the default 1.0 feed, so a private 0.6 export without registry IDs does not enable anonymous publication of invalid newer formats.

The original well-known `/participants.json` entry point also serves this same public 1.0 feed, with the same publication gate and no redirect to an unauthenticated private endpoint.

Legacy `access_key`, `withtags`, `ixfid_*` and `ignore_missing_ixfid` query parameters do not grant access, reveal private fields or alter registry IDs. Unknown versions return 400. Unavailable public feeds return 503 without inventory details; private staff exports return the validation error. A feed never returns a successful partial list after an included record fails validation.

```sh
go test ./internal/ixf
```
