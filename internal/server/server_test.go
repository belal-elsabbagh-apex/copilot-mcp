package server

import (
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/uipath"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUnconfiguredServerAnnouncesTheSelfSetupPath(t *testing.T) {
	useConfig(t, nil)
	got := buildInstructions()
	for _, want := range []string{"NO CONFIG IS LOADED", "copilot://reference/config-guide", "call the doctor tool"} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions = %q, want containing %q", got, want)
		}
	}
}

func TestConfiguredServerInstructionsOmitTheSetupBanner(t *testing.T) {
	useConfig(t, validConfig(nil))
	got := buildInstructions()
	if strings.Contains(got, "NO CONFIG") || !strings.Contains(got, "PROD IS READ-ONLY BY DESIGN") {
		t.Errorf("instructions = %q, want the base guidance without the setup banner", got)
	}
}

func TestLogLevelIsQuietUnlessVerboseWasRequested(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	cases := []struct {
		name string
		env  map[string]string
		want slog.Level
	}{
		{"default", nil, slog.LevelWarn},
		{"LOG_LEVEL=error stays quiet", map[string]string{"LOG_LEVEL": "error"}, slog.LevelWarn},
		{"LOG_LEVEL=DEBUG", map[string]string{"LOG_LEVEL": "DEBUG"}, slog.LevelDebug},
		{"LOG_LEVEL=trace", map[string]string{"LOG_LEVEL": "trace"}, slog.LevelDebug},
		{"COPILOT_MCP_DEBUG", map[string]string{"COPILOT_MCP_DEBUG": "1"}, slog.LevelDebug},
	}
	for _, c := range cases {
		if got := LogLevel(env(c.env)); got != c.want {
			t.Errorf("%s: LogLevel() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestResourcesListTheStaticReferenceDocsAndEveryPortal(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	res, err := cs.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	uris := map[string]bool{}
	for _, r := range res.Resources {
		uris[r.URI] = true
		if r.MIMEType != jsonMIME {
			t.Errorf("%s mimeType = %q, want application/json", r.URI, r.MIMEType)
		}
	}
	for _, r := range staticResources {
		if !uris[r.uri] {
			t.Errorf("resources/list lacks %s", r.uri)
		}
	}
	if !uris[portalURIBase+"PMG"] || !uris[portalURIBase+"OPTUM%20CARE%20NETWORK"] {
		t.Errorf("resources/list lacks the per-portal entries (PMG / OPTUM CARE NETWORK)")
	}
	tmpl, err := cs.ListResourceTemplates(t.Context(), nil)
	if err != nil || len(tmpl.ResourceTemplates) != 1 || tmpl.ResourceTemplates[0].URITemplate != portalTemplate {
		t.Errorf("resources/templates/list = %+v, %v, want the single portal template", tmpl, err)
	}
}

func readText(t *testing.T, cs *mcp.ClientSession, uri string) string {
	t.Helper()
	res, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("ReadResource(%s): %v", uri, err)
	}
	return res.Contents[0].Text
}

func TestEveryStaticResourceReadsAsIndentedJSON(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	for _, r := range staticResources {
		text := readText(t, cs, r.uri)
		var v any
		if err := json.Unmarshal([]byte(text), &v); err != nil || !strings.Contains(text, "\n  ") {
			t.Errorf("%s: body is not 2-space-indented JSON (%v)", r.uri, err)
		}
	}
	if guide := readText(t, cs, "copilot://reference/config-guide"); !strings.Contains(guide, "<ASK-USER>") {
		t.Error("config-guide lost its literal <ASK-USER> placeholders (HTML escaping must stay off)")
	}
}

func TestPortalTemplateResolvesByKeyAliasAndCaseAndReportsUnknown(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	for _, uri := range []string{"PMG", "pmg", "%20pmg", "cci", "OPTUM%20care%20network"} {
		got := readText(t, cs, portalURIBase+uri)
		if strings.Contains(got, "unknown portal") {
			t.Errorf("portal/%s = %q, want a resolved portal", uri, got)
		}
	}
	if got := readText(t, cs, portalURIBase+"nope"); got != "{\n  \"error\": \"unknown portal: nope\"\n}" {
		t.Errorf("portal/nope = %q, want the unknown-portal error body", got)
	}
}

func TestOrchestratorReferenceKeepsTSKeyOrderAndLabelOrder(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	text := readText(t, cs, "copilot://reference/orchestrators")
	order := []string{`"uipath"`, `"apex"`, `"stateLabels"`, `"terminal"`, `"notes"`}
	last := -1
	for _, key := range order {
		i := strings.Index(text, "\n  "+key)
		if i <= last {
			t.Errorf("top-level key %s at %d, want after %d (TS key order)", key, i, last)
		}
		last = i
	}
	if !strings.Contains(text, "\"queued\": \"Pending\",\n      \"claimed\": \"Claimed\"") {
		t.Error("apexJobStatusToState lost the dashboard order (queued, claimed, ...)")
	}
}

func TestUipathTerminalStatesListMatchesTheBackendsSet(t *testing.T) {
	got := slices.Sorted(slices.Values(uipathTerminalStates))
	want := slices.Sorted(maps.Keys(uipath.TerminalStates))
	if !slices.Equal(got, want) {
		t.Errorf("uipathTerminalStates = %v, want the same set as uipath.TerminalStates %v", got, want)
	}
}

func TestBuildMcpIssueWorksWithNoConfigAndRejectsShortInput(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	res := callTool(t, cs, "build_mcp_issue", map[string]any{
		"kind": "feedback", "title": "clone_order output is hard to scan",
		"details": "The per-order rows mix successes and failures; a split summary would read better.",
	})
	var p IssuePayload
	if err := json.Unmarshal([]byte(resultText(t, res)), &p); err != nil || res.IsError {
		t.Fatalf("build_mcp_issue = %q (isError=%v), want the issue payload", resultText(t, res), res.IsError)
	}
	if p.Repo != RepoURL || p.Title != "[feedback] clone_order output is hard to scan" || strings.Join(p.Labels, ",") != "enhancement" {
		t.Errorf("payload = %+v, want default repo, [feedback] title, enhancement label", p)
	}
	for name, args := range map[string]map[string]any{
		"unknown kind":  {"kind": "complaint", "title": "x", "details": "too short"},
		"short details": {"kind": "bug", "title": "long enough title", "details": "too short"},
	} {
		if !callTool(t, cs, "build_mcp_issue", args).IsError {
			t.Errorf("%s: accepted, want the schema to reject it", name)
		}
	}
}
