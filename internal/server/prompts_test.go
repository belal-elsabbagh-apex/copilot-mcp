package server

import (
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegistersExactlyTheWorkflowPrompts(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	res, err := cs.ListPrompts(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range res.Prompts {
		got = append(got, p.Name)
	}
	slices.Sort(got)
	want := []string{
		"clone-and-verify-order", "diagnose-order", "inspect-settings", "reconcile-settings",
		"report-faulted-jobs", "send-mcp-feedback", "triage-stuck-orders",
	}
	if !slices.Equal(got, want) {
		t.Errorf("prompts = %v, want %v", got, want)
	}
}

func getPrompt(t *testing.T, cs *mcp.ClientSession, name string, args map[string]string) (string, error) {
	t.Helper()
	res, err := cs.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		return "", err
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("%s: messages = %+v, want one user message", name, res.Messages)
	}
	return res.Messages[0].Content.(*mcp.TextContent).Text, nil
}

func TestDiagnoseOrderAsksForAnEnvOnlyWhenNoneWasGiven(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	without, err := getPrompt(t, cs, "diagnose-order", map[string]string{"orderUid": "abcdefgh12"})
	if err != nil {
		t.Fatal(err)
	}
	with, _ := getPrompt(t, cs, "diagnose-order", map[string]string{"orderUid": "abcdefgh12", "env": "prod"})
	if !strings.Contains(without, "confirm with the user which env") || strings.Contains(with, "confirm with the user which env") {
		t.Error("env reminder must appear exactly when env is omitted")
	}
	if !strings.Contains(with, `get_order with orderUid="abcdefgh12", env="prod"`) || !strings.Contains(with, "(env: prod)") {
		t.Errorf("prompt = %q, want the env threaded into the tool calls", with)
	}
}

func TestReportFaultedJobsDefaultsToProdAndTheRPARepo(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	got, _ := getPrompt(t, cs, "report-faulted-jobs", map[string]string{"orchestrator": "apex"})
	for _, want := range []string{"in prod on Apex-Medical-AI-Inc/RPAPlaywright", `orchestrator="apex", env="prod"`} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt = %q, want containing %q", got, want)
		}
	}
	got, _ = getPrompt(t, cs, "report-faulted-jobs", map[string]string{"orchestrator": "uipath", "since": "2026-01-01", "top": "5", "repo": "a/b"})
	if !strings.Contains(got, `since="2026-01-01", top=5`) || !strings.Contains(got, `jobId, repo="a/b"`) {
		t.Errorf("prompt = %q, want since/top/repo threaded in", got)
	}
}

func TestSendFeedbackPromptTreatsAnythingButBugAsFeedback(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	got, _ := getPrompt(t, cs, "send-mcp-feedback", map[string]string{"summary": "x", "kind": "idea"})
	if !strings.Contains(got, `kind="feedback"`) || !strings.HasPrefix(got, "File feedback about") {
		t.Errorf("prompt = %q, want feedback framing for an unknown kind", got)
	}
}

func TestPromptRejectsMissingAndTooShortArgumentsAsInvalidParams(t *testing.T) {
	cs, _ := connect(t, newTestServer(t))
	for name, args := range map[string]map[string]string{"short": {"orderUid": "x"}, "missing": {}} {
		_, err := getPrompt(t, cs, "diagnose-order", args)
		if err == nil || !strings.Contains(err.Error(), "Invalid arguments for prompt diagnose-order") {
			t.Errorf("%s: err = %v, want an invalid-arguments error", name, err)
		}
	}
}

func complete(t *testing.T, cs *mcp.ClientSession, ref *mcp.CompleteReference, arg, value string) []string {
	t.Helper()
	res, err := cs.Complete(t.Context(), &mcp.CompleteParams{Ref: ref, Argument: mcp.CompleteParamsArgument{Name: arg, Value: value}})
	if err != nil {
		t.Fatal(err)
	}
	return res.Completion.Values
}

func TestCompletionSuggestsEnvsOrchestratorsProfilesAndPortals(t *testing.T) {
	useConfig(t, validConfig(nil))
	cs, _ := connect(t, New("test"))
	prompt := func(name string) *mcp.CompleteReference {
		return &mcp.CompleteReference{Type: "ref/prompt", Name: name}
	}

	if got := complete(t, cs, prompt("diagnose-order"), "env", "p"); !slices.Equal(got, []string{"prod", "pre_prod"}) {
		t.Errorf("env completion = %v, want the full list (TS completable does not prefix-filter)", got)
	}
	if got := complete(t, cs, prompt("report-faulted-jobs"), "orchestrator", ""); !slices.Equal(got, []string{"uipath", "apex"}) {
		t.Errorf("orchestrator completion = %v, want uipath, apex", got)
	}
	if got := complete(t, cs, prompt("triage-stuck-orders"), "orderUid", ""); len(got) != 0 {
		t.Errorf("completion for an argument without suggestions = %v, want none", got)
	}
	portal := &mcp.CompleteReference{Type: "ref/resource", URI: portalTemplate}
	if got := complete(t, cs, portal, "name", "p"); !slices.Equal(got, []string{"PMG", "PCA"}) {
		t.Errorf("portal completion for %q = %v, want PMG, PCA", "p", got)
	}
}
