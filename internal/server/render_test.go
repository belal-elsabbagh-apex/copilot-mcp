package server

import (
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Output produced by a MarshalJSON method is copied into the result as-is, so those methods must
// not HTML-escape either: JSON.stringify never turns < > & into \u003c etc.
func TestMarshalJSONMethodsRenderWithoutHTMLEscaping(t *testing.T) {
	type entry struct {
		Message string `json:"message"`
	}
	cases := map[string]any{
		"passed-through BE value":    struct{ Name shared.Field }{shared.Pass(map[string]any{"n": "Smith & Jones <x>"}, "n")},
		"single-id job log envelope": orchestrator.JobEntriesEnvelope(orchestrator.UiPath, config.Prod, "f", false, []entry{{"a <&> b"}}),
	}
	for name, v := range cases {
		got, err := renderJSON(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, `\u00`) || !strings.ContainsAny(got, "<&>") {
			t.Errorf("%s: escaped or lost characters: %s", name, got)
		}
	}
}
