//nolint:staticcheck // SEP-2577 deprecates MCP logging, but the TS server advertised and used it, so the surface stays.
package server

// Client-visible logging (notifications/message + logging/setLevel). Every use of the SDK's
// deprecated logging API is confined to this file so the deprecation has one home.

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultLogLevel is the minimum severity clients see until they send logging/setLevel. The SDK's
// own default is "send nothing until the client sets a level"; the TS server defaulted to info, so
// Connect seeds every session with this.
const DefaultLogLevel = "info"

// loggingCapabilities declares `logging` explicitly so notifications/message + logging/setLevel
// are advertised (tools/resources/prompts/completions are inferred by the SDK).
func loggingCapabilities() *mcp.ServerCapabilities {
	return &mcp.ServerCapabilities{Logging: &mcp.LoggingCapabilities{}}
}

// mcpLog sends a structured log notification ({message, ...data}, logger "copilot") to every
// connected session. level is an RFC-5424 name (debug, info, notice, warning, error, ...). The SDK
// drops it below each session's level; errors (no client, capability not negotiated) are swallowed.
func (s *Server) mcpLog(level, message string, data map[string]any) {
	payload := map[string]any{"message": message}
	for k, v := range data {
		payload[k] = v
	}
	for ss := range s.Sessions() {
		_ = ss.Log(context.Background(), &mcp.LoggingMessageParams{Level: mcp.LoggingLevel(level), Logger: "copilot", Data: payload})
	}
}

// Connect shadows mcp.Server.Connect so every session starts at DefaultLogLevel instead of the
// SDK's "mute until logging/setLevel". Use it (or Run) instead of the embedded method.
func (s *Server) Connect(ctx context.Context, t mcp.Transport) (*mcp.ServerSession, error) {
	return s.Server.Connect(ctx, t, &mcp.ServerSessionOptions{State: &mcp.ServerSessionState{LogLevel: DefaultLogLevel}})
}
