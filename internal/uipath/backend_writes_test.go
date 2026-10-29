package uipath

import (
	"net/http"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func TestBackendAddQueueItemReportsIDsAsStrings(t *testing.T) {
	f := writeFake(t, nil, answer(http.StatusOK, map[string]any{"Id": 42, "Status": "New", "QueueDefinitionId": 79926}))
	out, err := Backend.AddQueueItem(t.Context(), orchestrator.AddQueueItemArgs{
		Env: config.PreProd, Queue: "Scratch queue", Reference: "REF-1", Priority: orchestrator.PriorityHigh,
		SpecificContent: map[string]any{"orderUid": "u1", "IsApproved": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	item := bodyOf(t, f.calls()[0])["itemData"].(map[string]any)
	if item["Name"] != "Scratch queue" || item["Priority"] != "High" {
		t.Errorf("itemData = %v", item)
	}
	want := orchestrator.AddQueueItemResult{
		Env: config.PreProd, QueueID: "79926", QueueName: "Scratch queue", Reference: "REF-1", ItemID: "42", Status: "New", Forced: []string{"IsApproved"},
	}
	if jsonOf(t, out) != jsonOf(t, want) {
		t.Fatalf("out = %s", jsonOf(t, out))
	}
}

func TestBackendAddQueueItemUnreportedQueueIDIsEmptyNotZero(t *testing.T) {
	writeFake(t, nil, answer(http.StatusOK, map[string]any{"Id": 1, "Status": "New"}))
	out, err := Backend.AddQueueItem(t.Context(), orchestrator.AddQueueItemArgs{
		Env: config.PreProd, Queue: "Scratch queue", Reference: "REF-1", Priority: orchestrator.PriorityNormal, SpecificContent: map[string]any{"orderUid": "u1"},
	})
	if err != nil || out.QueueID != "" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}

func TestBackendDeleteRejectsNonPositiveIntegerIDsBeforeAnyRequest(t *testing.T) {
	f := writeFake(t, nil, okEmpty)
	for _, id := range []string{"", "abc", "0", "-3", "1.5", "12abc", "99999999999999999999", "9007199254740992"} {
		if _, err := Backend.DeleteQueueItem(t.Context(), orchestrator.DeleteQueueItemArgs{Env: config.PreProd, ItemID: id}); !shared.IsExpected(err) {
			t.Errorf("id %q: err = %v", id, err)
		}
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("calls = %d", n)
	}
}

func TestBackendDeleteConvertsTheStringIDToTheNumericODataKey(t *testing.T) {
	f := writeFake(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		respond(w, http.StatusOK, map[string]any{"Id": 9, "Status": "New", "Reference": "r9"})
	})
	out, err := Backend.DeleteQueueItem(t.Context(), orchestrator.DeleteQueueItemArgs{Env: config.PreProd, ItemID: "9", QueueID: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.calls()[1].Path; got != "/myorg/mytenant/orchestrator_/odata/QueueItems(9)" {
		t.Errorf("path = %s", got)
	}
	want := orchestrator.DeleteQueueItemResult{Env: config.PreProd, ItemID: "9", Deleted: true, PreviousStatus: "New", Reference: "r9"}
	if jsonOf(t, out) != jsonOf(t, want) {
		t.Errorf("out = %s", jsonOf(t, out))
	}
}

func TestBackendStartJobMapsProcessIDAndInputAndLinksEachJob(t *testing.T) {
	f := writeFake(t, nil, answer(http.StatusOK, page(map[string]any{"Id": 5, "Key": "job-key-1", "State": "Pending", "ReleaseName": "Proc.Dev"})))
	out, err := Backend.StartJob(t.Context(), orchestrator.StartJobArgs{
		Env: config.PreProd, ProcessID: "609b3602-b6f2-44b2-9ee2-6a8988fac1f5", Input: map[string]any{"a": 1}, JobsCount: 2, MatchValue: "ignored-on-uipath",
	})
	if err != nil {
		t.Fatal(err)
	}
	info := bodyOf(t, f.calls()[0])["startInfo"].(map[string]any)
	if info["ReleaseKey"] != "609b3602-b6f2-44b2-9ee2-6a8988fac1f5" || info["JobsCount"] != 2.0 || info["InputArguments"] != `{"a":1}` {
		t.Errorf("startInfo = %v", info)
	}
	wantLink := testOrchestratorURL + "/jobs(sidepanel:sidepanel/jobs/job-key-1/details)"
	if len(out.Jobs) != 1 || out.Jobs[0] != (orchestrator.StartedJob{ID: "job-key-1", State: "Pending", ProcessName: "Proc.Dev", Link: wantLink}) {
		t.Fatalf("jobs = %+v", out.Jobs)
	}
}

func TestBackendStopJobReturnsTheKeyAsJobID(t *testing.T) {
	f := stopFake(t, "Running")
	out, err := Backend.StopJob(t.Context(), orchestrator.StopJobArgs{Env: config.PreProd, JobID: "job-key-1", Strategy: orchestrator.StopKill})
	if err != nil {
		t.Fatal(err)
	}
	calls := f.calls()
	if !strings.HasSuffix(calls[1].Path, "/odata/Jobs(5)/UiPath.Server.Configuration.OData.StopJob") || calls[1].Body != `{"strategy":"Kill"}` {
		t.Errorf("stop call = %+v", calls[1])
	}
	if out != (orchestrator.StopJobResult{Env: config.PreProd, JobID: "job-key-1", Strategy: orchestrator.StopKill, Stopped: true}) {
		t.Errorf("out = %+v", out)
	}
}

func TestBackendWritesRefuseProdBeforeAnyRequest(t *testing.T) {
	f := writeFake(t, nil, okEmpty)
	ctx := t.Context()
	_, e1 := Backend.AddQueueItem(ctx, orchestrator.AddQueueItemArgs{Env: config.Prod, Queue: "q", Reference: "r", Priority: orchestrator.PriorityNormal, SpecificContent: map[string]any{}})
	_, e2 := Backend.DeleteQueueItem(ctx, orchestrator.DeleteQueueItemArgs{Env: config.Prod, ItemID: "1"})
	_, e3 := Backend.StartJob(ctx, orchestrator.StartJobArgs{Env: config.Prod, ProcessID: "rk", Input: map[string]any{}, JobsCount: 1})
	_, e4 := Backend.StopJob(ctx, orchestrator.StopJobArgs{Env: config.Prod, JobID: "job-key-1", Strategy: orchestrator.StopSoft})
	for i, err := range []error{e1, e2, e3, e4} {
		if err == nil || !strings.Contains(err.Error(), "pre_prod-only") {
			t.Errorf("write %d: err = %v", i, err)
		}
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("calls = %d", n)
	}
}
