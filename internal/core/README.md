# Core topology

This package represents desired inventory. Creating or editing it never contacts
a switch, installs a configuration, or claims that a protocol is up.

`Bundle` has one fixed switch per side and 1–128 paired physical links. Types are
`ecmp`, `l2-lag`, and `l3-lag`. The application caps storage at 2,000 bundles and
10,000 links. ECMP uses per-link subnets and BFD; L3 LAG uses bundle subnets and
BFD. L2 LAG has neither. STP belongs to L2 LAG. Both LAG sides require an interface
name and channel group; channel group zero remains a valid explicit value.

Side MTU is 0–64000; zero means unspecified. Endpoint status, duplex, autoneg,
speed and rate limit are desired inventory preserved from legacy physical
interfaces. Missing speed remains unknown. A configured speed differing from
physical inventory produces a warning without overwriting either value.

IPv4 `/31` endpoints are network/next; other IPv4 subnets use network+1/+2.
IPv6 `/127` endpoints are network/next; other IPv6 subnets use network+1/+2.
Subnets must be canonical and contain both unicast endpoints. The application
rejects overlaps within a shared switch or infrastructure, including peering
VLAN ranges and switch loopbacks. Pure inventory may omit switch ASN/loopback
data, but `derived.bgpReady` stays false. Only the IPv4 loopback supplies a BGP
router ID; an IPv6 loopback is never substituted for it. Readiness describes a
configuration draft, not operational BGP state or a deployable switch artifact.

## Aggregate HTTP API

All paths start with `/api/core`. Staff browser accounts can read. Only admin or
operator browser sessions can preview or mutate. API tokens are read-only,
require `core:read` and explicit `switchIds`, and must cover both bundle sides.
Members cannot access core topology even when an internal customer record is
retained in `sideA/sideB.ownerMemberId`.

- `GET /`: `{items, unbundledLinks, capabilities}`.
- `GET /inventory?bundleId=...`: `{switches, ports, members}`. Ports carry
  `_etag`, `available`, `blockedReason`, and `adoptable`. The current bundle's own
  ports are available; unbundled links require explicit adoption.
- `POST /preview`: `{bundle, adoptLinks?: [{id, etag}]}` returns normalized
  `{bundle, derived, warnings}` with stable object IDs. It changes no inventory.
- `POST /bundles`: `{bundle, portEtags, adoptLinks?}` creates one aggregate.
- `GET /bundles/:id`: a flat bundle DTO plus `revision`, `derived`, `warnings`.
- `PUT /bundles/:id`: `{bundle, expectedRevision, portEtags, adoptLinks?}` replaces
  the complete aggregate, including all paired links.
- `DELETE /bundles/:id`: `{expectedRevision, reason}` deletes its logical
  topology, preserving physical ports and SNMP mappings.

`expectedRevision` is the response's opaque review `revision`, not the persisted
`bindingRevision`. The former covers the complete bundle and current switch,
port and allocation dependencies. `portEtags` must include every old and new
endpoint port and use the tokens from **core inventory**, not generic resources.
Core port tokens also cover switch metadata and occupancy. Missing preconditions
return 428; changed revisions return 409. Clients must retain drafts and require
new review instead of automatically refreshing a token and retrying.

`bindingRevision`, `createdAt`, and `updatedAt` are server managed. Existing
metadata may be omitted or echoed unchanged, never overwritten. Successful
mutations rotate the binding and stamp old/new physical port observation epochs.
Core-bound ports are reserved. Removing a link or bundle keeps that reservation;
releasing it for member use is a separate reviewed inventory action.

Adoption requires each selected old flat link's current resource ETag and the
same explicit port pair in the new aggregate (side reversal is permitted). It
preserves the old layer and atomically removes the old flat record; its complete
metadata is retained in the adoption audit. A transaction failing validation
leaves the old link and all ports untouched. Legacy flat links remain occupied
until explicitly adopted or removed; their type never implies ECMP versus LAG.

## Observations and provisioning boundary

The application computes physical observations using verified per-port SNMP
bindings. It reports freshness and per-side evidence. IF-MIB port Up cannot prove
LACP membership, BGP establishment, or BFD state: their current adapters are
unsupported and must remain visibly so. Capacity counts each physical link once,
only when both inventory speeds are present and equal. It is not measured
throughput. Traffic must retain A/B directions without summing both endpoints.

There is no implicit backup/standby role. Cost and preference remain legacy
configuration metadata, not an automatic failover policy. Provisioning may use
real core objects for monitoring, but must not infer device configuration or
health from `enabled`, desired endpoint status, or `bgpReady`.

## Switch configuration plans

`BuildSwitchPlan(switchID, bundles, PlanInventory)` builds deterministic typed
JSON for a target-specific adapter, such as reviewed Ansible input. It carries
local physical interface names, logical aggregate names, desired physical
attributes, interface addresses, BGP neighbor ASNs/cost/preference, and separate
IPv4/IPv6 floodlists. Floodlists include only active managed switches in the same
infrastructure, excluding the target. An inactive target has shut-down interface
and neighbor intent; inactive neighbors are explicitly shut down.

ECMP emits one routed physical interface and neighbor per link/address family.
L3 LAG emits one aggregate interface and neighbor per family, with bundle-level
BFD. Physical LAG members have no duplicate logical addresses. L2 LAG emits STP
and aggregate membership without invented BGP. IPv4 loopbacks use `/32`, IPv6
loopbacks use `/128`; only IPv4 supplies a BGP router ID. Routed plans require
both switch ASNs, IPv4 router IDs and loopback names. IPv6 additionally requires
the target side's explicitly reviewed MTU to be at least 1280. Missing values are
errors; inventory can still be saved for later completion.

`ValidateSwitchPlan` validates bounded structure and internal relationships,
including physical membership, distinct names, matching neighbor addresses and
ASNs, one neighbor per routed interface/family, and correct router ID/loopback
pairing. It does not attest that the inventory exists on a device. Plan flags
must remain `inventoryOnly: true`, `nativeValidated: false`, and
`requiresTargetAdapter: true`. No native device configuration is generated or
installed. Interface names and descriptions are JSON data; a downstream adapter
must validate target syntax and must never interpolate them into a shell.
