package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoArgs struct {
	Text string `json:"text" jsonschema:"what to echo"`
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func TestSuccessRendersOneTextBlockOfIndentedJSONWithoutHTMLEscaping(t *testing.T) {
	s := newTestServer(t)
	addTool(s, &mcp.Tool{Name: "echo", Description: "test tool"}, func(_ context.Context, _ *mcp.CallToolRequest, in echoArgs) (any, error) {
		return map[string]any{"text": in.Text, "list": []int{1}}, nil
	})
	cs, _ := connect(t, s)

	res := callTool(t, cs, "echo", map[string]any{"text": "<ASK-USER> & more"})
	want := "{\n  \"list\": [\n    1\n  ],\n  \"text\": \"<ASK-USER> & more\"\n}"
	if res.IsError || resultText(t, res) != want {
		t.Errorf("result = %q (isError=%v), want %q", resultText(t, res), res.IsError, want)
	}
	if res.StructuredContent != nil {
		t.Errorf("structuredContent = %v, want none (the TS server never emitted it)", res.StructuredContent)
	}
}

func TestFailureRendersTheTSToolErrorBodyWithReportIssueForUnknownErrors(t *testing.T) {
	s := newTestServer(t)
	addTool(s, &mcp.Tool{Name: "bad", Description: "test tool"}, func(context.Context, *mcp.CallToolRequest, noArgs) (any, error) {
		return nil, errors.New("something exploded")
	})
	addTool(s, &mcp.Tool{Name: "expected", Description: "test tool"}, func(context.Context, *mcp.CallToolRequest, noArgs) (any, error) {
		return nil, errors.New("unknown profile 'x'")
	})
	cs, _ := connect(t, s)

	var bug struct {
		Error       string `json:"error"`
		ReportIssue *struct{ Message, URL string }
	}
	res := callTool(t, cs, "bad", map[string]any{})
	if err := json.Unmarshal([]byte(resultText(t, res)), &bug); err != nil {
		t.Fatal(err)
	}
	if !res.IsError || bug.Error != "something exploded" || bug.ReportIssue == nil || !strings.Contains(bug.ReportIssue.URL, "bad") {
		t.Errorf("unknown failure = isError %v, %+v, want isError with error and reportIssue naming the tool", res.IsError, bug)
	}

	res = callTool(t, cs, "expected", map[string]any{})
	if !res.IsError || strings.Contains(resultText(t, res), "reportIssue") {
		t.Errorf("expected failure = %q, want isError and no reportIssue", resultText(t, res))
	}
}

func TestAPanickingToolBecomesAnErrorResultInsteadOfKillingTheServer(t *testing.T) {
	s := newTestServer(t)
	addTool(s, &mcp.Tool{Name: "boom", Description: "test tool"}, func(context.Context, *mcp.CallToolRequest, noArgs) (any, error) {
		panic("kaboom")
	})
	cs, _ := connect(t, s)
	res := callTool(t, cs, "boom", map[string]any{})
	if !res.IsError || !strings.Contains(resultText(t, res), "panic: kaboom") {
		t.Errorf("result = %q (isError=%v), want an error result naming the panic", resultText(t, res), res.IsError)
	}
}

func TestInvalidInputIsAnErrorResultNotAProtocolError(t *testing.T) {
	s := newTestServer(t)
	called := false
	addTool(s, &mcp.Tool{Name: "echo", Description: "test tool"}, func(context.Context, *mcp.CallToolRequest, echoArgs) (any, error) {
		called = true
		return nil, nil
	})
	cs, _ := connect(t, s)
	for name, args := range map[string]any{"missing required": map[string]any{}, "wrong type": map[string]any{"text": 5}, "unknown key": map[string]any{"text": "x", "extra": 1}} {
		res := callTool(t, cs, "echo", args)
		if !res.IsError {
			t.Errorf("%s: isError = false, want the SDK's validation failure as an error result", name)
		}
	}
	if called {
		t.Error("tool body ran for invalid input, want the schema to reject it first")
	}
}
