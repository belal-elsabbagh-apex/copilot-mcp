---
id: uipath-writes-guarded
files: src/uipath/actions.ts, src/uipath/safety.ts
---

`src/uipath/actions.ts` is the ONLY module that mutates Orchestrator state, via
`add_queue_item`, `delete_queue_item`, and `start_job`. All three take `env: z.literal("pre_prod")`
at the schema layer AND assert `env === "pre_prod"` again in the domain function — dev-clone
only, defense in depth.

Every posted `SpecificContent` passes `guardQueueItemSafety` (`src/uipath/safety.ts`, pure,
unit-tested — the single enforcement point, also used by `build_queue_item`/`pull_queue_item`):
- `IsApproved` is forced `false`.
- Non-empty `serverURL`/`queueUrl` must match the configured pre-prod values (fails closed if
  unconfigured).
- `<TO-FILL>` placeholders are rejected.

`delete_queue_item` is fetch-first and refuses to delete any item whose `Status` isn't `New`.

The MCP never repins releases and never creates queues. `list_processes`/`list_queues`/
`list_triggers` are read-only discovery/verification tools — dev-clone queue ids differ from
the prod ids in `PORTALS`, so verification must go through these, not assumption.

**Why:** a queue item that reaches Orchestrator with `IsApproved=true` or a prod callback URL
can trigger a real automation run against real patient data.

**Violation signature:** a new Orchestrator write added outside `uipath/actions.ts`; a queue-item
builder that sets `IsApproved` from caller input; a `serverURL`/`queueUrl` check that's
skipped when the config value is empty (should fail closed, not open); `delete_queue_item`
deleting without first re-fetching and checking `Status`.
