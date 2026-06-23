// MCP notification helpers: client-visible logging (notifications/message) and
// progress (notifications/progress). These are side-channels — they must never
// throw into a tool handler, and never write to stdout (that's the JSON-RPC
// channel; see the console redirection in server.ts).

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { RequestHandlerExtra } from "@modelcontextprotocol/sdk/shared/protocol.js";
import {
  type LoggingLevel,
  type ServerNotification,
  type ServerRequest,
  SetLevelRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";

// RFC-5424 severity, low -> high. A log line is emitted only when its level is at
// least as severe as the client-requested minimum (default "info").
const SEVERITY: Record<LoggingLevel, number> = {
  debug: 0,
  info: 1,
  notice: 2,
  warning: 3,
  error: 4,
  critical: 5,
  alert: 6,
  emergency: 7,
};

let minLevel: LoggingLevel = "info";

// Exposed for tests; not part of the server's runtime flow.
export const _currentLevel = (): LoggingLevel => minLevel;

/**
 * Wire up the `logging/setLevel` request so clients can tune verbosity at runtime.
 * McpServer does not register this handler itself; we add it once after construction.
 */
export function registerLogging(server: McpServer): void {
  try {
    server.server.setRequestHandler(SetLevelRequestSchema, (req) => {
      minLevel = req.params.level;
      return {};
    });
  } catch {
    // Already registered (e.g. double init in tests) — leave the existing handler.
  }
}

type Extra = RequestHandlerExtra<ServerRequest, ServerNotification>;

/**
 * Send a structured MCP log notification. No-ops below the current level and
 * swallows any error (no client connected, capability not negotiated, etc.).
 */
export function mcpLog(
  server: McpServer,
  level: LoggingLevel,
  message: string,
  data?: Record<string, unknown>,
): void {
  if (SEVERITY[level] < SEVERITY[minLevel]) return;
  try {
    void server.server
      .sendLoggingMessage({ level, logger: "copilot", data: { message, ...data } })
      .catch(() => {});
  } catch {
    // not connected — drop it
  }
}

/**
 * Emit a progress notification for the in-flight request, but only when the client
 * opted in by supplying a progressToken in the request `_meta`.
 */
export function reportProgress(
  extra: Extra,
  progress: number,
  total?: number,
  message?: string,
): void {
  const progressToken = extra._meta?.progressToken;
  if (progressToken === undefined) return;
  const params: {
    progressToken: string | number;
    progress: number;
    total?: number;
    message?: string;
  } = { progressToken, progress };
  if (total !== undefined) params.total = total;
  if (message !== undefined) params.message = message;
  try {
    void extra.sendNotification({ method: "notifications/progress", params }).catch(() => {});
  } catch {
    // transport gone — drop it
  }
}
