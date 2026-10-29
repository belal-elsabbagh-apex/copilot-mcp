package uipath

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

var keyRE = regexp.MustCompile(`(?:Job)?Key eq (\S+)`)

func jobRow(key string, over map[string]any) map[string]any {
	m := map[string]any{
		"Id": 1, "Key": key, "State": "Faulted", "ReleaseName": "PMG auth submit",
		"CreationTime": "2026-07-01T10:00:00Z", "EndTime": "2026-07-01T10:01:00Z",
	}
	for k, v := range over {
		m[k] = v
	}
	return m
}

func logJSON(level, msg string, extra map[string]any) map[string]any {
	m := map[string]any{"Level": level, "Message": msg, "TimeStamp": "2026-07-01T10:00:30Z"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// serve answers Jobs by key (missing = no rows) and RobotLogs by key (missing = no rows); the
// string "error" as a value means HTTP 500.
func serve(t *testing.T, jobs map[string]any, logs map[string]any) *fake {
	t.Helper()
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		m := keyRE.FindStringSubmatch(r.URL.Query().Get("$filter"))
		key := ""
		if m != nil {
			key = m[1]
		}
		var src map[string]any
		switch {
		case strings.HasSuffix(r.URL.Path, "/odata/Jobs"):
			src = jobs
		case strings.HasSuffix(r.URL.Path, "/odata/RobotLogs"):
			src = logs
		default:
			okEmpty(w, r)
			return
		}
		switch v := src[key].(type) {
		case string:
			respond(w, http.StatusInternalServerError, map[string]any{"message": "boom"})
		case nil:
			respond(w, http.StatusOK, page())
		case []any:
			respond(w, http.StatusOK, page(v...))
		default:
			respond(w, http.StatusOK, page(v))
		}
	})
	f.useConfig(t, nil)
	return f
}

func TestListJobsMapsRowsToCoresWithLinksAndTheResolvedFolder(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, page(jobRow(testKey, map[string]any{"Release": map[string]any{"ProcessVersion": "1.2.3"}})))
	})
	f.useConfig(t, nil)
	res, err := Backend.ListJobs(t.Context(), orchestrator.ListJobsArgs{Env: config.Prod, Top: 5})
	if err != nil || res.Folder != "Authorization" || len(res.Jobs) != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	j := res.Jobs[0]
	if j.ID != testKey || j.State != "Faulted" || !j.Terminal || *j.DurationMs != 60_000 || j.Process.Name != "PMG auth submit" ||
		*j.Process.Version != "1.2.3" || !strings.Contains(j.Link, testKey) {
		t.Fatalf("job = %+v", j)
	}
	custom, _ := Backend.ListJobs(t.Context(), orchestrator.ListJobsArgs{Env: config.Prod, Top: 5, Folder: "Some Folder"})
	if custom.Folder != "Some Folder" {
		t.Fatalf("folder = %q", custom.Folder)
	}
}

func TestGetJobsSingleIDLookupFailureFailsTheCall(t *testing.T) {
	serve(t, map[string]any{testKey: "error"}, nil)
	_, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, JobIDs: []string{testKey}}, nil)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetJobsBatchKeepsGoingPastAFailingID(t *testing.T) {
	serve(t, map[string]any{testKey: jobRow(testKey, nil), testKey2: "error"}, nil)
	res, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, Batch: true, JobIDs: []string{testKey, testKey2, testKey3}}, nil)
	if err != nil || len(res.Entries) != 3 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	e := res.Entries
	if e[0].ID != testKey || !e[0].Found || e[0].Job.ID != testKey {
		t.Errorf("entry 0 = %+v", e[0])
	}
	if e[1].Found || !strings.Contains(e[1].Error, "500") {
		t.Errorf("entry 1 = %+v", e[1])
	}
	if got := jsonOf(t, e[2]); got != `{"id":"`+testKey3+`","found":false}` {
		t.Errorf("entry 2 = %s", got)
	}
}

