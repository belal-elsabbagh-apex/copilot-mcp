---
id: tool-response-contract
files: internal/server/tool.go, internal/server/tools_*.go, internal/server/registry_test.go
---

Every tool is registered through `addTool` (`internal/server/tool.go`), which renders the handler's result as
one indented-JSON text block and turns every returned error or recovered panic into the `{error, reportIssue?}`
error body (`toolError`). Every `mcp.Tool` declares a title, a description and all four annotation hints
(`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`) — via `readOnlyHints(openWorld)` for reads
and `writeHints(destructive, idempotent)` for writes. `registry_test.go` enforces this wiring
(`TestEveryToolDeclaresTitleDescriptionAndAllFourAnnotationHints`,
`TestReadOnlyToolsAreNonDestructiveAndIdempotent`) and `TestRegistersExactlyTheExpectedTools` pins the tool list.

**Why:** MCP hosts use the annotation hints to decide how much autonomy/confirmation to grant
a tool call (e.g. surfacing destructive, non-idempotent tools differently). A tool that's
mistagged as read-only when it writes state, or missing hints entirely, defeats that.

**Violation signature:** an `mcp.AddTool`/`s.Server.AddTool` call that bypasses `addTool`; a tool with
`Annotations` nil or built by hand with a missing hint; a handler that builds its own `mcp.CallToolResult`
instead of returning a value/error; a write tool (anything that reaches `copilot.Client.Req` with a mutating
verb, `internal/uipath/actions.go`, or `internal/apex/actions.go`) registered with `readOnlyHints(...)`, or one
whose name is missing from `writeTools` in `registry_test.go`.
