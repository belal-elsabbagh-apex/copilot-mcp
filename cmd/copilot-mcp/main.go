// Command copilot-mcp serves the Copilot MCP tools over stdio.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is stamped by the Makefile (-X main.version=...) from the git tag.
var version = "dev"

func main() {
	// stdout is the JSON-RPC channel, so every diagnostic goes to stderr.
	server.InstallLogging()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Debug("copilot MCP server ready (stdio)", "version", version)
	if err := server.New(version).Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
