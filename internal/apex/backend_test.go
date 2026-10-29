package apex

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

const stampT = "2026-10-08T00:40:00.000Z"

func ndT(o map[string]any) string {
	base := map[string]any{"timestamp": stampT}
	for k, v := range o {
		base[k] = v
	}
	return nd(base)
}

func footerLine(exit int) string {
	return fmt.Sprintf("=== apex job %s finished: exit_code=%d timed_out=False cancelled=False suspended=False ===", jobID, exit)
}

var backendLog = strings.Join([]string{
	"=== apex job " + jobID + " started (workflow=demo runtime=python) ===",
	ndT(map[string]any{"level": "INFO", "message": "login ok"}),
	ndT(map[string]any{"level": "WARN", "message": "slow page", "fields": map[string]any{"ms": 5200}}),
	ndT(map[string]any{"level": "ERROR", "message": "row not found"}),
	ndT(map[string]any{"level": "INFO", "message": strings.Repeat("x", 500)}),
	"[apex-adapter] result=FAILED reason=business_failure exit=21 outcomes=-",
	footerLine(21),
}, "\n")

func logArtifact() Artifact { return artifactFixture("job.log", "logs", 100) }

func artRoute(a Artifact) string { return strings.TrimPrefix(a.DownloadURL, apiPrefix) }

// routeJob serves one job: detail, artifact listing and the job.log download. The more specific
// routes are registered last (the fake takes the last matching prefix).
func routeJob(f *fakeApex, detail JobDetail, artifacts []Artifact, log string) {
	f.on("GET", "/jobs/"+detail.ID, jsonReply(detail))
	f.on("GET", "/jobs/"+detail.ID+"/artifacts", jsonReply(artifacts))
	f.on("GET", artRoute(logArtifact()), reply{status: 200, raw: []byte(log)})
}

func failedDetailFixture(mod func(*JobDetail)) JobDetail {
	d := jobDetailFixture()
	d.Status = "failed"
	if mod != nil {
		mod(&d)
	}
	return d
}

func TestListJobsWithoutProcessNameMakesOneNewestFirstRequestAndMapsCores(t *testing.T) {
	f := newFakeApex(t, nil)
	row := jobRowFixture()
	row.Status, row.ErrorClass = "failed", sp("permanent")
	f.on("GET", "/jobs", pagedReply([]JobRow{row}, 1))
	out, err := Backend.ListJobs(t.Context(), orchestrator.ListJobsArgs{Env: config.PreProd, Since: "2026-10-01T00:00:00Z", Top: 5})
	if err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("GET", "/jobs")
	if len(calls) != 1 {
		t.Fatalf("job requests = %d, want 1", len(calls))
	}
	q := queryOf(t, calls[0])
	if q.Get("created_after") != "2026-10-01T00:00:00Z" || q.Get("sort_by") != "created_at" || q.Get("sort_dir") != "desc" || q.Has("workflow_id") {
		t.Errorf("query = %v", q)
	}
	j := out.Jobs[0]
	if out.Folder != "Authorization Test" || j.State != "Faulted" || j.Link != f.URL+"/jobs/"+jobID || *j.Fault.Class != "permanent" || j.Fault.Message != nil {
		t.Errorf("result = %+v", out)
	}
}

func TestListJobsProcessNameMatchesWorkflowsBySubstringAndMergesNewestFirst(t *testing.T) {
	f := newFakeApex(t, nil)
	wf := func(id, name string) Workflow { w := workflowFixture(); w.ID, w.Name = id, name; return w }
	f.on("GET", "/workflows", pagedReply([]Workflow{wf("wf-a", "demo-auth-submit"), wf("wf-b", "other-thing"), wf("wf-c", "DEMO-eligibility")}, 3))
	job := func(id, at string) JobRow { r := jobRowFixture(); r.ID, r.CreatedAt = id, at; return r }
	f.route("GET", "/jobs", func(c apexCall) reply {
		if queryOf(t, c).Get("workflow_id") == "wf-a" {
			return pagedReply([]JobRow{job("a2", "2026-10-08T00:00:05Z"), job("a1", "2026-10-08T00:00:01Z")}, 2)
		}
		return pagedReply([]JobRow{job("c1", "2026-10-08T00:00:03Z")}, 1)
	})
	out, err := Backend.ListJobs(t.Context(), orchestrator.ListJobsArgs{Env: config.PreProd, Top: 2, ProcessName: "DEMO"})
	if err != nil {
		t.Fatal(err)
	}
	var asked, ids []string
	for _, c := range f.callsTo("GET", "/jobs") {
		asked = append(asked, queryOf(t, c).Get("workflow_id"))
	}
	for _, j := range out.Jobs {
		ids = append(ids, j.ID)
	}
	slices.Sort(asked)
	if !slices.Equal(asked, []string{"wf-a", "wf-c"}) || !slices.Equal(ids, []string{"a2", "c1"}) {
		t.Errorf("workflows queried = %v, jobs = %v", asked, ids)
	}
}

