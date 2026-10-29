---
id: uipath-writes-guarded
files: internal/uipath/actions.go, internal/uipath/backend_writes.go, internal/orchestrator/safety.go
---

`internal/uipath/actions.go` is the ONLY file that mutates UiPath Orchestrator state, via
`addQueueItem`, `deleteQueueItem`, `startJob`, and `stopJob` (exposed through `backend_writes.go`). All four
are `pre_prod`-only at the schema layer (`preProdOnly()`, `internal/server/tools_orchestrator_common.go`) AND
assert `env == config.PreProd` again in the domain function (`assertPreProd`) — dev-clone only, defense in depth.

Every posted `SpecificContent` passes `orchestrator.GuardQueueItemSafety` (`internal/orchestrator/safety.go`,
pure, unit-tested — the single enforcement point for BOTH orchestrators, also used by
`build_queue_item`/`pull_queue_item`):
- `IsApproved` is forced `false`.
- Non-empty `serverURL`/`queueUrl` must match the configured pre-prod values (fails closed if
  unconfigured).
- `<TO-FILL>` placeholders are rejected.

`deleteQueueItem` is fetch-first (`assertDeletable`) and refuses to delete any item whose `Status` isn't `New`.
`stopJob` is likewise fetch-first (`assertStoppable`; resolving the caller's GUID `Key` to Orchestrator's numeric
`Id` scoped to the pre-prod folder — so a prod job's `Key` resolves to nothing) and refuses to stop a job whose
`State` is already `Successful`/`Faulted`/`Stopped`. Both fetch-first reads (`FetchJobByKey`, `GetQueueItem`) are
the raw, uncached calls — never `readcache.go`. UiPath writes are retried on a 401 (once, with a fresh token) and a 429 (up to `shared.MaxRateLimitRetries`) exactly
like reads — neither means the request was processed, so a replay cannot double-post — and never on a network
error (`request` in `internal/uipath/client.go`).

The MCP never repins releases and never creates queues. `list_processes`/`list_queues`/
`list_triggers` are read-only discovery/verification tools — dev-clone queue ids differ from
the prod ids in `reference.Portals`, so verification must go through these, not assumption.

**Why:** a queue item that reaches Orchestrator with `IsApproved=true` or a prod callback URL
can trigger a real automation run against real patient data.

**Violation signature:** a new Orchestrator write added outside `uipath/actions.go`; a queue-item
builder that sets `IsApproved` from caller input; a `serverURL`/`queueUrl` check that's
skipped when the config value is empty (should fail closed, not open); `deleteQueueItem`
deleting without first re-fetching and checking `Status`; `stopJob` posting `StopJob` without
first re-fetching and checking `State`; a fetch-first guard reading through a cache.