func TestGetJobsSingleIDResolvingToNothingIsNotFoundNotAnError(t *testing.T) {
	serve(t, nil, nil)
	res, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, JobIDs: []string{testKey}}, nil)
	if err != nil || jsonOf(t, res.Entries) != `[{"id":"`+testKey+`","found":false}]` {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestGetJobsWithoutLogDigestFetchesNoLogsAndLeavesFaultNull(t *testing.T) {
	f := serve(t, map[string]any{testKey: jobRow(testKey, nil)}, nil)
	res, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, JobIDs: []string{testKey}}, nil)
	if err != nil || res.Entries[0].Job.Fault != nil || res.Entries[0].LogDigest != nil {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if n := len(f.callsTo("/odata/RobotLogs")); n != 0 {
		t.Fatalf("log calls = %d", n)
	}
}

func TestGetJobsLogDigestGivesFaultAndTheTransactionOutcome(t *testing.T) {
	ended := `{"transactionState":"Ended","processingExceptionType":"BusinessException","processingExceptionReason":"missing member id"}`
	f := serve(t, map[string]any{testKey: jobRow(testKey, nil)}, map[string]any{testKey: []any{
		logJSON("Error", "System.InvalidOperationException: submit rejected", nil),
		logJSON("Info", "Transaction Ended", map[string]any{"RawMessage": ended}),
	}})
	res, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, JobIDs: []string{testKey}, IncludeLogDigest: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := res.Entries[0]
	if fault := jsonOf(t, e.Job.Fault); fault != `{"message":"System.InvalidOperationException: submit rejected","signature":"system.invalidoperationexception: submit rejected","class":"System.InvalidOperationException"}` {
		t.Errorf("fault = %s", fault)
	}
	d := e.LogDigest.(LogDigest)
	if d.Fetched != 2 || deref(d.TransactionOutcome.ProcessingExceptionType) != "BusinessException" || deref(d.TransactionOutcome.ProcessingExceptionReason) != "missing member id" {
		t.Errorf("digest = %+v", d)
	}
	if sel := f.callsTo("/odata/RobotLogs")[0].query("$select"); !strings.Contains(sel, "RawMessage") {
		t.Errorf("$select = %q", sel)
	}
}

func TestGetJobsSuccessfulJobGetsADigestButNoFault(t *testing.T) {
	serve(t, map[string]any{testKey: jobRow(testKey, map[string]any{"State": "Successful"})}, map[string]any{testKey: logJSON("Info", "done", nil)})
	res, _ := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, JobIDs: []string{testKey}, IncludeLogDigest: true}, nil)
	e := res.Entries[0]
	if e.Job.Fault != nil || e.LogDigest.(LogDigest).Fetched != 1 {
		t.Fatalf("entry = %+v", e)
	}
}

func TestGetJobsLogsFailureDegradesToLogDigestError(t *testing.T) {
	serve(t, map[string]any{testKey: jobRow(testKey, nil)}, map[string]any{testKey: "error"})
	res, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, JobIDs: []string{testKey}, IncludeLogDigest: true}, nil)
	if err != nil {
		t.Fatalf("a single id must still succeed: %v", err)
	}
	e := res.Entries[0]
	if !e.Found || e.Job.ID != testKey || e.Job.Fault != nil || !strings.Contains(e.LogDigestError, "500") || e.LogDigest != nil {
		t.Fatalf("entry = %+v", e)
	}
}

func TestGetJobsOutputIsParsedWithoutTheTokenAndNullWhenAbsent(t *testing.T) {
	out, _ := json.Marshal(map[string]any{"out_status": "ok", "token": "jwt.secret"})
	serve(t, map[string]any{testKey: jobRow(testKey, map[string]any{"OutputArguments": string(out)}), testKey2: jobRow(testKey2, nil)}, nil)
	res, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, Batch: true, JobIDs: []string{testKey, testKey2}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	all := jsonOf(t, res)
	if strings.Contains(all, "jwt.secret") || !strings.Contains(all, `"output":{"out_status":"ok"}`) || !strings.Contains(all, `"output":null`) {
		t.Fatalf("result = %s", all)
	}
}