func TestListJobsProcessNameMatchingNothingQueriesNoJobs(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/workflows", pagedReply([]Workflow{workflowFixture()}, 1))
	out, err := Backend.ListJobs(t.Context(), orchestrator.ListJobsArgs{Env: config.PreProd, Top: 5, ProcessName: "zzz"})
	if err != nil || len(out.Jobs) != 0 || out.Jobs == nil {
		t.Fatalf("ListJobs = %+v, %v, want a non-nil empty list", out, err)
	}
	if n := len(f.callsTo("GET", "/jobs")); n != 0 {
		t.Errorf("job requests = %d, want 0", n)
	}
}

func getJobsArgs(ids ...string) orchestrator.GetJobsArgs {
	return orchestrator.GetJobsArgs{Env: config.PreProd, JobIDs: ids}
}

func TestGetJobs404IsNotFoundNotAnError(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/"+jobID, jsonReply(map[string]any{"detail": "Job not found"}, 404))
	out, err := Backend.GetJobs(t.Context(), getJobsArgs(jobID), nil)
	if err != nil || len(out.Entries) != 1 || out.Entries[0].ID != jobID || out.Entries[0].Found || out.Entries[0].Error != "" {
		t.Errorf("GetJobs = %+v, %v, want one not-found entry", out, err)
	}
}

