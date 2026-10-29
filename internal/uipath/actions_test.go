package uipath

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

func TestBuildAddQueueItemBodyWrapsTheGuardedContent(t *testing.T) {
	sc := map[string]any{"orderUid": "u1", "IsApproved": false}
	got := jsonOf(t, buildAddQueueItemBody("My queue", "REF-1", orchestrator.PriorityNormal, sc))
	want := `{"itemData":{"Name":"My queue","Priority":"Normal","Reference":"REF-1","SpecificContent":{"IsApproved":false,"orderUid":"u1"}}}`
	if got != want {
		t.Fatalf("body = %s", got)
	}
}

func TestBuildStartJobBody(t *testing.T) {
	base := startArgs{env: config.PreProd, releaseKey: "rk-guid", jobsCount: 1}
	body, _ := buildStartJobBody(base)
	if got := jsonOf(t, body); got != `{"startInfo":{"JobsCount":1,"ReleaseKey":"rk-guid","Strategy":"ModernJobsCount"}}` {
		t.Errorf("empty input must omit InputArguments: %s", got)
	}
	base.jobsCount, base.inputArguments = 2, map[string]any{"a": 1}
	body, _ = buildStartJobBody(base)
	if got := jsonOf(t, body); !strings.Contains(got, `"InputArguments":"{\"a\":1}"`) || !strings.Contains(got, `"JobsCount":2`) {
		t.Errorf("input must be JSON-stringified: %s", got)
	}
}

func TestAssertDeletableOnlyPassesNew(t *testing.T) {
	if err := assertDeletable(QueueItem{ID: 7, Status: "New"}); err != nil {
		t.Errorf("New must pass: %v", err)
	}
	for _, s := range []string{"InProgress", "Successful", "Failed", "Deleted", "Abandoned"} {
		if err := assertDeletable(QueueItem{ID: 7, Status: s}); err == nil || !strings.Contains(err.Error(), "only 'New' items may be deleted") {
			t.Errorf("%s must be refused: %v", s, err)
		}
	}
}

func TestAssertStoppableRefusesTerminalStates(t *testing.T) {
	for _, s := range []string{"Pending", "Running", "Resumed", "Suspended", "Stopping"} {
		if err := assertStoppable(Job{ID: "5", Key: "job-key-1", State: s}); err != nil {
			t.Errorf("%s must pass: %v", s, err)
		}
	}
	for _, s := range []string{"Successful", "Faulted", "Stopped"} {
		if err := assertStoppable(Job{ID: "5", Key: "job-key-1", State: s}); err == nil || !strings.Contains(err.Error(), "state is already") {
			t.Errorf("%s must be refused: %v", s, err)
		}
	}
}

func writeFake(t *testing.T, over map[string]any, h http.HandlerFunc) *fake {
	t.Helper()
	f := newFake(t, h)
	cfg := map[string]any{"serverUrlByEnv": map[string]any{"pre_prod": "https://pre-prod-be.example.com"}}
	for k, v := range over {
		cfg[k] = v
	}
	f.useConfig(t, cfg)
	return f
}

func answer(status int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { respond(w, status, v) }
}

func bodyOf(t *testing.T, c call) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(c.Body), &m); err != nil {
		t.Fatalf("body %q: %v", c.Body, err)
	}
	return m
}

var addArgs = orchestrator.AddQueueItemArgs{
	Env: config.PreProd, Queue: "Scratch queue", Reference: "REF-1", Priority: orchestrator.PriorityNormal,
	SpecificContent: map[string]any{"orderUid": "u1", "IsApproved": true},
}

func TestEveryWriteRefusesProdBeforeAnyRequest(t *testing.T) {
	f := writeFake(t, nil, okEmpty)
	ctx := t.Context()
	prodAdd := addArgs
	prodAdd.Env = config.Prod
	_, errs := addQueueItem(ctx, prodAdd)
	_, errDel := deleteQueueItem(ctx, 1, config.Prod)
	_, errStart := startJob(ctx, startArgs{env: config.Prod, releaseKey: "rk", jobsCount: 1})
	_, errStop := stopJob(ctx, config.Prod, "job-key-1", orchestrator.StopSoft)
	for name, err := range map[string]error{"add": errs, "delete": errDel, "start": errStart, "stop": errStop} {
		if err == nil || !strings.Contains(err.Error(), "pre_prod-only") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("calls = %d, want none", n)
	}
}

func TestAddQueueItemGuardViolationsBlockThePost(t *testing.T) {
	f := writeFake(t, nil, okEmpty)
	args := addArgs
	args.SpecificContent = map[string]any{"orderUid": "<TO-FILL>"}
	if _, err := addQueueItem(t.Context(), args); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("calls = %d", n)
	}
}