func TestGetJobsProgressCountsEveryIDOff(t *testing.T) {
	serve(t, map[string]any{testKey: jobRow(testKey, nil), testKey2: jobRow(testKey2, nil), testKey3: jobRow(testKey3, nil)}, nil)
	var mu sync.Mutex
	var seen [][2]int
	_, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, Batch: true, JobIDs: []string{testKey, testKey2, testKey3}},
		shared.StepProgress(func(done, total int, _ string) {
			mu.Lock()
			seen = append(seen, [2]int{done, total})
			mu.Unlock()
		}))
	if err != nil || len(seen) != 3 || seen[0] != [2]int{1, 3} || seen[1] != [2]int{2, 3} || seen[2] != [2]int{3, 3} {
		t.Fatalf("seen = %v, err = %v", seen, err)
	}
}

func TestGetJobsPreservesInputOrderAcrossTheWorkerPool(t *testing.T) {
	jobs := map[string]any{}
	ids := make([]string, 30)
	for i := range ids {
		ids[i] = strings.Repeat(string(rune('a'+i%26)), 8) + "-" + string(rune('a'+i/26))
		jobs[ids[i]] = jobRow(ids[i], nil)
	}
	serve(t, jobs, nil)
	res, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, Batch: true, JobIDs: ids}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range res.Entries {
		if e.ID != ids[i] || !e.Found {
			t.Fatalf("entry %d = %+v, want %s", i, e, ids[i])
		}
	}
}

var logsBase = orchestrator.GetJobLogsArgs{Env: config.Prod}

func TestGetJobLogsSingleIDFailureFailsTheCallButABatchPutsItOnTheEntry(t *testing.T) {
	serve(t, nil, map[string]any{testKey: "error", testKey2: logJSON("Info", "ok", nil)})
	a := logsBase
	a.JobIDs = []string{testKey}
	if _, err := Backend.GetJobLogs(t.Context(), a, nil); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
	a.JobIDs, a.Batch = []string{testKey, testKey2}, true
	res, err := Backend.GetJobLogs(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Entries[0].Error, "500") || res.Entries[0].Logs == nil || len(res.Entries[0].Logs) != 0 {
		t.Errorf("entry 0 = %+v", res.Entries[0])
	}
	if e := res.Entries[1]; e.JobID != testKey2 || e.Returned != 1 || e.Truncated {
		t.Errorf("entry 1 = %+v", e)
	}
}

func TestGetJobLogsTruncatesMessagesAndOnlyParsesRawOnRequest(t *testing.T) {
	long := strings.Repeat("x", 500)
	raw := `{"transactionState":"Ended","queueName":"PMG"}`
	serve(t, nil, map[string]any{testKey: logJSON("Info", long, map[string]any{"RawMessage": raw})})
	a := logsBase
	a.JobIDs = []string{testKey}

	plain, err := Backend.GetJobLogs(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row := plain.Entries[0].Logs[0]; row.Message != strings.Repeat("x", 400)+"…" || row.Raw != nil {
		t.Errorf("plain row = %+v", row)
	}
	if strings.Contains(jsonOf(t, plain), "RawMessage") {
		t.Error("RawMessage must never leak")
	}

	a.FullMessages, a.IncludeRawFields = true, true
	rich, _ := Backend.GetJobLogs(t.Context(), a, nil)
	row := rich.Entries[0].Logs[0]
	if row.Message != long || !strings.Contains(string(row.Raw), `"transactionState":"Ended"`) || !strings.Contains(string(row.Raw), `"queueName":"PMG"`) {
		t.Errorf("rich row = %+v / %s", row, row.Raw)
	}
	if strings.Contains(jsonOf(t, rich), `"RawMessage"`) {
		t.Error("RawMessage must never leak")
	}
}

func TestGetJobLogsRawIsExplicitNullWhenUnparseable(t *testing.T) {
	serve(t, nil, map[string]any{testKey: logJSON("Info", "m", nil)})
	a := logsBase
	a.JobIDs, a.IncludeRawFields = []string{testKey}, true
	res, _ := Backend.GetJobLogs(t.Context(), a, nil)
	if got := jsonOf(t, res.Entries[0].Logs[0]); !strings.Contains(got, `"raw":null`) {
		t.Fatalf("row = %s", got)
	}
}

func TestGetJobLogsTotalAboveTheWindowMarksTheEntryTruncated(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, map[string]any{"value": []any{logJSON("Info", "a", nil)}, "@odata.count": 600})
	})
	f.useConfig(t, nil)
	a := logsBase
	a.JobIDs = []string{testKey}
	res, _ := Backend.GetJobLogs(t.Context(), a, nil)
	e := res.Entries[0]
	if e.TotalMatching == nil || *e.TotalMatching != 600 || !e.Truncated {
		t.Fatalf("entry = %+v", e)
	}
}

