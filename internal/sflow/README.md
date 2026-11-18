# sFlow v5 metadata collection

This package implements the [official sFlow v5 datagram format](https://sflow.org/SFLOW-DATAGRAM5.txt)
and standard records described in the [v5 specification](https://sflow.org/sflow_version_5.txt).
Wire layout was cross-checked against the official [sflowtool definitions](https://github.com/sflow/sflowtool/blob/master/src/sflow.h)
and [pmacct definitions](https://github.com/pmacct/pmacct/blob/master/src/sflow.h).
It does not execute either external decoder.

`Parse` supports compact and expanded flow/counter samples, raw Ethernet/IPv4/IPv6
headers, decoded Ethernet/IPv4/IPv6 records, and extended-switch VLAN metadata.
It rejects malformed framing as a whole: callers never receive partial accepted
flows. Bounds are 65,507 datagram bytes, 256 samples, 256 records per sample,
4,096 captured header bytes, four VLAN tags and eight IPv6 extension headers.
Unknown extensions are length-skipped and counted. Unsupported or short packet
metadata remains explicitly unattributed. MPLS and proprietary encapsulations
are not inferred. Compact/expanded mixing within one datagram is rejected.

Only MAC addresses, address family, VLAN/physical-interface metadata, packet
size, sampling rate and limited quality fields survive parsing. Raw captured
headers, application payload, host IP addresses, TCP/UDP ports, usernames and
URLs are never stored or uploaded. `sampledTcp179Activity` means an observed
TCP/179 ACK sample without SYN/RST/FIN; it never means BGP Established.

FLOW estimates use `packetSize × samplingRate` bytes and `samplingRate` packets,
serialized as decimal strings. They are sampling estimates, not complete octet
counters or measured per-member billing data. Packet size follows the exported
record's layer and may include Ethernet FCS; stripped bytes are not added twice.
CNTR samples have a separate typed collection. Max-value wire counters may be
unavailable or just before wrap; this package does not turn them into zero or
merge them into the authenticated SNMP counter history.

## Operator configuration

Use a dedicated ordinary user and two owned regular files with mode `0600`:

```json
{
  "endpoint": "https://manager.example/api/sflow",
  "bind": "192.0.2.20:6343",
  "maxDatagramsPerSecond": 100,
  "exporters": [{
    "id": "exporter-one",
    "switchId": "switch-one",
    "sourceIp": "192.0.2.1",
    "agentAddress": "192.0.2.1",
    "subAgentId": 0
  }]
}
```

```sh
ixp-sflow check-config --config /etc/moedove/sflow.json
ixp-sflow listen --config /etc/moedove/sflow.json --token-file /etc/moedove/sflow.token
```

Only `listen` opens the explicitly configured UDP endpoint. It is a collector
socket, not a packet capture interface. The token comes only from its separate
private file; it never appears in command arguments, emitted snapshots or error
details. HTTPS certificate verification is mandatory, redirects and ambient HTTP
proxies are disabled, requests have fixed paths/timeouts/size limits. Forwarding
stays disabled unless the separate opt-in below is supplied. Exporters require all three
matches: transport source IP, claimed agent address and sub-agent ID. Network ACLs
and a trusted management network remain necessary: UDP allowlisting is not
cryptographic source authentication.

Before opening a socket, the collector fetches current exporter binding leases
from `GET /api/sflow/bindings?exporterId=...`. On lease refresh it closes the old
socket before fetching new leases, then reopens it to discard buffered old-epoch
packets. Sequence tracking survives that refresh. Mapping conflicts, temporary
HTTP failures/rate limits and transport failures close the socket immediately,
discard the failed packet and buffered queue, then retry binding acquisition
with exponential backoff from one second up to 30 seconds. Only packets received
on a newly opened socket may use the refreshed lease; no failed packet is
retagged or resent. Successful upload resets the delay. HTTP401/403 and permanent
configuration/protocol errors stop the collector. The fixed
upload path is `POST /api/sflow/ingest`; the server must independently validate
the tuple, binding revision, current authoritative MAC/port/VLAN mapping and
sequence quality. No client-supplied member/connection attribution is accepted.

Bindings explicitly carry server `issuedAt` and `expiresAt`. The collector
records local monotonic instants before/after the HTTPS request, estimates server
time at response as `issuedAt + RTT/2`, and advances that estimate by monotonic
elapsed time for `receivedAt`. It does not compare authorization timestamps to
local wall time. Remaining lease time conservatively subtracts the entire RTT
from `expiresAt - issuedAt`, then refreshes early. HTTP requests are bounded to
15 seconds; excessive round-trip time or a consumed lease causes a new binding
request rather than extending authorization. With stable server clocks, the
midpoint uncertainty is up to half the request round trip; asymmetric delay can
shift the estimate either way. This is a server-aligned receipt estimate, never
an exact packet capture timestamp. The server still validates mapping revision
and its authoritative resource time boundaries before accepting the sample.

`receivedAt` is collector receipt time, not a precise on-wire packet timestamp.
Network delay, UDP loss, capture truncation, agent-reported drops and sampling
uncertainty remain visible limitations. Sequence gaps count missing datagrams;
source sample sequence gaps are distinct. A reset-like sequence/uptime change
is a restart signal, not authenticated proof of a device reboot. Duplicate,
conflicting or out-of-order datagrams must not contribute flow estimates.

## Persistent sampled aggregates

The control plane constructs `AttributedBatch` from authoritative safe IDs. Its
stable ID must identify one event independent of an HTTP retry. `Store.Ingest`
is idempotent for identical IDs/content and rejects an ID reused with different
content. New receipt times are limited to five minutes old / 30 seconds ahead.
`uptimeMs` and `datagramDigest` bind the exporter packet independently of mapping
revision. The digest must be computed by the control plane from validated safe
datagram content, not copied from a client claim.

Every exporter has a durable sequence/uptime/digest checkpoint in the same WAL
transaction and snapshot as its aggregates. This checkpoint survives collector
or manager restarts, mapping changes, and expiration of the ten-minute HTTP-ID
cache. `IngestWithResult` recomputes classification while holding the write lock;
HTTP claims of fresh/first data cannot authorize counting. Exact packet repeats,
same-tuple content conflicts and out-of-order packets add no FLOW estimates.
Low sequence/uptime first creates a non-counted restart candidate; a consecutive,
timing-consistent follow-up can resume as `restart-inferred`. This is a heuristic
under the trusted UDP network assumption, not cryptographic replay protection or
proof of a reboot. A legacy exporter with history but no cursor first establishes
a non-counted baseline. Sequence holes also include local filtering and HTTP
loss; they do not measure exact network loss.

`Classify` is an advisory preview; the write path always checks again. `Status`
returns checkpoint `sequence`/`uptimeMs`, the last observed datagram tuple,
receipt time and classification. These are collection evidence, not exporter
health. Durable cursor count is bounded to 4,096 exporters and is not erased by
ordinary retention.

`Open` owns a private directory and process lock. The bounded `flows.wal` appends
checksummed records and fsyncs before changing memory. Atomic checksummed
`flows.snapshot.json` includes the last WAL sequence; restart safely handles a
crash after snapshot rename but before journal truncation. An incomplete final
WAL write is truncated, while a complete invalid frame fails recovery. A
one-minute compactor removes expired rows and compacts the safe attributed WAL.
Close finishes compaction and rejects later reads/writes.

Live ingestion prepares only the at-most512 touched resolution buckets, one
quality row, one exporter cursor and one idempotency entry. It applies that delta
only after WAL fsync; write/sync or quota failures leave memory unchanged.
Conservative snapshot-size accounting updates from those changed entries and
reserves atomic replacement space without serializing or cloning the whole
database per datagram. Full retention/accounting scans run at open and periodic
maintenance; snapshot serialization runs at the one-minute or 4MiB WAL
compaction boundary. Replay uses the same mutation logic in private memory.

Five-minute directed pair buckets retain 48 hours; UTC-day buckets and exporter
quality summaries retain 30 days. Retention is by collector receipt window.
Rows preserve mapping revision, frozen member/connection IDs, and separate
64-hex source/destination member entity revisions. A member query must supply
its ID and current entity revision, matched together on the same side of the
directed pair. Old rows lacking entity revisions remain staff-only. Reusing a
deleted member ID or returning through an A→B→A allocation cannot expose another
entity's history. Internal authorized `switchIds` restrict both rows and quality;
an empty set means unrestricted staff browser access and must never be supplied
for a bound API token. Empty matrices are not prefilled: no samples means no
row, and unavailable estimates remain empty/unknown. Member queries exclude
unattributed rows and exporter-wide quality; the app must still enforce current
membership and historical-label privacy. Estimated counters are arbitrary
precision decimal strings, never added to complete SNMP counter totals.

Hard limits are 100,000 aggregate/quality rows, 200,000 recent idempotency IDs,
and 128 MiB including atomic snapshot replacement workspace. Ingest fails before
crossing a quota; there is no silent eviction of nonexpired data. Queries are
bounded to retention and 10,000 returned rows with explicit truncation. Exporter
day quality counts datagrams, missing/duplicate/unordered datagrams, restart
signals, sample sequence gaps and separately received counter samples. Quality
is not a network capture completeness percentage.

Back up `flows.snapshot.json` and `flows.wal` under the application process lock
after closing the store; exclude `.lock` and transient `.snapshot-*` files.
All safe IDs and aggregate data remain private even though payload is absent.

`ListFiles(dir)` returns only existing data filenames relative to the sFlow
directory; an absent legacy directory is empty. `ValidateFiles(dir)` reads
bounded original files, validates snapshot/WAL checksums and sequence continuity,
replays unapplied records in memory, and checks cursor/member-entity metadata.
Neither hook calls `Open`, creates a lock/directory, starts maintenance, compacts,
truncates, or repairs source files. A trailing partial WAL write returns
`ErrTornWALTail` without changing bytes. The backup controller can explicitly
preserve that recoverable source or report it; normal restore/open performs the
documented recovery. Unexpected persistent files and unsafe symlinks fail closed.

`Store.Members(switchIDs)` provides the complete bounded authorization directory
of distinct `{id, revision}` identities observed in attributed rows for those
switches, independently of report pagination. It contains no names or totals;
legacy rows without entity revisions and unattributed rows cannot populate it.
The app must verify current entity revision before supplying inventory labels.

Buckets optionally expose `lastTcp179At`, which advances only on an actual
TCP/179 activity sample. Ordinary packets advance `lastReceivedAt` without
changing the activity timestamp. A legacy bucket can retain an activity count
with an unknown timestamp; neither receipt time nor another packet type is
substituted as evidence of BGP activity or state.

## Receiver plans and controlled relay

`Receiver`, `FilterBinding` and `BuildReceiverPlan` describe a member destination
and its explicit exporter→switch→infrastructure→actual VLAN record→physical
ifIndex/MAC→connection mapping. Ambiguous tuples fail; there is no first-match
inventory attribution. Plans expire after 60 seconds and require a fresh
ownership check plus separate control-plane approval before any delivery. A plan
or pretag map is never a reusable execution grant.

`RenderPretag` emits an advisory map using the documented
[pmacct sfacctd pretag keys](https://github.com/pmacct/pmacct/blob/master/examples/pretag.map.example).
Its `ip` key matches the claimed agent address, not transport source IP; its VLAN
matching uses exported in/out VLAN metadata. Consequently this export is not a
tenant delivery ACL and cannot independently authorize forwarding. Each rule
includes agent/sub-agent, VLAN, interface and MAC; identifiers and numbers are
typed and cannot inject commands. This advisory map is not installed by the
collector; native pmacct configuration remains a separate integration.

To relay approved packet headers, add an explicit local policy to the private
collector configuration, then start with `listen --forward`:

```json
"forwarding": {
  "relayAgentAddress": "192.0.2.254",
  "allowedDestinationCidrs": ["198.51.100.0/24"],
  "allowedPorts": [6343],
  "maxReceivers": 8,
  "maxDatagramsPerSecond": 100,
  "maxBytesPerSecond": 131072,
  "maxBytesPerBatch": 65536
}
```

```sh
ixp-sflow listen --config /etc/moedove/sflow.json \
  --token-file /etc/moedove/sflow.token --forward
```

The token additionally needs `sflow:forward`; each binding must advertise
`forwardingAllowed:true` before a socket opens in this mode. The private policy
and `--forward` are both required. Policy presence alone changes no traffic.
Literal destinations must match both an allowed CIDR and port; there is no DNS
lookup, redirect or receiver-supplied command. The relay agent address must differ
from the original exporter address.

For each incoming datagram, `CaptureAt` creates a private immutable set of
sanitized header records and a cryptographically random 32-byte nonce. The
original receive buffer is cleared before HTTPS upload. The request contains
only ordinary safe metadata plus `requestDeliveries`, `requestNonce`, and the
canonical safe `datagramDigest`; no IP addresses, ports or packet bytes enter
manager storage. The manager alone checks current receiver approval, entity and
MAC/connection ownership, then returns a `deliveryEnvelope` with the same nonce,
digest, exporter, switch and mapping revision. Its per-receiver entries bind
receiver revision, explicit address/port, unique `Datagram.Flows` indices and
`headerDataDisclosure:true`. CNTR/unknown sample positions never count as flow
indices. The client compares the envelope with the immutable capture and request;
it does not infer authority from an old plan or cached receiver approval.

Grants last at most five seconds. Forwarding conservatively checks both the
upload RTT budget and the server-aligned capture clock's upper estimate; slow or
uncertain authorization causes no send. A capture is consumed once even on
rejection. Missing/invalid/expired grants, scope denial, failed ingestion,
rebindings and receipt failures cannot resend an old packet.

Output is a **sanitized filtered-relay sFlow v5 stream**, not a copy of the source
datagram or a byte-accurate packet capture:

- Only authorized FLOW samples are reencoded. All CNTR, vendor/unknown records
  and other opaque extensions are removed.
- The explicit relay address and private per-receiver sub-agent/sequence/uptime
  replace source-wide identities and statistics. Sample pool, drops, source ID,
  input and output interfaces are suppressed as unknown zero values; they are
  not observations of source zero traffic or zero drops.
- Original packet size and sampling rate remain per authorized sample. Additional
  filtering/limits mean this stream cannot measure complete source traffic or
  reconstruct source loss statistics.
- Ethernet/VLAN and proven IPv4/IPv6/L4 header boundaries are retained; application
  payload is removed. IP/TCP options are zeroed, IPv6 extensions/fragments stop at
  the base IP header, IPv4 fragments stop at the IP header, and unsupported
  transport stops at its proven boundary. Decoded Ethernet/IPv4/IPv6 records are
  reencoded at their fixed standard lengths; padding and trailing extensions
  are discarded. Inner checksums are not evidence of an unmodified original
  packet, and these prefixes must not be replayed as source traffic.
- Each output UDP datagram is at most1,400 bytes; batches are limited by explicit
  local receiver, packet and byte budgets. The implementation caps policies at
  32 receivers per batch, 10,000 datagrams/s, 16MiB/s and 1MiB/batch. Relay stream
  state is bounded to4,096 receiver IDs. No stale plan grants forwarding authority.

`delivery-receipts` is a separate fixed HTTPS path. Receipts bind the original
nonce/digest/mapping and receiver revision, with counts and outcomes such as
`sent-to-socket`, `blocked-policy`, `expired`, `rate-limited`, `encode-rejected`,
`send-error` and `partial-send-error`. They contain no headers, MAC/IP endpoints
or payload. A local UDP write cannot confirm remote arrival or processing; UDP
also provides no transport authentication or confidentiality. Reports are
attempted once, and a missing report remains unknown rather than provoking a
packet retransmission.

## Validation

Parser and malicious-fixture checks were run only through the root-provided
strict sandbox launcher: no network, empty allowlisted environment, read-only
source/toolchain, private 512 MiB temporary filesystem, AS 4 GiB, CPU 120s,
processes 128, open files 128, file size 64 MiB and wall time 150s. Non-race Go
fixtures exercise normal/expanded packets, truncation/oversized lengths, VLANs,
IPv6, Counter64, metadata-only JSON, private files and fixed HTTP mock transports,
plus WAL replay, torn tail, compaction crash, idempotency and tenant/epoch filters.
No real UDP exporter or switch was contacted or activated during implementation.
Recovery fixtures also cover mapping409/temporary503 refresh without packet
retagging, capped retry delays, revoked-token shutdown, WAL write/fsync failure
atomicity, and a fixed 2,000-row growth check of the bounded transaction working
set. Socket and HTTP transitions use mocks, not a live switch feed.
Receiver tests also use mixed-member payload/vendor/counter fixtures and verify
that each recipient sees only authorized sanitized samples. A separate strict
private-loopback launcher enables only `lo`, drops all target capabilities and
sets no-new-privileges. The explicit UDP fixture verifies `CapEff=0`, `CapBnd=0`
and the absence of non-loopback interfaces before testing a local write/read.
This is the only real UDP send used during implementation; no external receiver
or production packet stream is contacted.