func TestAddQueueItemPostsTheGuardedPayloadWithAuthAndFolderHeaders(t *testing.T) {
	f := writeFake(t, nil, answer(http.StatusOK, map[string]any{"Id": 42, "Status": "New", "QueueDefinitionId": 79926}))
	out, err := addQueueItem(t.Context(), addArgs)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	c := calls[0]
	if c.Method != "POST" || c.Path != "/myorg/mytenant/orchestrator_"+addQueueItemPath {
		t.Errorf("request = %s %s", c.Method, c.Path)
	}
	for h, want := range map[string]string{
		"Authorization": "Bearer test-bearer", "Content-Type": "application/json",
		"X-Uipath-Organizationunitid": "434039", "X-Uipath-Folderpath": "Authorization Dev Clone",
	} {
		if got := c.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	item := bodyOf(t, c)["itemData"].(map[string]any)
	if item["Name"] != "Scratch queue" || item["SpecificContent"].(map[string]any)["IsApproved"] != false {
		t.Errorf("itemData = %v", item)
	}
	if out.itemID != 42 || out.queueDefinitionID != 79926 || out.status != "New" || len(out.forced) != 1 || out.forced[0] != "IsApproved" {
		t.Errorf("result = %+v", out)
	}
}

func TestAddQueueItemHonoursAConfiguredPath(t *testing.T) {
	f := writeFake(t, map[string]any{"addQueueItemPath": "/odata/custom"}, answer(http.StatusOK, map[string]any{"Id": 1, "Status": "New"}))
	if _, err := addQueueItem(t.Context(), addArgs); err != nil {
		t.Fatal(err)
	}
	if got := f.calls()[0].Path; got != "/myorg/mytenant/orchestrator_/odata/custom" {
		t.Fatalf("path = %s", got)
	}
}

func TestAddQueueItemSurfacesANon2xxAsAnAPIError(t *testing.T) {
	writeFake(t, nil, answer(http.StatusNotFound, map[string]any{"message": "queue not found"}))
	_, err := addQueueItem(t.Context(), addArgs)
	if !IsNotFound(err) || !strings.Contains(err.Error(), "UiPath POST") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteQueueItemIsFetchFirstAndRefusesNonNewItems(t *testing.T) {
	f := writeFake(t, nil, answer(http.StatusOK, map[string]any{"Id": 9, "Status": "InProgress", "Reference": "r9"}))
	_, err := deleteQueueItem(t.Context(), 9, config.PreProd)
	if err == nil || !strings.Contains(err.Error(), "status is 'InProgress'") {
		t.Fatalf("err = %v", err)
	}
	if calls := f.calls(); len(calls) != 1 || calls[0].Method != "GET" {
		t.Fatalf("calls = %+v, want only the fetch", calls)
	}
}

func TestDeleteQueueItemDeletesANewItemAndToleratesAn204(t *testing.T) {
	f := writeFake(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		respond(w, http.StatusOK, map[string]any{"Id": 9, "Status": "New", "Reference": "r9"})
	})
	out, err := deleteQueueItem(t.Context(), 9, config.PreProd)
	if err != nil || out.reference != "r9" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	calls := f.calls()
	if len(calls) != 2 || calls[1].Method != "DELETE" || calls[1].Path != "/myorg/mytenant/orchestrator_/odata/QueueItems(9)" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestStartJobPostsStartJobsAndMapsTheStartedJobs(t *testing.T) {
	f := writeFake(t, nil, answer(http.StatusOK, page(map[string]any{"Id": 5, "Key": "job-key-1", "State": "Pending", "ReleaseName": "Proc.Dev"})))
	jobs, err := startJob(t.Context(), startArgs{env: config.PreProd, releaseKey: "609b3602-b6f2-44b2-9ee2-6a8988fac1f5", jobsCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	c := f.calls()[0]
	if c.Method != "POST" || c.Path != "/myorg/mytenant/orchestrator_"+startJobsPath {
		t.Errorf("request = %s %s", c.Method, c.Path)
	}
	if len(jobs) != 1 || jobs[0] != (startedJob{key: "job-key-1", state: "Pending", releaseName: "Proc.Dev"}) {
		t.Errorf("jobs = %+v", jobs)
	}
}

func stopFake(t *testing.T, state string) *fake {
	t.Helper()
	return writeFake(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if state == "" {
				respond(w, http.StatusOK, page())
				return
			}
			respond(w, http.StatusOK, page(map[string]any{"Id": 5, "Key": "job-key-1", "State": state}))
			return
		}
		respond(w, http.StatusOK, map[string]any{})
	})
}

func TestStopJobErrorsWhenNoJobResolvesForTheKey(t *testing.T) {
	f := stopFake(t, "")
	if _, err := stopJob(t.Context(), config.PreProd, "job-key-1", orchestrator.StopSoft); err == nil || !strings.Contains(err.Error(), "no dev-clone job found") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.calls()); n != 1 {
		t.Fatalf("calls = %d", n)
	}
}

func TestStopJobIsFetchFirstAndRefusesATerminalJob(t *testing.T) {
	f := stopFake(t, "Successful")
	if _, err := stopJob(t.Context(), config.PreProd, "job-key-1", orchestrator.StopSoft); err == nil || !strings.Contains(err.Error(), "state is already 'Successful'") {
		t.Fatalf("err = %v", err)
	}
	if calls := f.calls(); len(calls) != 1 || calls[0].Method != "GET" {
		t.Fatalf("calls = %+v, want only the fetch", calls)
	}
}

func TestStopJobPostsStopJobByNumericIdForAStoppableJob(t *testing.T) {
	f := stopFake(t, "Running")
	key, err := stopJob(t.Context(), config.PreProd, "job-key-1", orchestrator.StopSoft)
	if err != nil || key != "job-key-1" {
		t.Fatalf("key = %q, err = %v", key, err)
	}
	calls := f.calls()
	if len(calls) != 2 || calls[1].Method != "POST" ||
		calls[1].Path != "/myorg/mytenant/orchestrator_/odata/Jobs(5)/UiPath.Server.Configuration.OData.StopJob" || calls[1].Body != `{"strategy":"SoftStop"}` {
		t.Fatalf("calls = %+v", calls)
	}
	if got := calls[0].Header.Get("X-Uipath-Folderpath"); got != "Authorization Dev Clone" {
		t.Errorf("the key must resolve in the pre-prod folder, got %q", got)
	}
}