func TestGetJobsNon404FailureThrowsForOneIDButLandsOnTheEntryInABatch(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/bad", jsonReply(map[string]any{"detail": "kaput"}, 500))
	f.on("GET", "/jobs/"+jobID, jsonReply(jobDetailFixture()))
	if _, err := Backend.GetJobs(t.Context(), getJobsArgs("bad"), nil); err == nil || !strings.Contains(err.Error(), "kaput") {
		t.Errorf("single-id error = %v, want kaput", err)
	}
	args := getJobsArgs("bad", jobID)
	args.Batch = true
	out, err := Backend.GetJobs(t.Context(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e := out.Entries[0]; e.ID != "bad" || e.Found || !strings.Contains(e.Error, "kaput") {
		t.Errorf("entry 0 = %+v", e)
	}
	if e := out.Entries[1]; e.ID != jobID || !e.Found {
		t.Errorf("entry 1 = %+v, want found, in input order", e)
	}
}

func TestGetJobsLogDigestSummarizesTheRunFromTheFooter(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(func(d *JobDetail) { d.Outcome, d.ExitCode = sp("business_failure"), new(21) }), []Artifact{logArtifact()}, backendLog)
	args := getJobsArgs(jobID)
	args.IncludeLogDigest = true
	out, err := Backend.GetJobs(t.Context(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := out.Entries[0]
	digest, ok := e.LogDigest.(LogDigest)
	if e.LogsUnavailable != nil || !ok {
		t.Fatalf("entry = %+v, want a digest", e)
	}
	if digest.ByLevel.Get("Info") != 5 || digest.ByLevel.Get("Warn") != 1 || digest.ByLevel.Get("Error") != 1 {
		t.Errorf("byLevel = %s", jsonOf(t, digest.ByLevel))
	}
	run := digest.Run
	if *run.ExitCode != 21 || *run.Outcome != "business_failure" || run.TimedOut || run.Cancelled {
		t.Errorf("run = %+v", run)
	}
	var failures []string
	for _, c := range digest.Failures {
		failures = append(failures, c.Message)
	}
	if !slices.Contains(failures, "row not found") || !strings.HasPrefix(*e.Job.Fault.Signature, "apex:") {
		t.Errorf("failures = %v, signature = %v", failures, e.Job.Fault.Signature)
	}
}

func TestGetJobsRunningJobHasNoLogYetAndMakesNoArtifactRequest(t *testing.T) {
	f := newFakeApex(t, nil)
	d := jobDetailFixture()
	d.Status, d.CompletedAt = "running", nil
	f.on("GET", "/jobs/"+jobID, jsonReply(d))
	args := getJobsArgs(jobID)
	args.IncludeLogDigest = true
	out, _ := Backend.GetJobs(t.Context(), args, nil)
	if u := out.Entries[0].LogsUnavailable; u == nil || u.Reason != orchestrator.LogsRunning {
		t.Errorf("logsUnavailable = %+v, want running", u)
	}
	if n := len(f.callsTo("GET", "/jobs/"+jobID+"/artifacts")); n != 0 {
		t.Errorf("artifact requests = %d, want 0", n)
	}
}

func TestGetJobsNeverStartedJobHasNoArtifactsAndMakesNoArtifactRequest(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/"+jobID, jsonReply(failedDetailFixture(func(d *JobDetail) { d.StartedAt, d.ErrorClass = nil, sp("missing_credential") })))
	args := getJobsArgs(jobID)
	args.IncludeLogDigest = true
	out, _ := Backend.GetJobs(t.Context(), args, nil)
	e := out.Entries[0]
	if e.LogsUnavailable == nil || e.LogsUnavailable.Reason != orchestrator.LogsNeverStarted || !jobDetails(t, *e.Job).NeverStarted {
		t.Errorf("entry = %+v, want never-started", e)
	}
	if n := len(f.callsTo("GET", "/jobs/"+jobID+"/artifacts")); n != 0 {
		t.Errorf("artifact requests = %d, want 0", n)
	}
}

func TestGetJobsFinishedJobWithoutAJobLogArtifactIsArtifactPolicy(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, jobDetailFixture(), []Artifact{artifactFixture("bot.log", "other", 5)}, "")
	args := getJobsArgs(jobID)
	args.IncludeLogDigest = true
	out, _ := Backend.GetJobs(t.Context(), args, nil)
	if e := out.Entries[0]; e.LogsUnavailable == nil || e.LogsUnavailable.Reason != orchestrator.LogsArtifactPolicy || e.LogDigest != nil {
		t.Errorf("entry = %+v, want artifact-policy and no digest", e)
	}
}

func TestGetJobsFailingLogDownloadIsLogDigestErrorAndTheJobStillReturns(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, jobDetailFixture(), []Artifact{logArtifact()}, "")
	f.on("GET", artRoute(logArtifact()), jsonReply(map[string]any{"detail": "forbidden"}, 403))
	args := getJobsArgs(jobID)
	args.IncludeLogDigest = true
	out, _ := Backend.GetJobs(t.Context(), args, nil)
	if e := out.Entries[0]; !e.Found || !strings.Contains(e.LogDigestError, "forbidden") || e.LogDigest != nil {
		t.Errorf("entry = %+v, want found with logDigestError", e)
	}
}

func TestGetJobsReportsProgressOncePerJob(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/", jsonReply(jobDetailFixture()))
	var mu sync.Mutex
	var seen []int
	args := getJobsArgs("j1", "j2", "j3")
	args.Batch = true
	if _, err := Backend.GetJobs(t.Context(), args, func(done, total int, _ string) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, done)
		if total != 3 {
			t.Errorf("total = %d, want 3", total)
		}
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(seen)
	if !slices.Equal(seen, []int{1, 2, 3}) {
		t.Errorf("progress ticks = %v, want 1 2 3", seen)
	}
}

// ---- GetJobLogs ----

func logsArgs() orchestrator.GetJobLogsArgs {
	return orchestrator.GetJobLogsArgs{Env: config.PreProd, JobIDs: []string{jobID}}
}

func TestGetJobLogsReturnsTheWholeLogTruncatingMessagesUnlessFullMessages(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(nil), []Artifact{logArtifact()}, backendLog)
	out, err := Backend.GetJobLogs(t.Context(), logsArgs(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e := out.Entries[0]
	if e.JobID != jobID || e.Returned != 7 || *e.TotalMatching != 7 || e.Truncated {
		t.Errorf("entry = %+v", e)
	}
	long := func(e orchestrator.JobLogsEntry) int {
		for _, l := range e.Logs {
			if strings.HasPrefix(l.Message, "xxx") {
				return len(l.Message)
			}
		}
		return -1
	}
	if got := long(e); got != 401+2 { // 400 x's + the 3-byte ellipsis
		t.Errorf("truncated message = %d bytes, want 403", got)
	}
	args := logsArgs()
	args.FullMessages = true
	full, _ := Backend.GetJobLogs(t.Context(), args, nil)
	if got := long(full.Entries[0]); got != 500 {
		t.Errorf("full message = %d bytes, want 500", got)
	}
}

func TestGetJobLogsKeepsTheFirst500MatchingRowsAndFlagsTruncated(t *testing.T) {
	f := newFakeApex(t, nil)
	lines := make([]string, 600)
	for i := range lines {
		lines[i] = ndT(map[string]any{"level": "INFO", "message": fmt.Sprintf("row %d", i)})
	}
	routeJob(f, failedDetailFixture(nil), []Artifact{logArtifact()}, strings.Join(lines, "\n"))
	out, _ := Backend.GetJobLogs(t.Context(), logsArgs(), nil)
	e := out.Entries[0]
	if e.Returned != 500 || *e.TotalMatching != 600 || !e.Truncated || e.Logs[0].Message != "row 0" || e.Logs[499].Message != "row 499" {
		t.Errorf("window = returned %d of %d truncated=%v first=%q last=%q", e.Returned, *e.TotalMatching, e.Truncated, e.Logs[0].Message, e.Logs[499].Message)
	}
	args := logsArgs()
	args.Filter.Tail = new(800)
	tail, _ := Backend.GetJobLogs(t.Context(), args, nil)
	if e := tail.Entries[0]; e.Returned != 500 || *e.TotalMatching != 600 || !e.Truncated || e.Logs[499].Message != "row 599" {
		t.Errorf("tail window = returned %d of %d, last %q", e.Returned, *e.TotalMatching, e.Logs[499].Message)
	}
}

func TestGetJobLogsOver5MiBIsReadFromItsTailAndReportedTruncatedInTheDigestToo(t *testing.T) {
	f := newFakeApex(t, nil)
	big := logArtifact()
	big.SizeBytes = 5*1024*1024 + 1
	routeJob(f, failedDetailFixture(nil), []Artifact{big}, backendLog)
	out, _ := Backend.GetJobLogs(t.Context(), logsArgs(), nil)
	if got := f.callsTo("GET", artRoute(big))[0].Header.Get("Range"); got != "bytes=-5242880" {
		t.Errorf("Range = %q, want the 5 MiB tail", got)
	}
	if e := out.Entries[0]; e.Returned != 7 || *e.TotalMatching != 7 || !e.Truncated {
		t.Errorf("entry = %+v, want truncated", e)
	}
	args := getJobsArgs(jobID)
	args.IncludeLogDigest = true
	jobs, _ := Backend.GetJobs(t.Context(), args, nil)
	if note := jobs.Entries[0].LogDigest.(LogDigest).Note; !strings.Contains(note, "last 5 MiB") {
		t.Errorf("digest note = %q, want the partial-log note", note)
	}
}

func TestGetJobLogsMinLevelContainsAndTailNarrowClientSide(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(nil), []Artifact{logArtifact()}, backendLog)
	run := func(fl orchestrator.LogFilter) orchestrator.JobLogsEntry {
		args := logsArgs()
		args.Filter = fl
		out, err := Backend.GetJobLogs(t.Context(), args, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out.Entries[0]
	}
	level := func(e orchestrator.JobLogsEntry) []string {
		var out []string
		for _, l := range e.Logs {
			out = append(out, l.Level)
		}
		return out
	}
	if got := level(run(orchestrator.LogFilter{MinLevel: orchestrator.LevelWarn})); !slices.Equal(got, []string{"Warn", "Error"}) {
		t.Errorf("warn levels = %v", got)
	}
	if e := run(orchestrator.LogFilter{MinLevel: orchestrator.LevelError}); len(e.Logs) != 1 || e.Logs[0].Message != "row not found" {
		t.Errorf("error rows = %+v", e.Logs)
	}
	if e := run(orchestrator.LogFilter{MinLevel: orchestrator.LevelWarn, Tail: new(1)}); e.Returned != 1 || *e.TotalMatching != 2 {
		t.Errorf("tail entry = returned %d of %d, want 1 of 2 (pre-tail count)", e.Returned, *e.TotalMatching)
	}
	if e := run(orchestrator.LogFilter{Contains: "slow"}); len(e.Logs) != 1 || e.Logs[0].Message != "slow page" {
		t.Errorf("contains rows = %+v", e.Logs)
	}
}

func TestGetJobLogsOnlyFailuresKeepsErrorRowsAndFailureWording(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(nil), []Artifact{logArtifact()}, backendLog)
	args := logsArgs()
	args.Filter.OnlyFailures = true
	out, _ := Backend.GetJobLogs(t.Context(), args, nil)
	var msgs []string
	for _, l := range out.Entries[0].Logs {
		msgs = append(msgs, l.Message)
	}
	if !slices.Contains(msgs, "row not found") || !slices.ContainsFunc(msgs, func(m string) bool { return strings.Contains(m, "result=FAILED") }) || slices.Contains(msgs, "login ok") {
		t.Errorf("failure rows = %v", msgs)
	}
}

func TestGetJobLogsIncludeRawFieldsReturnsTheNDJSONFieldsOrNull(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(nil), []Artifact{logArtifact()}, backendLog)
	args := logsArgs()
	args.IncludeRawFields = true
	out, _ := Backend.GetJobLogs(t.Context(), args, nil)
	raw := map[string]string{}
	for _, l := range out.Entries[0].Logs {
		raw[l.Message] = string(l.Raw)
	}
	if raw["slow page"] != `{"ms":5200}` || raw["login ok"] != "null" {
		t.Errorf("raw = %v", raw)
	}
}

