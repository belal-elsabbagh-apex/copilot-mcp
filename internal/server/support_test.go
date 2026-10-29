//nolint:staticcheck // SEP-2577 deprecates MCP logging; the test client still observes it.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMain(m *testing.M) {
	// The SDK logs every session connect at INFO through slog.Default; keep test output readable.
	slog.SetLogLoggerLevel(slog.LevelWarn)
	os.Exit(m.Run())
}

// useConfig points COPILOT_MCP_CONFIG at a file holding v (or at a missing file when v is nil)
// and resets the config cache, restoring it on cleanup.
func useConfig(t *testing.T, v any) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("COPILOT_MCP_CONFIG", path)
	config.ResetForTest()
	t.Cleanup(config.ResetForTest)
}

// validConfig is the smallest config the loader accepts, with feedback overrides.
func validConfig(feedback map[string]any) map[string]any {
	creds := func(host string) map[string]any {
		return map[string]any{"be": "https://" + host + ".example.com", "email": host + "@example.com", "password": "pw"}
	}
	cfg := map[string]any{
		"copilot": map[string]any{"sessionCache": false, "prod": creds("p"), "pre_prod": creds("pp")},
		"uipath": map[string]any{
			"orchestratorUrl": "https://cloud.uipath.com/org/tenant/orchestrator_",
			"bearer":          "test-bearer",
		},
	}
	if feedback != nil {
		cfg["feedback"] = feedback
	}
	return cfg
}

// newTestServer is New("test") with no config loaded.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	useConfig(t, nil)
	return New("test")
}

// events collects what the server pushed to the client.
type events struct {
	logs     chan *mcp.LoggingMessageParams
	progress chan *mcp.ProgressNotificationParams
}

// connect attaches an in-memory client to s and returns its session plus the pushed events.
func connect(t *testing.T, s *Server) (*mcp.ClientSession, *events) {
	t.Helper()
	ev := &events{
		logs:     make(chan *mcp.LoggingMessageParams, 64),
		progress: make(chan *mcp.ProgressNotificationParams, 64),
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ClientOptions{
		LoggingMessageHandler: func(_ context.Context, r *mcp.LoggingMessageRequest) { ev.logs <- r.Params },
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			ev.progress <- r.Params
		},
	})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(t.Context(), st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, ev
}

// recv waits for one event or fails.
func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a server notification")
		panic("unreachable")
	}
}

// noEvent fails if anything arrives within a short grace period.
func noEvent[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected notification: %+v", v)
	case <-time.After(100 * time.Millisecond):
	}
}

// resultText is the single text block of a tool result.
func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("result has %d content blocks, want exactly 1", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content block is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}
