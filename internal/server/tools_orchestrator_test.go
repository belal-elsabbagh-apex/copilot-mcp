package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callError runs a tool expected to fail before touching any orchestrator and returns its error
// text.
func callError(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	cs, _ := connect(t, newTestServer(t))
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || len(res.Content) != 1 {
		t.Fatalf("%s(%v): want one error block, got %+v", name, args, res)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s: content = %T, want text", name, res.Content[0])
	}
	return text.Text
}

func TestJobToolsRequireExactlyOneOfJobIDAndJobIDs(t *testing.T) {
	for _, tool := range []string{"get_job", "get_job_logs"} {
		base := m{"env": "prod", "orchestrator": "uipath"}
		if got := callError(t, tool, base); !strings.Contains(got, "provide either jobId or jobIds") {
			t.Errorf("%s with neither: %s", tool, got)
		}
		both := m{"env": "prod", "orchestrator": "uipath", "jobId": jobID, "jobIds": []string{jobID}}
		if got := callError(t, tool, both); !strings.Contains(got, "provide only one of jobId or jobIds") {
			t.Errorf("%s with both: %s", tool, got)
		}
	}
}

func TestFindOrderQueueItemsRefusesFolderWithoutOrchestrator(t *testing.T) {
	got := callError(t, "find_order_queue_items", m{"orderUid": "abcdefgh", "env": "prod", "folder": "Authorization"})
	if !strings.Contains(got, "pass `orchestrator` together with it") {
		t.Errorf("error = %s", got)
	}
}

func TestPullQueueItemRejectsAMalformedURLInTheHandler(t *testing.T) {
	got := callError(t, "pull_queue_item", m{"orchestrator": "uipath", "url": "not a url"})
	if !strings.Contains(got, "Invalid url") {
		t.Errorf("error = %s", got)
	}
}

func TestDownloadRequestDecodesTheAllOrFilenameListUnion(t *testing.T) {
	names, all, err := downloadRequest("all")
	if err != nil || !all || names != nil {
		t.Errorf(`"all" = %v, %v, %v`, names, all, err)
	}
	var list any
	if err := json.Unmarshal([]byte(`["trace.zip","job.log"]`), &list); err != nil {
		t.Fatal(err)
	}
	names, all, err = downloadRequest(list)
	if err != nil || all || len(names) != 2 || names[0] != "trace.zip" {
		t.Errorf("list = %v, %v, %v", names, all, err)
	}
	if _, _, err := downloadRequest([]any{1}); err == nil {
		t.Error("a non-string filename must be refused")
	}
}
