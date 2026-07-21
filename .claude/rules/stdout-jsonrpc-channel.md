---
id: stdout-jsonrpc-channel
files: src/server.ts, everywhere
---

stdout is the JSON-RPC channel for the MCP stdio transport. Anything written to stdout that
isn't a protocol message corrupts the connection.

`console.*` is redirected to stderr in `server.ts` at startup, so ordinary `console.log`
calls are safe by construction — but any *new* direct write to stdout (`process.stdout.write`,
a third-party logger configured to stdout, a debug `print`-style call added ad hoc) bypasses
that redirection.

Client-visible logging must go through `mcpLog()` (`src/mcp/notify.ts`), which sends MCP
`notifications/message`. Local diagnostics belong on stderr, gated by `LOG_LEVEL=debug` /
`COPILOT_MCP_DEBUG=1`.

**Why:** a single stray stdout write can silently break every tool call in the session, and
the failure mode (garbled JSON-RPC) is confusing to debug from the client side.

**Violation signature:** `process.stdout.write(` anywhere in `src/`; a new dependency that
logs to stdout by default without being redirected; a handler that returns debug output via
`console.log` before the stdout redirection is set up (i.e. logic hoisted above the
redirection call in `server.ts`).