func TestGetJobLogsVideoIsBestEffort(t *testing.T) {
	const video = "https://cdn.example.com/jobs/recording.webm"
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/VideoRecording/") && strings.Contains(r.URL.Path, testKey2):
			respond(w, http.StatusInternalServerError, map[string]any{"message": "boom"})
		case strings.Contains(r.URL.Path, "/VideoRecording/") && strings.Contains(r.URL.Path, testKey):
			respond(w, http.StatusOK, []any{map[string]any{"uri": video}})
		case strings.Contains(r.URL.Path, "/VideoRecording/"):
			respond(w, http.StatusNotFound, map[string]any{})
		default:
			respond(w, http.StatusOK, page(logJSON("Info", "a", nil)))
		}
	})
	f.useConfig(t, nil)
	a := logsBase
	a.JobIDs, a.Batch, a.IncludeVideo = []string{testKey, testKey2, testKey3}, true, true
	res, err := Backend.GetJobLogs(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := res.Entries
	if e[0].Video == nil || e[0].Video.Kind != "url" || e[0].Video.URL != video {
		t.Errorf("entry 0 = %+v", e[0])
	}
	if e[1].Video != nil || !strings.Contains(e[1].VideoError, "500") || e[1].Returned != 1 {
		t.Errorf("entry 1 = %+v", e[1])
	}
	if e[2].Video != nil || e[2].VideoError != "" {
		t.Errorf("a 404 means no recording, not an error: %+v", e[2])
	}
}

func TestBuildFaultedJobIssueHonoursRepoAndLabels(t *testing.T) {
	serve(t, map[string]any{testKey: jobRow(testKey, nil)}, map[string]any{testKey: logJSON("Error", "Selector not found", nil)})
	res, err := Backend.BuildFaultedJobIssue(t.Context(), orchestrator.FaultIssueArgs{Env: config.Prod, JobID: testKey, Repo: "acme/repo", Labels: []string{"rpa"}})
	if err != nil || !res.Found || res.Repo != "acme/repo" || res.Labels[0] != "rpa" || res.FaultSignature != "selector not found" || !strings.Contains(res.SearchQuery, "repo:acme/repo") {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestBuildFaultedJobIssueForAnUnknownJobIsAStubWithFoundFalse(t *testing.T) {
	serve(t, nil, nil)
	res, err := Backend.BuildFaultedJobIssue(t.Context(), orchestrator.FaultIssueArgs{Env: config.Prod, JobID: testKey})
	if err != nil || res.Found || res.FaultSignature != "job ended in state faulted" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestBackendNameIsUiPath(t *testing.T) {
	if Backend.Name() != orchestrator.UiPath {
		t.Fatalf("name = %q", Backend.Name())
	}
}

func TestFanOutIsBoundedByTheWorkerPool(t *testing.T) {
	var mu sync.Mutex
	inFlight, peak := 0, 0
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(15 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		okEmpty(w, r)
	})
	f.useConfig(t, nil)
	ids := make([]string, 40)
	for i := range ids {
		ids[i] = "id-" + strconv.Itoa(i)
	}
	res := fetchJobsForKeys(t.Context(), ids, "Authorization")
	if len(res) != len(ids) {
		t.Fatalf("results = %d", len(res))
	}
	if peak > maxConcurrency || peak < 2 {
		t.Fatalf("peak concurrency = %d, want 2..%d", peak, maxConcurrency)
	}
}
