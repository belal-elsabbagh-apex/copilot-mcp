//nolint:staticcheck // SEP-2577 deprecates MCP logging; these tests pin the surface the TS server kept.
package server

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLogsBelowInfoAreSuppressedUntilTheClientLowersTheLevel(t *testing.T) {
	s := newTestServer(t)
	cs, ev := connect(t, s)

	s.mcpLog("debug", "noisy", nil)
	noEvent(t, ev.logs)

	s.mcpLog("info", "hi", nil)
	s.mcpLog("error", "boom", map[string]any{"code": 1})
	if got := recv(t, ev.logs); got.Level != "info" {
		t.Fatalf("first log level = %q, want info (the default threshold)", got.Level)
	}
	got := recv(t, ev.logs)
	data, _ := got.Data.(map[string]any)
	if got.Level != "error" || got.Logger != "copilot" || data["message"] != "boom" || data["code"] != float64(1) {
		t.Fatalf("second log = %+v, want error from logger copilot packing message + data", got)
	}

	if err := cs.SetLoggingLevel(t.Context(), &mcp.SetLoggingLevelParams{Level: "debug"}); err != nil {
		t.Fatal(err)
	}
	s.mcpLog("debug", "now visible", nil)
	if got := recv(t, ev.logs); got.Level != "debug" {
		t.Fatalf("log level = %q, want debug after setLevel lowered the threshold", got.Level)
	}
}

func TestConfigReloadIsAnnouncedToTheClient(t *testing.T) {
	s := newTestServer(t)
	_, ev := connect(t, s)
	s.mcpLog("info", "Configuration reloaded from /x", map[string]any{"source": "/x"})
	data, _ := recv(t, ev.logs).Data.(map[string]any)
	if data["source"] != "/x" {
		t.Errorf("log data = %v, want the reload source", data)
	}
}

func TestServerAdvertisesLoggingCompletionsAndTheCopilotIdentity(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	init := cs.InitializeResult()
	if init.ServerInfo.Name != "copilot" || init.ServerInfo.Version != "test" {
		t.Errorf("serverInfo = %+v, want copilot/test", init.ServerInfo)
	}
	c := init.Capabilities
	if c.Logging == nil || c.Completions == nil || c.Prompts == nil || c.Resources == nil {
		t.Errorf("capabilities = %+v, want logging, completions, prompts and resources", c)
	}
}
