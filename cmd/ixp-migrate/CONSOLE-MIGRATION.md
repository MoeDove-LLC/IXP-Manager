# Console inventory migration

`console_server` and `consoleserverconnection` are offline inventory sources.
Converting or saving them does not open a terminal, issue credentials, resolve
DNS, connect to equipment, or provision a console server.

The existing converter command includes these tables when supplied:

```sh
ixp-migrate --input offline-tables.json --output console-inventory.json
```

Include `cust`, `location`, `cabinet` and any referenced `vendor` records. Current
foreign keys are authoritative: `custid` refers to `cust.id`, despite the old
PHP `CustomerTag` relationship typo. The obsolete `switchid` column is preserved
only as source evidence, never as a device or access grant.

Servers retain name, hostname or literal management IP, rack/location/vendor,
model, serial number, notes and active intent. Hostnames use pinned official
Go IDNA processing with nontransitional mapping, label/hyphen/joiner/Bidi checks
and DNS size limits. Unicode names become canonical ASCII; the original spelling
is retained in `legacyConsole` when normalization changes it. This is syntax
validation, not a DNS query. Missing/invalid active state becomes disabled with
a warning and original evidence; no operational health is inferred.

Connections retain description/name, member/server references, the original
serial port label, baud rate, parity, stop bits, flow control, autobaud and notes.
`portLabel` is text, not a TCP port number. Leading zeros are significant: `01`
and `1` are different. New labels reject boundary whitespace, controls and
invisible format characters; uniqueness uses NFC plus Unicode case folding
without rewriting the displayed label. Server names use the same comparison.

Old Go numeric `port` values from 1 through 65535 remain valid. They are displayed
as decimal labels. If a request supplies both fields, their text must agree
exactly; to set `01`, omit the numeric field. Updating an old record using the
new label form preserves the label and removes its obsolete numeric value.

Known legacy serial codes are mapped explicitly:

| Source | Target |
| --- | --- |
| speed | One of 300, 600, 1200, 2400, 4800, 9600, 14400, 19200, 28800, 38400, 57600, 115200, 230400 |
| parity 1 / 2 / 3 | even / odd / none |
| stopbits 1 / 2 | 1 / 2 |
| flowcontrol 1 / 2 / 3 | none / rts-cts / xon-xoff |
| autobaud null / missing | Unknown, not an invented false value |

Unknown serial values are retained in read-only `legacySerial` JSON with a
warning; they do not become executable configuration or guessed defaults.
Selecting autobaud does not erase stored manual parameters. Current fields may
be explicitly corrected while keeping original source evidence. `--strict`
refuses any conversion warning before publishing output. Invalid/missing
references or canonical-name/port collisions fail conversion rather than
dropping connections or guessing a replacement owner.

Console inventory remains staff-only. A member association describes equipment
ownership and does not grant portal, terminal or management-host access. The
resource API retains normal ETag concurrency controls. Servers with referenced
connections cannot be deleted; the UI lists the related records instead of
performing an implicit cascade.

Verification uses synthetic data, with dependencies prepared beforehand and
network disabled during tests:

```sh
GOPROXY=off go test -race ./internal/console ./cmd/ixp-migrate ./internal/app -run 'Console|OfflineHostname'
```

The browser inventory workflow is `web/tests/console.mjs`. The separate minimal
`web/tests/document-text-actions.mjs` verifies that legacy text viewing is
offered only for the backend-supported `.txt` / `.md` extensions, while `.csv`
and `.log` remain downloadable without an unsupported preview action.
