# Copilot order audit log — `GET /api/v1/orders/{orderUid}/audit`

The per-order activity trail the Copilot UI shows: who did what to an order, and when.
It is the only place that names the **actor** behind a field edit or a submit. The order
row's `statusTimestamps` and `actionsHistory` record only times and action kinds.

Status: **reverse-engineered from prod.** Written from a browser HAR and live read-only
calls on 2026-11-15. The route is not in the local `cloud-ehr` checkout (`stable` @
`bbb8e6e080`, 2026-10-12), so there is no source to cite. Anything marked *unverified*
was not seen.

## Request

| | |
|---|---|
| Method / path | `GET /api/v1/orders/{orderUid}/audit` |
| Hosts | UI: `https://copilot.apexmedical.ai`. Backend direct: `https://prod-be-batch.ehrcopilotbe.com` |
| Auth (backend direct) | `authcode: <clinic-scoped JWT>`, the same token `POST /orders/filter` takes. The UI sends it from the browser session (the HAR export strips credentials) |
| Read-only | yes |

| Query param | Values | Default | Observed behaviour |
|---|---|---|---|
| `limit` | integer | 20 | `0` gives the default 20. `1000` returned all 34 entries of a 34-entry order. *Maximum cap unverified* |
| `order` | `asc` \| `desc` | `desc` (UI sends `desc` explicitly) | Anything else returns `400 VALIDATION_ERROR` "`order` must be `asc` or `desc`." |
| `cursor` | opaque string from `nextCursor` | none | Keyset paging. Page 2 continues exactly where page 1 ended, with no overlap |

*Unverified:* any filter params (by action, actor or date). None were seen in the UI's request.

## Response `200`

```jsonc
{
  "orderUid": "<orderUid>",
  "patientId": "<number>",
  "items": [ AuditItem, ... ],          // sorted by (timestamp, id) in `order` direction
  "nextCursor": "<string>" | null       // null = last page
}
```

`nextCursor` is base64url JSON `{"ts":"<µs ISO-8601>","id":"<item id>"}`. Treat it as opaque.

### `AuditItem`

| Field | Type | Notes |
|---|---|---|
| `id` | string (numeric) | Monotonic. Breaks ties between items with the same `timestamp`, which does happen |
| `orderUid` | string | |
| `timestamp` | ISO-8601, ms | |
| `source` | `http` \| `portal` \| `sqs` \| `scheduler` \| `automation` \| `system` | Which channel wrote it. `http` = any user-token API call, **including bots** (see Gotchas) |
| `actionGroup` / `action` | see table below | |
| `actor` | `{staffUserId, displayName, isSystem}` | `staffUserId` is a numeric string for people and `system:<name>` for system actors |
| `text` | string | Human sentence shown in the UI ("moved the order New → Submit.") |
| `details` | object | Always has `traceId` and `requestId`. Other keys depend on the action (below) |

### Observed actions

| `actionGroup` | `action` | Typical `details` |
|---|---|---|
| lifecycle | `ORDER_CREATED`, `ORDER_PROCESSED` | `transition` |
| transition | `ORDER_TAB_CHANGED` | `transition` |
| submission | `ORDER_SUBMITTED`, `ORDER_PORTAL_SUBMISSION_FAILED` | `transition` (failure `text` carries the reason, e.g. "Referred provider") |
| edit | `ORDER_FIELDS_EDITED` | `changes`, sometimes `transition` |
| attachment | `ORDER_ATTACHMENTS_CHANGED`, `ORDER_CHART_DOCUMENTS_SYNCED` | `attachments` |
| fax | `ORDER_FAX_REGENERATED` | none |
| bookmark | `ORDER_BOOKMARKED`, `ORDER_UNBOOKMARKED`, `ORDER_BOOKMARK_OPENED` | none |

This list comes from 2 orders and is not exhaustive.

### Observed system actors

`system:portal` (Payer portal automation), `system:automation` (Automation),
`system:scheduler:create-referral-orders` (Scheduler), `system:eligibility` (System (Eligibility)),
`system:sqs:<queue name>` (Background worker, e.g. `AI Queue`, `Eligibility V2 Callback Queue`).

### `details` shapes

```jsonc
"transition": {                                    // from is null on ORDER_CREATED
  "from": {"category": "For Review", "tab": "Submit"},
  "to":   {"category": "Processing", "tab": "In Progress"}
}
"changes": {                                       // keyed by UI label, values are display strings
  "Referred Facility":  {"from": "SYNOVATION MEDICAL GROUP", "to": "Sage Pain & Wellness Institute"},
  "Referred Affiliate": {"from": "(unknown)", "to": "(not set)"}
}
"attachments": {
  "list": "fax", "listLabel": "the outbound fax", "reorderSource": "auto-sort",
  "counts": {"added": 0, "checked": 0, "removed": 0, "unchecked": 0},
  "added": [{"name", "category", "selected", "attachmentUid"}], "removed": [], "checked": [], "unchecked": [],
  "reordered": {"from": ["..."], "to": ["..."]}
}
```

`changes` labels seen so far: Referred To, Referred Facility, Referred Affiliate, Referred
Speciality, Referring Provider, Location, Fax To / Fax To Name / Fax To Phone / Fax To Address,
Fax From / Fax From Phone / Fax From Address, Referred / Referring Fax, Referred Phone,
Referred Address, and `Referral Form *`. The placeholders `(unknown)` and `(not set)` are
display text, not nulls.

## Errors

| Status | Body | When |
|---|---|---|
| 400 | `{"error":{"code":"VALIDATION_ERROR",…}}` | bad `order` |
| 403 | `{"error":{"code":"E4003","message":"Unauthorized",…auth_error_type:"missing_token"}}` | no `authcode` |
| 404 | `{"error":{"code":"E7155","message":"Order not found"}}` | unknown uid, or an order outside the token's clinic (*the second case is unverified*) |

## Gotchas

- **PHI.** `ORDER_FIELDS_EDITED` diffs for `Referral Form *` carry the patient's name, DOB,
  member ID, phone and address in plain text. Only fax numbers are masked (`***-***-1234`).
  Keep responses local and never paste them into issues.
- **Bot writes show up as people.** A bot's `/selected-facility` callback is logged as
  `source:"http"`, `ORDER_FIELDS_EDITED`, with the actor being whichever user the queue
  item's `token` belongs to. Decode the JWT payload `id` and it matches `staffUserId`.
  Example: Gateway order `ef431276`, 12:39:20 UTC, logged under a clinic physician
  (staff 57). Only the bot's *failure* callbacks use `system:portal`.
- **An in-network check is logged as a submit.** It appears as `ORDER_SUBMITTED` "started
  submitting the order", while the order row's `actionsHistory` says `in_network_check_action`.
- **Bot candidates aren't logged.** A `/selected-facility` callback logs only the field changes
  of the facility the bot marked chosen. The other candidates it offered never appear, so the
  audit cannot show that a later manual pick was one of the bot's lower-ranked options.

## Example

```bash
# token from copilot-mcp get_login_token {profile, env:"prod"}; never echo it
curl -s -H "authcode: $TOK" \
  "https://prod-be-batch.ehrcopilotbe.com/api/v1/orders/<orderUid>/audit?limit=100&order=asc" \
| jq -r '.items[] | [.timestamp, .source, .action, .actor.displayName, .text] | @tsv'
```
