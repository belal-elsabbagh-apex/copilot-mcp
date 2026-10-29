// Package server is the MCP wiring layer (the Go port of the v2.0.0 TS server): it constructs the
// server, registers resources, prompts and completions, and owns the helpers every tool file
// builds on — addTool, schema patches, hints, progress and log notifications. Tool logic lives in the domain packages; the tool files in this
// package only translate MCP arguments to calls and results back.
package server

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server is the MCP server plus the version every failure payload and issue URL carries.
type Server struct {
	*mcp.Server
	// Version is the build's semver tag, advertised as serverInfo.version and embedded in the
	// prefilled GitHub-issue URL on unexpected failures.
	Version string
}

// New builds the fully registered server: resources, prompts, completions, every tool set, and
// the config-reload log notification.
func New(version string) *Server {
	s := &Server{Version: version}
	s.Server = mcp.NewServer(
		&mcp.Implementation{Name: "copilot", Version: version},
		&mcp.ServerOptions{
			Instructions:      buildInstructions(),
			Capabilities:      loggingCapabilities(),
			CompletionHandler: s.complete,
			Logger:            slog.Default(),
		},
	)
	s.registerResources()
	s.registerPrompts()
	for _, register := range toolSets {
		register(s)
	}
	// Config is live-reloaded (see config.Load) — tell the client via a logging notification
	// whenever an edit on disk is picked up.
	_ = config.OnReload(func(info config.ReloadInfo) {
		s.mcpLog("info", "Configuration reloaded from "+info.Source, map[string]any{"source": info.Source})
	})
	return s
}

// Run serves one session over t until the client disconnects or ctx is cancelled (the SDK's
// Server.Run, but through Connect above).
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	ss, err := s.Connect(ctx, t)
	if err != nil {
		return err
	}
	closed := make(chan error, 1)
	go func() { closed <- ss.Wait() }()
	select {
	case <-ctx.Done():
		_ = ss.Close()
		if err := <-closed; err != nil {
			slog.Debug("session ended after cancel", "err", err)
		}
		return ctx.Err()
	case err := <-closed:
		return err
	}
}

// LogLevel is the stderr diagnostics policy (mirrors the TS console redirection): progress chatter
// is silent unless verbose (LOG_LEVEL in debug|info|warn|trace, or COPILOT_MCP_DEBUG set);
// warnings and errors always reach stderr. stdout is the JSON-RPC channel and is never used.
func LogLevel(getenv func(string) string) slog.Level {
	verbose := getenv("COPILOT_MCP_DEBUG") != ""
	switch strings.ToLower(getenv("LOG_LEVEL")) {
	case "debug", "info", "warn", "trace":
		verbose = true
	}
	if verbose {
		return slog.LevelDebug
	}
	return slog.LevelWarn
}

// InstallLogging points slog at stderr with the LogLevel policy.
func InstallLogging() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: LogLevel(os.Getenv)})))
}

// buildInstructions is the initialize-time guidance for the connected agent. Instructions are
// static per session, so the config is probed once at startup: an unconfigured server announces
// the self-setup path (config-guide resource -> write file -> doctor) instead of letting every
// tool fail cryptically. Config loading itself stays lazy and is retried on each call, so setup
// completes without a restart.
func buildInstructions() string {
	const base = "EHR Copilot order operations + orchestrator (UiPath and Apex) execution analysis. " +
		"Every Copilot tool requires an explicit env (prod | pre_prod) and a profile from the config — never assume either. " +
		"Every job- or queue-level orchestrator tool also requires an explicit `orchestrator` (uipath | apex); only " +
		"find_order_queue_items and find_stuck_orders may omit it (omitted = every configured backend), and " +
		"get_job_artifacts is Apex-only. An order can run on either orchestrator and which one is decided outside this " +
		"server, so look where the item actually sits. Job ids are GUIDs on both — pass them with the right `orchestrator`.\n\n" +
		"PROD IS READ-ONLY BY DESIGN. This server cannot mutate prod: every write path — order minting/deletes, " +
		"settings sync, queue-item posts/deletes, job starts/stops — targets pre-prod only (the UiPath dev-clone folder, " +
		"the Apex 'Authorization Test' folder), enforced at the tool schema AND asserted again in the domain layer " +
		"(Apex also at its HTTP chokepoint). Posted queue items additionally pass a safety guard (IsApproved forced " +
		"false, callback URLs pinned to pre-prod). Reading prod is normal and safe; " +
		"never work around a pre-prod-only refusal — it is the design, not a bug.\n\n" +
		"FEEDBACK: when a tool fails for a reason that looks like a bug in this server (not bad input, config, or an " +
		"upstream HTTP error), the error payload includes a `reportIssue` block with a prefilled GitHub new-issue URL " +
		"(carries only the tool name, error message, and server version — no args or credentials). Surface that link " +
		"to the user or open the issue directly so the bug gets reported; expected/user-actionable failures carry no such block. " +
		"The user can also ask to file a bug or general feedback about this server at any time — no failure required: " +
		"use build_mcp_issue (or the send-mcp-feedback prompt) to compose the issue, then post it via the host's GitHub tooling or hand over the prefilled URL.\n\n" +
		"Stable facts (portals, orchestrator folders/labels, schemas, safety rules, config guide) are resources under copilot://."
	cfg := config.Status()
	if cfg.OK {
		return base
	}
	detail := cfg.Error
	if detail == "" {
		detail = "no config found"
	}
	return base + "\n\nNO CONFIG IS LOADED — every tool will fail until one is provided (" + detail + ").\n" +
		"To set this server up: read the copilot://reference/config-guide resource, ask the user for the credential/token values (never invent them), " +
		"write the JSON config file it describes, then call the doctor tool to verify connectivity. " +
		"The config is re-read on the next call — no restart needed."
}
