package server

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolFunc is a tool body: it receives the decoded, schema-validated input and returns the value
// to render as the result (any JSON-marshalable value) or an error. A returned error becomes the
// TS toolError payload; a panic is recovered into one.
type ToolFunc[In any] func(ctx context.Context, req *mcp.CallToolRequest, in In) (any, error)

// addTool registers a tool whose input schema is inferred from In (see schema.go for the
// descriptions/enums/bounds patches) and whose result is rendered exactly like the TS `ok()`:
// ONE text content block of 2-space-indented JSON, no structuredContent. Failures render like
// the TS `toolError(tool, e, VERSION)`: isError plus {error, reportIssue?}.
func addTool[In any](s *Server, t *mcp.Tool, fn ToolFunc[In], patches ...Patch) {
	t.InputSchema = inputSchema[In](t.Name, patches)
	mcp.AddTool(s.Server, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		out, err := callGuarded(ctx, req, in, fn)
		if err != nil {
			return s.toolError(t.Name, err), nil, nil
		}
		text, err := renderJSON(out)
		if err != nil {
			return s.toolError(t.Name, fmt.Errorf("rendering result: %w", err)), nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	})
}

// callGuarded turns a panic in a tool body into an error: the TS handlers sit in try/catch, and an
// unrecovered panic would take the whole stdio server down.
func callGuarded[In any](ctx context.Context, req *mcp.CallToolRequest, in In, fn ToolFunc[In]) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("tool panicked", "tool", req.Params.Name, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn(ctx, req, in)
}

// toolError renders a failed tool call (TS toolError): isError and one text block holding
// {error, reportIssue?}.
func (s *Server) toolError(tool string, err error) *mcp.CallToolResult {
	text, rerr := renderJSON(errorPayload(tool, err, s.Version))
	if rerr != nil { // errorBody is plain strings; unreachable, but never lose the message
		text = err.Error()
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// readOnlyHints annotates a read-only tool: non-destructive and idempotent. openWorld is false
// only for tools that touch no external service (list_setting_sections, build_mcp_issue).
func readOnlyHints(openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: new(false), IdempotentHint: true, OpenWorldHint: &openWorld}
}

// writeHints annotates a mutating tool. Every write tool talks to an external system, so
// openWorld is always true.
//
//nolint:unused // the tool sets annotate their mutating tools with it
func writeHints(destructive, idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{DestructiveHint: &destructive, IdempotentHint: idempotent, OpenWorldHint: new(true)}
}

// toolSets are the files that register tools; each adds itself from an init() so no shared file
// lists them.
var toolSets []func(*Server)

func addToolSet(register func(*Server)) { toolSets = append(toolSets, register) }
