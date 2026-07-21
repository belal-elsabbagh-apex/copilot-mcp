---
id: tool-response-contract
files: src/server.ts, src/shared/util.ts, src/server.test.ts
---

Every registered tool returns via the `ok()` / `err()` helpers, and every `registerTool` call
declares all four annotation hints: `readOnlyHint`, `destructiveHint`, `idempotentHint`,
`openWorldHint`. `server.test.ts` enforces this wiring — it should fail if a tool is missing
either.

**Why:** MCP hosts use the annotation hints to decide how much autonomy/confirmation to grant
a tool call (e.g. surfacing destructive, non-idempotent tools differently). A tool that's
mistagged as read-only when it writes state, or missing hints entirely, defeats that.

**Violation signature:** a `server.registerTool(...)` call missing one of the four annotation
keys; a handler that returns a raw object/string instead of going through `ok()`/`err()`; a
write tool (anything touching `copilot-client.ts`'s `req` with a mutating verb, or
`uipath/actions.ts`) tagged `readOnlyHint: true` or `destructiveHint: false` when it isn't
actually safe to retry/no-op.
