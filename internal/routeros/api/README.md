# RouterOS API-SSL transport

This package is a standard-library-only, bounded RouterOS binary API client. It exposes fixed inventory dictionaries and typed mutations; it has no public arbitrary sentence, command path, script, REST endpoint or shell execution interface. It does not import the parent `internal/routeros` planner.

The protocol follows MikroTik's [API documentation](https://manual.mikrotik.com/docs/developer-guides/api/): byte-counted words use the five length encodings through a four-byte unsigned length, and a zero-length word ends a sentence. Unknown/reserved length forms and noncanonical encodings fail closed before allocating their advertised size. Replies are read through `!done`; `!empty` is an empty-data marker, **not** command completion. Any `!trap` remains failure even if `!done` follows. `!fatal`, malformed replies, duplicate attributes/tags, wrong tags and output-limit failures close the transport.

`Dial(ctx, Config)` always establishes TLS 1.2 or later, verifies the normal certificate chain and hostname, and optionally also matches a lowercase-hex SHA256 digest of the certificate SPKI. A pin does not bypass normal certificate verification. There is no `InsecureSkipVerify`, plaintext fallback, anonymous-DH mode or legacy challenge-response retry. Current `/login` credentials are sent only after the verified TLS handshake. Login arguments and remote error text are never returned in errors or logged by this package.

## Public interface

- `Read(ctx, Resource)` reads a fixed `/print` path with a fixed `.proplist`, retaining absence rather than inventing defaults. Extra returned properties are discarded.
- `ReadDetailed(ctx, Resource)` uses the same fixed projection with the native detail flag. An absent property still remains unknown; requesting detail does not establish a default value.
- `ReadFilterRuleDisabled(ctx, bool)` has only the fixed filter-rule `disabled=true/false` query and returns native IDs. Duplicate IDs, malformed IDs or an explicit returned boolean contradicting the query fail closed. This is query membership evidence, not an inference about omitted fields; a caller must independently verify the selected sets cover the expected inventory before using them as boolean evidence. The production observer does not currently use this experimental path.
- `Inventory(ctx)` collects system resource/license/identity, optional RouterBOARD information, routing tables, BGP instances/connections, filter rules and a Safe Mode probe. This sequence is **not an atomic device snapshot**. The parent must perform its own freshness, ownership, closed-graph and configuration-drift checks.
- `ReadProperty(ctx, Resource, id, property)` explicitly gets one property from the same fixed non-sensitive dictionary. A missing `ret` gives `Known:false`; an explicit empty `ret` is `Known:true, Value:""`. Neither is silently converted to boolean false.
- `ReadProperties(ctx, requests)` pipelines at most 64 fixed non-sensitive getters on the same authenticated connection. Unique tags associate each completion with its exact object/property, including out-of-order replies. Duplicate/unknown tags, traps, malformed completions or any timeout discard the whole batch and close the connection; no partial evidence or automatic retry is returned. Credentials, login and mutations cannot enter this path.
- `ReadCredential(ctx, connectionID)` is the sole sensitive getter, fixed to `/routing/bgp/connection/get`, `.id` and `value-name=tcp-md5-key`. Only an explicit, unmasked returned value can be known. Missing return data and recognized masking remain unknown. Its exact version/permission behavior still needs device-native validation before the parent may treat it as authentication evidence.
- `ValidateMutation(Mutation)` validates the complete local operation without sending anything. The executor should validate both forward and rollback operations before the first write.
- `Apply(ctx, Mutation)` supports add/set/enable/disable/remove in the four managed routing menus, plus a narrow `Unset` operation. Selectors for existing objects must be native `*HEX` IDs, never console row numbers, names or expressions. Add requires the typed required attributes and a returned native ID. The parent must establish namespace ownership before calling it.
- `Close()` interrupts in-flight I/O and prevents reuse. The client never reconnects or retries automatically.

`Mutation.Property` is required only for `Unset`, whose attributes must be empty and whose selector must be a native `*HEX` ID. Its fixed encoding is `/<menu>/unset`, `=.id=<ID>`, `=value-name=<Property>`. The only allowed clear targets are routing-table `fib` and the two connection `input.limit-process-routes-ipv4/ipv6` properties. Other actions reject `Property`; this is not a generic unset API.

The isolated CHR 7.24.6 probe found that a `fib` API argument is a presence switch: even `fib=no` enabled it. Consequently Add/Set only accept `fib=yes` or `fib=true`, and encode them as the bare `=fib=` switch. `no`, `false` and an empty caller value are rejected before network I/O. A new non-FIB table omits the attribute; clearing an existing flag requires the explicit, separately validated `Unset` path. Empty AF-limit Set values are also rejected; set a positive integer or use the restricted Unset operation. Local validation/encoding success does not prove target-side reset behavior.

