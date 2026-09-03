# UiPath Orchestrator Webhooks — Integration Research

**Status:** Research / proposal — no code changes yet.
**Scope:** Automation Cloud Orchestrator only (this server's `uipath.orchestratorUrl`
schema requires a `cloud.uipath.com/{org}/{tenant}/orchestrator_` URL; standalone-only
webhook behavior is out of scope).

## 1. How UiPath webhooks work

- **Subscription model.** A webhook is a **tenant-scoped entity** managed via
  `{orchestratorUrl}/odata/Webhooks` (`GET`/`POST`/`PUT`/`DELETE`). Fields: `Name`
  (mandatory since the 2023 API change), `Url`, `Secret` (up to 100 chars, optional but
  required for signature verification), `Enabled`, `SubscribeToAllEvents`,
  `AllowInsecureSsl`, and `Events[]` (list of subscribed event types).
  `GET .../odata/Webhooks/UiPath.Server.Configuration.OData.GetEventTypes()` enumerates
  every subscribable event type for the tenant.
- **Event catalog.**
  - Job: `Job.started`, `Job.completed`, `Job.faulted`, `Job.stopped`, `Job.suspended`
  - QueueItem: `QueueItem.added`, `QueueItem.transactionStarted`,
    `QueueItem.transactionCompleted`, `QueueItem.transactionFailed`,
    `QueueItem.transactionAbandoned`, `QueueItem.retried`
  - Queue: `Queue.created`, `Queue.updated`, `Queue.deleted`
  - Robot, Process, Trigger events (create/update/delete/status)
  - Every payload carries common properties: `Type` (string), `EventId` (unique per
    event), `Timestamp` (RFC-3339/8601), `TenantId`.
- **Delivery is push, synchronous.** Orchestrator POSTs JSON to `Url` as events happen —
  there is no pull/replay API for missed events.
- **Folder scoping.** Events are generated per folder. A resource shared across folders
  (e.g. a queue) produces **one delivery per folder** it's visible in.
- **Authentication — HMAC-SHA256 over the raw body.**
  1. Read the `X-UiPath-Signature` header.
  2. Base64-decode it to get the raw signature bytes.
  3. Take the raw UTF-8 request body (do **not** re-serialize/reformat JSON before
     hashing — re-serialization whitespace differences are a common validation bug).
  4. Compute `HMAC-SHA256(secret_utf8, raw_body)`.
  5. Compare to the decoded header bytes (constant-time compare); reject on mismatch.
  There is no other authentication on the request — signature verification is
  mandatory, not optional hardening.
- **Reliability caveat — circuit breaker.** If deliveries to a `Url` keep failing,
  Orchestrator's circuit breaker **opens for 1 hour**, silently dropping events to that
  webhook for the duration. There is no dead-letter queue or backfill/replay API — an
  hour of receiver downtime is an hour of permanently lost events.
- **Transport requirement.** `Url` must be a real, reachable endpoint at subscribe time;
  HTTPS is required unless `AllowInsecureSsl` is set. Orchestrator does not tolerate an
  endpoint that only exists intermittently.

Sources: UiPath Orchestrator docs — *About Webhooks*, *Webhooks Requests* (API guide),
*Types of Events*, *Managing Webhooks*; May 2023 release notes (mandatory `Name`).

## 2. The core architectural tension

This MCP server is **stdio-only**: `src/server.ts` opens a single
`StdioServerTransport` and nothing else (see `isEntrypoint()` at the bottom of the
file). It has no listening port, and the host process spawns/kills it per client
session — there is no guarantee of uptime independent of a connected client.

Webhooks need the opposite: an **always-reachable HTTP endpoint** with uptime
independent of any particular MCP session (compounded by the 1-hour circuit-breaker
penalty for downtime). So "adopt webhooks" is really two separable concerns that
must not be conflated:

1. **Managing the subscription** — create/list/enable/delete webhook definitions in
   Orchestrator, choose events/folders. This fits naturally as ordinary MCP tools
   (same shape as the existing `list_queues`/`list_triggers`/`add_queue_item`
   read+write pairs in `src/uipath/`).
2. **Receiving deliveries** — this cannot live inside the stdio process in any
   reliable way; it needs a component with independent uptime.

## 3. Integration options

### A. Decoupled receiver + durable store + query tools — recommended foundation