func TestGetJobLogsIncludeVideoPointsAtTheRecordingOrExplainsItsAbsence(t *testing.T) {
	f := newFakeApex(t, nil)
	video := artifactFixture("recording.mp4", "videos", 77)
	routeJob(f, failedDetailFixture(nil), []Artifact{logArtifact(), video}, backendLog)
	args := logsArgs()
	args.IncludeVideo = true
	out, _ := Backend.GetJobLogs(t.Context(), args, nil)
	if v := out.Entries[0].Video; v == nil || v.Artifact != "recording.mp4" || *v.SizeBytes != 77 || v.FetchWith != "get_job_artifacts" {
		t.Errorf("video = %+v", v)
	}
	readCache.Flush() // the fixture job is settled, so the first listing was cached; this swaps what upstream serves
	routeJob(f, failedDetailFixture(nil), []Artifact{logArtifact()}, backendLog)
	out, _ = Backend.GetJobLogs(t.Context(), args, nil)
	if e := out.Entries[0]; e.Video != nil || !strings.Contains(e.VideoError, "no video") {
		t.Errorf("entry = %+v, want a videoError", e)
	}
}

func TestGetJobLogsRunningJobReturnsNoRowsAndExplainsTheVideoToo(t *testing.T) {
	f := newFakeApex(t, nil)
	d := jobDetailFixture()
	d.Status, d.CompletedAt = "running", nil
	f.on("GET", "/jobs/"+jobID, jsonReply(d))
	args := logsArgs()
	args.IncludeVideo = true
	out, _ := Backend.GetJobLogs(t.Context(), args, nil)
	e := out.Entries[0]
	if len(e.Logs) != 0 || e.Logs == nil || e.Returned != 0 || e.LogsUnavailable.Reason != orchestrator.LogsRunning || e.VideoError == "" {
		t.Errorf("entry = %+v", e)
	}
}

