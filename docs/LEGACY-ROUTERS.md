# Legacy router inventory conversion

`ixp-migrate` now recognizes the real legacy **`routers`** table. It produces
reviewable inventory through the existing resource-import format. By default it
creates no peers, keys, approvals, configuration revisions, deployment jobs or automatic
update authority. Existing configuration generation and release gates stay in
place; an imported router record is not proof of platform or template parity.
The separate explicit [`--peer-drafts` workflow](LEGACY-PEER-DRAFTS.md) can derive
disabled candidates only when source selection, ownership and limits are proven.

```sh
install -d -m 700 ./private-migration
ixp-migrate --input legacy-tables.json \
  --output ./private-migration/router-inventory.json
```

Review the warnings and resulting identities before using the administrator's
inventory import. An existing output file is not replaced. `--strict` refuses
router conversion because its warnings identify routing-policy review still
required. A small synthetic source is available in
[`tools/migrate/example-legacy-routers.json`](../tools/migrate/example-legacy-routers.json).

The authoritative old contract is defined by
[`app/Models/Router.php`](../app/Models/Router.php),
[`app/Http/Requests/StoreRouter.php`](../app/Http/Requests/StoreRouter.php),
[`database/schema/2021-as-at-end-v5.sql`](../database/schema/2021-as-at-end-v5.sql),
and the subsequent router-pairing migration. Each old row declares its own
protocol, IPv4 BGP router ID, peering IP, ASN and VLAN. The old templates read
these row values directly. The converter does not obtain missing values from
the exchange settings, a customer, management host, paired router or IP pool.

| Legacy `software` | Preserved platform | Imported roles |
| --- | --- | --- |
| `1` | `bird1` | Route server, route collector; separate IPv4/IPv6 instances |
| `6` | `bird2` | Route server, route collector |
| `7` | `bird3` | Route server, route collector |
| `3` | `frr` | Route server, route collector |
| `4` | `openbgpd` | Route server, route collector |
| `2` | `quagga` | Route-server inventory only; separate current platform/version validation required |
| `5` | `cisco` | Route-server inventory only; separate current platform/version validation required |
| `99` or unknown | No invented platform | Omitted with a review warning; never guessed as Junos or RouterOS |

Legacy type `1` maps to `route-server` and `2` to `route-collector`. Unsupported
Cisco/Quagga collectors are omitted, with their declared role reported rather
than coerced. Type `99` is not assumed to be a route server. AS112 type `3`
requires a separately reviewed service selection and DNS/node configuration;
its old protocol and ASN alone cannot supply those missing settings.

Accepted rows receive stable `legacy-routers-ID` strings without floating-point
identity conversion. Protocol `4` supplies only `localIpv4`; protocol `6`
supplies only `localIpv6`. Rows are never merged based on pairing or a shared management host.
The current Go import requires router IDs to be unique within one infrastructure,
including the implicit default. All conflicting convertible candidates are
omitted with a warning, even across different VLANs or address families; their
router IDs are never silently renumbered. A legacy v4/v6 pair sharing one router
ID therefore needs explicit source/target identity review before import. The same
ID in distinct explicitly converted infrastructures remains separate and valid. IPv6 still requires its own explicitly recorded
**IPv4** `router_id`; it is never copied from the paired IPv4 row or inferred
from a peering address. ASN, address family and a successfully converted VLAN
with a matching prefix are mandatory. All rows claiming a duplicate peering
address on the same VLAN are omitted, including when a competing row is otherwise
unsupported. No arbitrary first owner is selected.

Source policy evidence is deliberately conservative:

- `quarantine`, `rpki` and `bgp_lc` must be explicitly false/0. Missing, malformed
  or enabled values require separate policy review and the row is omitted. The
  converter cannot infer RPKI cache settings or reconstruct old community and
  quarantine policies. Explicit `rpki=0` becomes `rpkiPolicy:"off"`; absence is
  never silently converted to disabled.
- For BIRD 2/3 and OpenBGPD, an explicit `rfc1997_passthru` true/false is retained
  as `rfc1997Passthrough`. On other platforms, an explicit true value is rejected
  rather than discarded; explicit false is reported without adding an unsupported
  override. Missing/null values remain omitted and require review of native
  defaults, especially OpenBGPD's different default.
- `skip_md5` and `pause_updates`, when present, must be valid boolean/0/1 values.
  Their source values are reported but do not import any session authentication
  or old updater authority. All resulting records are inert inventory.
- `pair_id` is the old configuration/update mutual-exclusion relationship, not
  evidence that two rows should become one dual-stack router. Pair coordination,
  old templates, management/API endpoints, access levels, source timestamps and
  software/OS version text remain outside the new inventory record. Ignored
  field names are reported, while their values and credentials are not logged.

Do not erase or falsify source flags merely to bypass a review warning. Preserve
the original private database export and register unsupported routers through
the relevant explicit configuration workflow once its missing inputs are known.
This converter adds no archive import API and makes no claim of restoring the
old templates' filtering, timers, large communities, passwords or update locks.

All conversion modes now validate the bounded source JSON before decoding:
duplicate fields, malformed Unicode, excessive nesting and trailing documents
are refused without echoing source values. The existing 128 MiB input limit and
integer-preserving decoding remain in force.
