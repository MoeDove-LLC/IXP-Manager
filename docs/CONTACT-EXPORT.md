# Contact group exports

Staff browser accounts (`admin`, `operator`, and read-only `viewer`) can open
**Contacts → Export by contact group** (`联系人 → 按联系组导出`). Select one group
name or type, optionally restrict the member, and download JSON or CSV. The
dialog initially selects no group. Member accounts retain their own contact
view. Administrator and operator automation can use a dedicated
`contacts:export` API token with an explicit group selector. The endpoint is
read-only.

The private, read-only endpoint is `GET /api/contact-export`:

| Query | Result |
| --- | --- |
| `options=1` | Minimal available names/types, matching group counts and member names/eligibility; no contact details |
| `name=Operations` | JSON rows for all groups with exactly this name |
| `type=ROLE&format=csv` | CSV rows for all groups with exactly this type |
| `name=Operations&memberId=MEMBER_ID&format=json` | Further restrict rows to the selected current member ID |

Exactly one nonempty `name` or `type` is required. `format` defaults to `json`.
Unknown or repeated query parameters are rejected. The selected group values
use exact case-sensitive comparisons. Equal names across distinct group records
are intentionally combined, matching the old SQL query's union semantics; the
dialog shows the number of matched groups. Multiple matching memberships for one
contact produce one row. Distinct contacts sharing an email remain distinct.

The retained `contact:export-group` columns, in order, are:

`name, position, email, phone, mobile, notes, cust_status`

Only fields already visible to that staff account are selected. Account
credentials, contact IDs, group relationship records, billing details and other
member metadata are not included. `cust_status` maps current statuses to the
legacy labels: `active` → `Normal`, `pending` → `Not Connected`, and `suspended`
→ `Suspended`; an absent/unrecognized status is `Unknown`.

Like `app/Console/Commands/Contact/ExportGroup.php`, eligibility requires
`joinedAt <= today` and an empty `leftAt` or `leftAt >= today`. Both boundaries
are inclusive. Missing or invalid join dates are excluded; creation time is not
treated as a join date. Status does not filter otherwise eligible members.
Group `active` and `limitedTo` remain metadata, as in the original command; the
current staff/contact visibility policy supplies authorization.

Differences from the old command are explicit:

- The date boundary is the server's **UTC calendar date**, rather than the old
  database session's timezone. Database collation-dependent case matching is
  replaced by exact matching.
- Current member IDs replace legacy numeric `--cid`. Use the Contacts browser
  workflow or the scoped HTTP endpoint.
- JSON is an array with the same seven field names. Values are strings; absent
  optional fields are empty strings instead of SQL null. It preserves original
  phone/email/text values without numeric conversion. Rows have deterministic
  contact-ID ordering; the original command did not specify ordering.
- CSV is UTF-8 without BOM, with every field quoted, doubled embedded quotes,
  and CRLF row separators. It includes a header even when no contacts match;
  empty JSON remains `[]`.
- CSV adds a visible `text: ` prefix to nonempty phone/mobile fields, preserving
  plus signs and leading zeroes in spreadsheet applications. It also prefixes
  other fields beginning with formula triggers, including full-width variants
  and triggers hidden behind whitespace/control characters. This deliberately
  changes CSV cell values; choose **JSON for exact-value interchange**. Quoting
  alone does not prevent spreadsheet formula interpretation, and escaping
  behavior differs across spreadsheet applications. See [OWASP's CSV injection
  guidance](https://community.owasp.org/attacks/CSV_Injection).
- A request is limited to 10,000 rows and 16 MiB. Oversized requests fail with
  413 before any partial file is returned; choose a narrower group or member.

Downloads use private/no-store responses, attachment disposition, fixed safe
filenames and content-type protection. Current session or token authority is rechecked
after encoding one consistent snapshot. The Store lock is released before
writing the response, so a slow download does not block inventory updates.
A failed refresh or download disables the dialog's
export action until the user explicitly reloads current selector data.

## Automated group export

Give the integration a token with only `contacts:export`. Supply one exact
`name` or `type` for each request. Keep the authorization header in a private
file so the token stays out of command arguments.

```sh
curl --fail --silent --show-error --get \
  --header @/private/contact-export.headers \
  --data-urlencode 'name=Operations' --data-urlencode 'format=json' \
  https://manager.example/api/contact-export
```

The `options=1` selector endpoint is reserved for the staff browser workflow.
The token cannot use other resource or export endpoints unless independently
granted their scopes.