func TestGetJobLogsMissingJobThrowsForOneIDAndLandsOnTheEntryInABatch(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/"+jobID, jsonReply(map[string]any{"detail": "Job not found"}, 404))
	if _, err := Backend.GetJobLogs(t.Context(), logsArgs(), nil); err == nil || !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Errorf("single-id error = %v", err)
	}
	args := logsArgs()
	args.Batch = true
	out, err := Backend.GetJobLogs(t.Context(), args, nil)
	if e := out.Entries[0]; err != nil || e.JobID != jobID || len(e.Logs) != 0 || !strings.Contains(e.Error, "not found") {
		t.Errorf("batch entry = %+v, %v", e, err)
	}
}

func TestGetJobLogsFanOutIsBoundedByTheDataSemaphore(t *testing.T) {
	f := newFakeApex(t, nil)
	var mu sync.Mutex
	inFlight, peak := 0, 0
	f.route("GET", "/jobs/", func(apexCall) reply {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
		return jsonReply(jobDetailFixture()) // the handler's own latency is the overlap window
	})
	ids := make([]string, 25)
	for i := range ids {
		ids[i] = fmt.Sprintf("j%d", i)
	}
	args := getJobsArgs(ids...)
	args.Batch = true
	out, err := Backend.GetJobs(t.Context(), args, nil)
	if err != nil || len(out.Entries) != 25 {
		t.Fatalf("GetJobs = %d entries, %v", len(out.Entries), err)
	}
	for i, e := range out.Entries {
		if e.ID != ids[i] {
			t.Fatalf("entry %d id = %s, want input order", i, e.ID)
		}
	}
	if peak > fanOut {
		t.Errorf("peak concurrent data GETs = %d, want at most %d", peak, fanOut)
	}
}
