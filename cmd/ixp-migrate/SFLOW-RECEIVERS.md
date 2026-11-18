# Legacy receiver and registered-MAC migration

`ixp-migrate` now converts the actual legacy receiver table into the application's
top-level `sflowReceivers` import list. It remains an offline converter: it does
not access a database, start a collector, send packets, approve a receiver, or
copy runtime credentials.

## Supported sources

The source is the existing `{ "tables": { ... } }` JSON export. Names follow the
checked repository schema and Eloquent models:

| Source | Relationship | New representation |
| --- | --- | --- |
| `sflow_receiver` | `virtual_interface_id` → `virtualinterface.id` | `sflowReceivers: [{name, spec}]` |
| `virtualinterface` | `custid` → `cust.id` | receiver `spec.memberId` |
| Every `vlaninterface` belonging to that VI | `virtualinterfaceid`, `vlanid` | all converted connection IDs and actual VLAN IDs |
| `l2address` | `vlan_interface_id` → `vlaninterface.id`, `mac` | registered `resources["mac-addresses"]` |
| `macaddress` | learned observations on `virtualinterfaceid` | excluded with an explicit warning; never registration or receiver authority |

`Layer2Address` is the model name, but its actual table is **`l2address`**, and its
foreign-key spelling is **`vlan_interface_id`**. The converter does not guess a
`layer2address` table or conflate that key with a VI reference.

The stock v5 receiver table has `id`, `virtual_interface_id`, `dst_ip`, `dst_port`
and later timestamp columns; it has no `active` column. Its API iterates all
receiver rows without an activation filter. Consequently a standard row becomes
desired `enabled:true`. As an explicitly supported export extension, `active`
may be JSON boolean, integer0/1, or an exact decimal string0/1; false is retained
as `enabled:false`. Null, unknown values, and other nonempty lifecycle/approval
fields (`enabled`, `status`, `approved`, etc.) are rejected rather than guessed.

Sources: [model](../../app/Models/SflowReceiver.php),
[legacy API](../../app/Http/Controllers/Api/V4/SflowReceiverController.php),
[request validation](../../app/Http/Requests/StoreSflowReceiver.php),
[Layer2Address model](../../app/Models/Layer2Address.php), and
[v5 schema](../../database/schema/2021-as-at-end-v5.sql).

## Conversion and safety boundaries

- Receiver IDs are deterministic `legacy-sflow-receiver-<source id>`. Integer
  decoding preserves IDs above JavaScript's exact-integer range. Names are
  generated from the old ID; destination values are not copied into diagnostics.
- Destinations must be literal IPv4/IPv6 unicast addresses, with no hostname,
  prefix, zone, IPv4-mapped IPv6, unspecified/multicast address or limited
  broadcast. Ports must be exact integers1–65535. IPv6 spelling is canonicalized.
- The owner is resolved through the VI customer. Every attached VLI must resolve
  to a successfully converted connection owned by that member. Its physical
  ports must close to the same member, switches and VLAN infrastructure. Missing
  members, ports, VLANs, skipped connections, core VIs, duplicate VLAN attachments
  and ambiguous references abort conversion. The converter never selects the
  first attachment or narrows an incomplete VI to whichever VLAN happened to pass.
- A LAG remains a logical connection with all physical ports. A multi-VLAN VI
  supplies all of its connections/VLANs, bounded by the target's64-item scope.
- Registered MACs are explicit nonzero unicast EUI-48 values, normalized to
  lowercase colon notation. Bare12-hex database values and conventional EUI-48
  text encodings are accepted. Any duplicate/conflicting registration within the
  same actual VLAN record aborts conversion, including duplicates on different
  connections. Identical tags in distinct infrastructures are not conflated.
- Learned `macaddress` values are never promoted. Its values, source credentials,
  approvals, revisions, membership generations and runtime delivery receipts are
  not included. Existing timestamps and first/last-seen evidence remain in the
  source backup; the new application establishes its own trusted timestamps.

`spec.revision` is empty in converter output. The application assigns a fresh
revision and member entity generation and clears all approval. Desired-enabled
receivers enter **draft**, and desired-disabled receivers remain **disabled**.
They still require current device mappings, registered MACs, active inventory,
explicit header-disclosure approval, a scoped collector token and local relay
opt-in before any forwarding. Conversion/import does not perform those actions.

## Fixture and verification

```sh
ixp-migrate -input tools/migrate/example-legacy-sflow-receivers.json \
  -output /private/path/receiver-import.json
```

The fixture contains a two-port LAG with two VLANs, three registered MACs and two
receiver destinations: a standard enabled IPv4 row and an explicitly disabled
IPv6 extension row. It also includes one learned observation that must be excluded
with a warning. Remove that intentionally unsupported observation table to test a
fully accounted `-strict` conversion.

Unknown/ambiguous receiver or registered-MAC ownership aborts output even without
`-strict`; the CLI writes neither a partial stdout document nor an import file.
Other warnings retain the existing converter policy. Error messages identify
table, row ID and field/reason without printing source secret values. Keep the
original export and review the private converted file before application import.

Tests cover complete LAG/multi-VLAN scope, source ID precision, destination and
port bounds, disabled intent, missing/skipped references, same-VLAN MAC conflicts,
cross-fabric closure, learned-data exclusion, and the absence of approval or
secret data. The root integration tests exercise the actual CLI→App import path.