A small standalone HTTP listener (e.g. `Bun.serve`, run persistently via `hub start`,
**not** part of `server.ts`'s stdio path) verifies `X-UiPath-Signature` and appends
validated events to a local SQLite file (e.g. `webhook-events.sqlite`). New read-only
MCP tools — `list_webhook_events`, `get_webhook_event` — query that store, mirroring
the existing pull-oriented tool style (`list_jobs`, `get_job`). New management tools —
`list_webhooks`, `create_webhook`, `delete_webhook`, `test_webhook` (Orchestrator
exposes a ping/test-send action) — wrap the `odata/Webhooks` API the way
`src/uipath/actions.ts` already wraps queue/job mutations.

This turns push into "pull the buffered history," the only pattern that survives the
stdio process's ephemeral lifetime: an agent session that starts an hour after a
`Job.faulted` event still sees it.

- **Pro:** fits the existing pull-tool architecture exactly; no protocol extension;
  events durable across server restarts.
- **Con:** one more persistent process to run/deploy; a new store/schema to maintain
  and back up.

### B. Same as A, but the receiver/store is remote

Identical shape, but the receiver lives off-box (Cloudflare Worker/Azure Function +
Turso/Postgres) so events land even when the dev workstation is off; tools call it over
HTTPS instead of local SQLite.

- **Pro:** survives the workstation being offline (directly mitigates the 1-hour
  circuit-breaker risk).
- **Con:** real infra to provision/pay for/secure; likely overkill for a
  local/personal-use server.

### C. Embedded listener inside the stdio server process

Have `server.ts` optionally `Bun.serve()` a port when a config flag
(e.g. `uipath.webhook.listen`) is set.

- **Pro:** zero extra moving parts for local testing.
- **Con:** not viable as the primary mechanism — multiple concurrent host-spawned
  instances collide on the port, and the listener dies with the session, so it isn't a
  real receiver. At best a `doctor`-style local dev aid layered on top of A.

### D. Push into a live MCP session (resource-update / logging notifications)

If a client happens to be connected when an event lands, `mcpLog`
(`src/mcp/notify.ts`) or a resource-update notification could surface it live.

- **Pro:** near-real-time UX when a session is open.
- **Con:** cannot be the delivery guarantee — most events arrive with nobody
  listening. Only viable as a bonus layered on top of A/B's durable store, never as the
  source of truth.

### E. Bypass MCP for the automated reaction, keep MCP for investigation

For high-value events (`Job.faulted`, `QueueItem.transactionFailed`), have the receiver
call the existing pure formatter directly —
`formatFaultedJobIssue`/`normalizeError` in `src/uipath/faults.ts` — and file/update the
GitHub issue itself, with no agent in the loop. MCP tools stay read-only for
inspection/history/subscription management. This extends the pattern `faults.ts`
already documents (today a **prompt** hands the built payload to the host's GitHub MCP
server); this option automates that hand-off instead of requiring a human to notice a
fault and ask.

- **Pro:** closes the loop on the most valuable event type without waiting on an agent
  session; reuses already-unit-tested pure logic.
- **Con:** a genuinely automated write path (files GitHub issues unattended) — needs
  its own dedupe/rate-limit care independent of MCP's tool-call model.

## 4. Recommendation

Build **A** as the foundation: a receiver process + SQLite event log + query tools
(`list_webhook_events`, `get_webhook_event`) + management tools
(`list_webhooks`, `create_webhook`, `delete_webhook`, `test_webhook`). Layer **E** on
top specifically for `Job.faulted`/`QueueItem.transactionFailed`, auto-building the same
issue payload `build_faulted_job_issue` already produces. Skip **C** entirely. Keep
**B** in reserve only if "must survive the workstation being off" becomes a real
requirement. Add **D** as a cheap incremental notification once A exists and a session
happens to be connected.

## 5. Open questions before implementation

- **Config shape.** New `uipath.webhook` section in `copilot-mcp.config.json`
  (`UipathSchema` in `src/config/config.ts`) — needs `receiverUrl` (what Orchestrator
  should call), `secret`, and receiver-side listen settings (port, SQLite path).
- **Receiver process lifecycle.** Run via `hub start` (persist across omp sessions) —
  confirm it should be a separate `bin`/script bundled by `bun build`, not folded into
  `dist/server.js`.
- **Event retention.** How long to keep rows in `webhook-events.sqlite` before pruning;
  whether prod vs pre-prod events need separate scoping (mirroring the
  prod/pre-prod isolation invariant already enforced elsewhere in this server).
- **Folder scoping in `create_webhook`.** Decide whether tools default to
  `SubscribeToAllEvents` per folder or require explicit event lists per the existing
  "never default env" invariant style.
