# Offline legacy statistics

Legacy imports use an independent store. They are reports or consolidated rate samples, never live counter deltas. The importer performs no SQL connection, web fetch, sampled-flow collection or implicit addition of overlapping totals. It does not read a production `.env`, discover credentials, or alter the source export/RRD file.

The repository's `TrafficDaily`, `TrafficDailyPhysInt`, `UploadStatsToDb`, `UploadPhysIntStatsToDb` and `UploadDailyP2p` implementations define the supported table semantics. The original table row ID, customer/physical-interface ID, peer ID where present, category and `day` label remain historical identifiers. They are not joined against today's member or port ownership.

## Formats and units

`LegacyInput` requires an explicit format and source declaration. `source.name`, `source.timezone` (IANA, including explicit `UTC`) and `source.backend` (`mrtg`, `rrd`, `sflow-rrd`) are mandatory. `content` is the offline export text.

| Format | Required declaration | Meaning |
|---|---|---|
| `ixp-table-json` | `source.table` | JSON array of original table records; supported tables are `traffic_daily`, `traffic_daily_phys_ints`, `p2p_daily_stats` |
| `rrd-xport-json` | `source.unit`, `entityKind`, `entityId`, explicit `columns` | Standard RRDtool xport JSON, with or without explicit row timestamps |
| `rrd-xport-xml` | Same as JSON | Standard xport XML; no DTDs, external entities, custom elements or enumerated `<v0>` output |

For SQL reports, `bits` retains bits and bits/s; `pkts`/`bcasts` retain packets and packets/s; `errs`/`discs` retain events and events/s. P2P reports retain legacy sampled estimates in bits and bits/s. The importer does not multiply existing table values by eight again. RRD exports require the original base unit explicitly: `bits`, `bytes`, `packets` or `events`; their selected columns remain rates in that unit per second. A filename or data-source name is not unit evidence.

All numerical values remain decimal strings. Scientific notation is normalized using exact decimal arithmetic; it is never converted through a JavaScript number or float64. `null`, missing values and explicit `NaN` remain unknown with a reason. Existing legacy zeroes stay reported zeroes, with a warning that old graph code may already have replaced missing samples with zero. The lost distinction cannot be recovered. Imported values are preserved as reported, not certified as accurate hardware measurements.

The table format rejects unknown columns rather than silently dropping them. It permits the historical `id`, `day`, entity references, `category`, period statistic fields, physical-interface maximum timestamps, and original `created_at`/`updated_at` fields. The latter timestamps are not guessed to be rolling-window endpoints. Duplicate JSON members, duplicate record IDs, invalid numbers and oversized/deep inputs are rejected.

## Windows and attribution

The SQL `day` in member/physical-interface reports is the save-date label. Their day/week/month/year windows have nominal lengths **119988 / 719712 / 2879712 / 31622400 seconds**. Actual start/end remain `null` unless explicit dated extraction evidence supplies them. These are overlapping rolling windows, so their totals must not be added.

P2P reports preserve their requested calendar date and timezone. Calendar boundaries respect DST, including days whose local midnight is skipped; a genuinely nonexistent local calendar date is rejected for P2P instead of inventing an interval. Calendar-label boundaries do not prove exact underlying RRD coverage. P2P maxima are labeled `legacy-sum-of-independent-maxima`, matching the old job's summation of separate interface peaks; they are not a simultaneous network peak.

