# Reviewed legacy BGP peer candidates

Ordinary `ixp-migrate` output is unchanged: it does not invent peers from member
connections. An explicit optional workflow derives **disabled candidates** from
the old router and VLAN-interface selection evidence:

```sh
install -d -m 700 ./private-migration
ixp-migrate --input legacy-tables.json \
  --output ./private-migration/peer-drafts.json \
  --peer-drafts --peer-limit-schema split --peer-as-of 2026-11-18
```

Choose the date and column convention from the actual source export. The example
date is not an automatic freshness grant. A synthetic source is provided in
[`tools/migrate/example-legacy-peer-drafts.json`](../tools/migrate/example-legacy-peer-drafts.json).
The options are permitted with ordinary inventory or `--resources-only`, and
not with file/archive bundle modes. `--strict` refuses these review-required
drafts. No App production endpoint or routing release gate is changed.

Each candidate has a stable ID containing its original router ID, VLI ID and
address family, and references only successfully converted router/member records.
It contains the explicitly owned neighbor address and ASN, a proven effective
prefix limit, `enabled:false`, and `allowedPrefixes:[]`. The empty array means
**no approved prefix policy was imported**, not that an old IRR result has been
verified as empty. Nothing is enabled, approved, generated or published. No MD5,
API credential, source filter expression or runtime observation is copied.

## Source selection and ownership

The reference path is `Router/GenerateConfiguration.php` →
`Tasks/Router/ConfigurationGenerator.php` →
`Models/Aggregators/VlanInterfaceAggregator.php`, followed by the selected stock
server/collector template. The original selection requires the router VLAN,
the enabled address family, a connected physical interface, current membership
dates, `cust.status` 1 or 2, and a non-associate customer. Route servers also
require `rsclient`; route collectors do not. `monitorrcbgp` is monitoring metadata,
not a collector subscription gate. Both stock template families exclude the
router's own ASN.

The draft converter checks the full source VLI → VI → customer chain against the
converted connection, member ASN and AF-specific address record. The converted
connection must be fully active, with all represented physical ports explicitly
connected and owned by the same member. This is deliberately narrower than an
old SQL join that could select only connected parts of a partially disconnected
LAG; that case is reported for manual review. No LAG or owner is flattened or
selected by arbitrary row ordering.

Every source VLI address reference participates in ownership checks, including a
disabled address family. Multiple records claiming the same canonical address
on a VLAN refuse the draft output rather than allowing the old template's
address-keyed last-row-wins behavior. A neighbor equal to the router's local
address is not drafted. Existing ambiguous-address/core-topology failures remain
fatal, and unsupported router inventory is never bypassed.

`--peer-as-of` is the **source database calendar date** used to reproduce the old
`CURRENT_DATE()` membership predicate; it is not implicitly today's date or UTC.
`datejoin` must be explicit and on/before that date. `dateleave` must be explicitly
null or on/after the date, including the departure day. DATE strings may be
`YYYY-MM-DD` or the legacy SQL-midnight representation `YYYY-MM-DD 00:00:00`.
Missing dates, other timestamps, unknown customer types, and suspended/expired
membership are not treated as current ownership. Pending customers remain
pending, and their peer drafts remain disabled.

## Limits and supported template scope

The 2025-09-01 max-prefix migration renamed VLI `maxbgpprefix` to
`ipv4maxbgpprefix`, added `ipv6maxbgpprefix` and customer `maxprefixesv6`, then
copied historical shared values into the new IPv6 columns. Declare one era:

| Declaration | VLI override | Customer fallback |
| --- | --- | --- |
| `shared` | `maxbgpprefix` for both AFs | `maxprefixes` for both AFs |
| `split`, IPv4 | `ipv4maxbgpprefix` | `maxprefixes` |
| `split`, IPv6 | `ipv6maxbgpprefix` | `maxprefixesv6` |

Mixed-era columns are rejected. A present, positive per-VLI integer takes
precedence. Only an explicitly null/zero override permits customer fallback.
Absent evidence, invalid/negative values, or an effective limit outside
1–10,000,000 are reported. If both layers lack a positive limit, the old generator
would consult deployment configuration. This tool never reads that configuration
or substitutes modern/default values; no candidate is produced.

Only these reviewed stock template/platform combinations are accepted:

- BIRD 1 RS: `api/v4/router/server/bird/standard`.
- BIRD 2/3 RS: `api/v4/router/server/bird2/standard` or
  `api/v4/router/server/bird2-2025/standard`.
- BIRD 1 collector: `api/v4/router/collector/bird/standard`.

The stock BIRD 2 and BIRD2-2025 collector bodies have **no effective import limit**,
despite the aggregator calculating a possible value. Their candidates are
reported as nonrepresentable because the new peer model requires `maxPrefixes`;
a stored but unused value is not invented as an old session limit. Other
platforms, custom templates, mismatched roles and AS112 are not coerced into the
supported cases. Template paths are compared as metadata, never opened or run.
Custom skins and deployed configuration are not attested by a matching path.

After importing, explicitly review current ownership, source template/skin
differences, prefix/IRR/RPKI policy, the limit and MD5 requirements before enabling
anything. Existing policy, credential, validation, approval and publication
workflows still apply. A source snapshot or a disabled draft is not proof of a
currently established BGP session. Drafts are bounded to 10,000 rows and 100,000
candidate checks; exceeding a bound produces no partial import file.