The parent compiler passes `Attribute.Value` as the actual API property value. The transport does not add CLI quoting or interpret/unescape `$`, `=`, or scripting syntax. Filter expressions must originate from the parent's constrained compiler, not a user-supplied arbitrary rule field. No mutation can introduce an unrecognized path or property. Connection-level `as` has an explicit scalar ASN setter for the constrained compiler: Add/Set accept only canonical positive decimal uint32 values below 4294967295, and reject empty, pair, leading-zero and unset forms. Its equality with the managed instance ASN is enforced by the parent planner/inspector, not inferred by this transport. Read-only drift properties `input.filter-nlri`, `output.filter-select`, `output.network` and `output.redistribute` still cannot be written through this package.

The caller-supplied `Sensitive` flag cannot make `tcp-md5-key` public: known sensitive property names force redaction in formatting and JSON. `SecretValue` has unexported storage, redacted formatting/JSON, constant-time equality and an explicit copying accessor. Prefer `Credential.Matches(expected)`, which also requires `Known`. A zero/unknown credential never verifies absent authentication. Plaintext copies are for the parent's explicitly private rollback handling, not public plans or logs.

## Error and bound semantics

`Error` exposes a fixed code, optional fixed operation identifiers/category, and `Uncertain`; it contains no raw router message or wrapped socket/TLS error. `IsUncertain(err)` identifies a mutation that may have taken effect. A partial write, lost connection/completion, malformed response, cancellation after submission, or missing add identifier is uncertain. A mutation trap also does not prove that no property changed. Stop and inspect/reconcile instead of automatically resending. Read failures discard all accumulated records rather than returning a misleading empty or partial inventory.

Calls serialize on one connection and generate their own tags. The context deadline includes waiting for that serialization slot. Default limits per command are 64 KiB/word, 128 KiB/sentence, 128 words, 4 MiB reply, 20,000 records and 40,064 sentences. Configurable ceilings are 1 MiB/word, 2 MiB/sentence, 256 words and 16 MiB reply; record/sentence ceilings cannot increase. Defaults are a 5-second connect/login deadline and a 10-second command deadline, each capped at 60 seconds. The decoder also bounds empty-sentence floods.

The getter pipeline holds that same serialization slot for the complete batch, bounds encoded requests to 64 KiB and replies to the configured byte ceiling plus at most two sentences per getter and 16 overhead sentences. Cancellation finishes closing the transport before releasing the slot. Fixed print/getter-batch errors may expose resource, property, byte/record/sentence counts, limit category and whether the caller or command deadline expired. They never contain router values; sensitive single getters do not expose size diagnostics.

## Safe Mode boundary

The [current Safe Mode CLI reference](https://manual.mikrotik.com/docs/cli-reference/safe-mode/) documents read-only `enabled`, `current`, `user` and `owner`. This client probes `/safe-mode/print` and preserves supported/unsupported/unknown capability separately. It does not infer menu availability from 7.18 (that release introduced `!empty`) and does not expose take/release/unroll/toggle mutations.

Safe Mode is not a transaction created by an API or REST request sequence. MikroTik's [configuration-management documentation](https://manual.mikrotik.com/docs/getting-started/configuration-management/) describes session ownership, rollback on abnormal termination and finite history capacity. Actual terminal ownership, isolation, action budget, rollback and confirm behavior belong to the parent controller and its tested native profile. No mock test here establishes those device guarantees.

## Validation

Tests use real `crypto/tls` handshakes over in-memory `net.Pipe` peers. No listener, external target, router daemon or kernel routing change is involved. They cover all length boundaries, malformed/oversized input, certificate and SPKI rejection, current login, tags, empty/done synchronization, trap/done failure, cancellation, partial writes, no automatic retries, fixed dictionaries/selectors, unknown fields, capability probes and credential redaction.

```sh
go test -race ./internal/routeros/api
go vet ./internal/routeros/api
```

The root task's isolated CHR lab must separately verify the exact production release's property names, nil/switch encoding, sensitive getter behavior and transactional supervision. For example, an explicitly verified version-specific `fib` nil normalization belongs in that profile, not in this transport's generic getter.

Native CHR 7.24.6 also distinguished never-initialized MD5 (getter unknown) from an explicitly written empty key (getter known empty). The transport preserves this distinction. `filter-rule.invalid` is not an available getter on that release; the observed `inactive` property needs resource- and exact-version-specific interpretation in the parent, never a global missing=false conversion. The independent native property probe includes invalid-rule cases and remains separate from the no-explicit-delete cancellation proof.

The capacity investigation found that `print detail` still omitted `disabled`
for the 2,006-rule fixture. A separate three-rule true/false predicate probe
also failed exact membership against explicit getters, so boolean query sets
are not used to fill missing flags. The getter pipeline completed individual
large observations in approximately 18–19.5 seconds, but the full 1,000-prefix
transaction with complete reads around every individual activation still
expired before successful staging. A later, separately gated two-level
object/global observation path passed the fixed capacity-and-cancel fixture;
see [the native record](../NATIVE-7.24.6.md). This does not turn missing flags into
defaults or make the getter pipeline itself a transactional guarantee. The
production write gates remain unchanged.
