---
id: stdout-jsonrpc-channel
files: cmd/copilot-mcp/main.go, internal/server/server.go, internal/server/logging.go, bin/copilot-mcp.js, everywhere
---

stdout is the JSON-RPC channel for the MCP stdio transport. Anything written to stdout that
isn't a protocol message corrupts the connection.

Go has no console redirect to lean on: the rule is that non-test code never writes to stdout at all. Diagnostics
go to `log/slog`, which `server.InstallLogging()` (called first in `main`) points at stderr; silent below warn
unless `LOG_LEVEL=debug` / `COPILOT_MCP_DEBUG=1`. Client-visible logging goes through `s.mcpLog()`
(`internal/server/logging.go`), which sends MCP `notifications/message`. The npm launcher
(`bin/copilot-mcp.js`) spawns the Go binary with `stdio: "inherit"` and writes only to stderr; `make package`
(`scripts/verify_package.sh`) fails if stdout carries more than the single `initialize` response line.

**Why:** a single stray stdout write can silently break every tool call in the session, and
the failure mode (garbled JSON-RPC) is confusing to debug from the client side.

**Violation signature:** `fmt.Print*`, `fmt.Fprint*(os.Stdout`, `os.Stdout.Write`, `log.Print*` (the stdlib
default logger), or `println` anywhere under `cmd/` or `internal/` outside `_test.go`; a
dependency or `slog` handler configured to stdout; `console.log` in `bin/copilot-mcp.js`; logic hoisted above
`server.InstallLogging()` in `main` that can log.