For [RRDtool xport](https://oss.oetiker.ch/rrdtool/doc/rrdxport.en.html), each timestamp identifies the end of a consolidated sample interval. Rows retain `[timestamp-step, timestamp)`, the actual exported step, original rates and unknown samples. The importer does not integrate rates into invented totals or replace exported intervals with user-declared windows. Explicit `columns` bind zero-based index and exact legend to `in`/`out` and `average`/`maximum`/`minimum`/`last`. Every exported column must be mapped; the mapping participates in the review checksum. Maximum values remain RRD consolidated rates, not instantaneous peaks.

Every row defaults to `staffOnly:true`. A supplied `LegacyOwnerEvidence` must identify the historical entity, member generation, evidence checksum and validity range. Only an exact exported or explicitly evidenced window fully contained within that range can receive member attribution. A current member ID or today's connection is never sufficient. Ambiguous local SQL maximum timestamps retain their original text and stay unresolved. Historical evidence remains an operator assertion requiring review; the library does not verify a document merely because its checksum was supplied.

## CLI

The CLI is offline and takes the store's exclusive lock. Stop an application using the same store before CLI import, query or retention. It accepts export files; it does not connect to the old database. A minimal synthetic fixture is [legacy-traffic-daily.json](testdata/legacy-traffic-daily.json).

```sh
ixp-traffic-import preview \
  --input traffic_daily.json --format ixp-table-json \
  --source 'Approved old MRTG export' --backend mrtg \
  --timezone Europe/Dublin --table traffic_daily

ixp-traffic-import import \
  --input traffic_daily.json --format ixp-table-json \
  --source 'Approved old MRTG export' --backend mrtg \
  --timezone Europe/Dublin --table traffic_daily \
  --store /srv/ixp/legacy-traffic --checksum REVIEWED_PREVIEW_CHECKSUM

ixp-traffic-import query --store /srv/ixp/legacy-traffic \
  --batch BATCH_ID --period day --from-day 2024-01-01 --to-day 2024-12-31

ixp-traffic-import validate --store /srv/ixp/legacy-traffic
ixp-traffic-import delete --store /srv/ixp/legacy-traffic \
  --batch BATCH_ID --checksum REVIEWED_BATCH_CHECKSUM
```

`--declarations file.json` supplies optional `columns`, `windows` and `owners` arrays. Alternatively, `preview` and `import --spec input.json` read the complete typed `LegacyInput`. `preview` emits `LegacyBatch`; `import` emits immutable `LegacyMetadata`; `list` emits `LegacyCatalog`; `query` emits `LegacyResult`. The CLI's explicit checksum is its local review boundary. It does not grant a browser role or bypass the application's separate proposal, approval, audit and retention authorization.

A bounded native adapter is available for a trusted **local offline RRD snapshot**:

```sh
ixp-traffic-import export-rrd \
  --rrdtool /usr/bin/rrdtool \
  --rrd-root /srv/offline-rrd --rrd-file member42.rrd \
  --in-ds traffic_in --out-ds traffic_out \
  --from 2024-01-01T00:00:00Z --to 2024-01-02T00:00:00Z --step 300 \
  --source 'Approved RRD snapshot' --backend rrd --timezone UTC \
  --unit bytes --entity-kind dataset --entity-id legacy-member42 \
  > input.json

ixp-traffic-import preview --spec input.json
```

`export-rrd` only produces a typed input for subsequent review. It validates a regular trusted executable, contained regular source file, fixed data-source identifiers and whitelisted consolidations. It copies the source into a private temporary directory and runs fixed `rrdtool xport --json --showtime` arguments against that copy. It clears `RRDCACHED_ADDRESS` and other inherited environment, accepts no daemon/remote URL/CDEF/command expression, and limits runtime to 30 seconds and output to 16 MiB. Source snapshots are bounded at 128 MiB. An explicit trusted `--rrd-library-directory` supports isolated local tool installations. The binary RRD format itself is not parsed by Go, and an active producer is not made transactionally consistent by copying: supply an offline snapshot.

Native validation in this workspace used official Debian RRDtool **1.7.2** packages extracted under `/tmp/moedove-rrd`, without system installation. All RRD files and SQL records used in tests are synthetic. This proves the supported parser/adapter behavior, not the correctness or deployment status of any production export.

## Store, queries and backup

`OpenLegacy` opens a private independent directory. Its fixed files are `legacy-manifest.json` and `batches/<64-hex-checksum>.json.gz`. Batch files are immutable, mode0600, atomically published and synced before the manifest. Retries with the same checksum are idempotent. An I/O failure requires restart; restart validates references and reclaims only recognized unreferenced files. Closed instances cannot access a replacement generation.

Limits are 16 MiB input text, 64 MiB decoded normalized batch, 16 MiB compressed batch, 20,000 rows per batch, 2,000 batches and 512 MiB of published compressed batch data, plus bounded manifest/temporary overhead. Quota does not silently discard reports: explicit checksum-bound retention is required. Keep a full backup before deletion if the original reports are still needed.

`LegacyQuery` requires a single `batchId`. It filters original entity/category/family/period/day labels and optionally known window bounds, then paginates (default100, maximum1000). Historical member ID and generation filters apply before pagination. Window filtering returns whole intersecting original reports, never prorated totals; unknown boundaries are excluded. `noAggregation:true` is always returned. No whole-source sum is fabricated across windows, rates or batches.

`LegacyListFiles(dir)` and `LegacyValidateFiles(dir)` validate referenced regular files, gzip bounds, hashes, canonical batch identity and attribution structure without repairing, cleaning or truncating. They return paths relative to the independent store and omit `.lock`, temporary and unreferenced files. Backup must quiesce writers and include the manifest plus exactly those referenced immutable batches. The application's backup layer also rejects extra unreferenced artifacts before publishing a restored directory.

Focused checks:

```sh
go test -race ./internal/traffic ./cmd/ixp-traffic-import -run '^TestLegacy'
RRDTOOL_BIN=/absolute/path/to/rrdtool \
  RRDTOOL_LIBRARY_DIRECTORY=/optional/trusted/library/directory \
  go test -race ./internal/traffic -run '^TestLegacyNative'
```
