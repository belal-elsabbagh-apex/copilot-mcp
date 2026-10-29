package apex

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

// The status lists the dashboard bundle's label maps (`Jr`, `$r`) enumerate.
var (
	bundleJobStatuses  = []string{"queued", "claimed", "running", "completed", "failed", "cancelled", "cancelling", "lost", "timed_out", "suspended", "resumed"}
	bundleItemStatuses = []string{"new", "in_progress", "successful", "failed", "retried", "abandoned", "deleted"}
)

func wires(ls []Label) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Wire
	}
	return out
}

func rowCore(row JobRow) orchestrator.JobCore { return toJobCore(row, nil, "") }

func detailCore(d JobDetail) orchestrator.JobCore { return toJobCore(d.JobRow, &d, "") }

func jobDetails(t *testing.T, c orchestrator.JobCore) JobDetails {
	t.Helper()
	d, ok := c.Details.(JobDetails)
	if !ok {
		t.Fatalf("Details = %T, want JobDetails", c.Details)
	}
	return d
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLabelMapsCoverExactlyTheDashboardsStatuses(t *testing.T) {
	if got := wires(JobLabels); !slices.Equal(got, bundleJobStatuses) {
		t.Errorf("job statuses = %v, want %v", got, bundleJobStatuses)
	}
	if got := wires(ItemLabels); !slices.Equal(got, bundleItemStatuses) {
		t.Errorf("item statuses = %v, want %v", got, bundleItemStatuses)
	}
	want := []string{"cancelled", "completed", "failed", "lost", "timed_out"}
	if got := slices.Sorted(slices.Values(TerminalStatuses)); !slices.Equal(got, want) {
		t.Errorf("terminal statuses = %v, want %v", got, want)
	}
}

func TestItemLabelsAreUiPathsSpellingsAndTheWireMapInvertsThem(t *testing.T) {
	var labels []string
	for _, l := range ItemLabels {
		labels = append(labels, l.Label)
		if got := toItemWireStatus(orchestrator.QueueItemStatus(l.Label)); got != l.Wire {
			t.Errorf("toItemWireStatus(%s) = %q, want %q", l.Label, got, l.Wire)
		}
	}
	var want []string
	for _, s := range orchestrator.QueueItemStatuses {
		want = append(want, string(s))
	}
	slices.Sort(labels)
	slices.Sort(want)
	if !slices.Equal(labels, want) {
		t.Errorf("item labels = %v, want the QueueItemStatus spellings %v", labels, want)
	}
}

func TestEveryJobStatusMapsToItsLabelAndTerminalFlag(t *testing.T) {
	for _, l := range JobLabels {
		row := jobRowFixture()
		row.Status = l.Wire
		core := rowCore(row)
		if core.State != l.Label || core.RawState != l.Wire || core.Terminal != IsTerminal(l.Wire) {
			t.Errorf("status %s -> %+v, want state %q terminal %v", l.Wire, core, l.Label, IsTerminal(l.Wire))
		}
	}
}

func TestUnknownJobStatusPassesThroughVerbatimAndIsNotTerminal(t *testing.T) {
	row := jobRowFixture()
	row.Status = "paused_by_ops"
	if core := rowCore(row); core.State != "paused_by_ops" || core.Terminal {
		t.Errorf("core = %+v, want verbatim non-terminal", core)
	}
}

func TestJobCoreShapesIDsTimingRetryFieldsAndLink(t *testing.T) {
	row := jobRowFixture()
	row.AttemptNumber, row.RetryOfJobID = 2, sp("prev-job")
	core := toJobCore(row, nil, "https://x/jobs/1")
	if core.Orchestrator != orchestrator.Apex || core.Process.Name != "demo-auth-submit" || *core.Process.ID != workflowID || *core.Process.Version != "1.0.2" {
		t.Errorf("process = %+v", core.Process)
	}
	if q := core.QueueItem; q == nil || q.QueueID != queueID || q.ItemID != itemID || q.Reference != "1000000001" {
		t.Errorf("queueItem = %+v", q)
	}
	if core.CreatedAt != "2026-10-08T00:39:49.044176Z" || *core.StartedAt != "2026-10-08T00:39:51.248748Z" || *core.EndedAt != "2026-10-08T00:44:14.649417Z" {
		t.Errorf("timing = %v %v %v", core.CreatedAt, *core.StartedAt, *core.EndedAt)
	}
	if *core.DurationMs != 263401 || *core.Attempt != 2 || *core.RetryOfJobID != "prev-job" || core.Link != "https://x/jobs/1" {
		t.Errorf("durationMs/attempt/retry/link = %v %v %v %v", *core.DurationMs, *core.Attempt, *core.RetryOfJobID, core.Link)
	}
}

func TestManualJobHasNoQueueItem(t *testing.T) {
	row := jobRowFixture()
	row.QueueItemID, row.Source = nil, sp("manual")
	core := rowCore(row)
	if core.QueueItem != nil || *jobDetails(t, core).Source != "manual" {
		t.Errorf("core = %+v, want queueItem null and source manual", core)
	}
}

func TestNeverStartedIsTerminalAndUnstartedOnly(t *testing.T) {
	reaped := jobRowFixture()
	reaped.Status, reaped.StartedAt, reaped.ErrorClass = "failed", nil, sp("missing_credential")
	queued := jobRowFixture()
	queued.Status, queued.StartedAt = "queued", nil
	failed := jobRowFixture()
	failed.Status = "failed"
	for name, tc := range map[string]struct {
		row  JobRow
		want bool
	}{"reaped": {reaped, true}, "queued": {queued, false}, "failed after running": {failed, false}} {
		if got := jobDetails(t, rowCore(tc.row)).NeverStarted; got != tc.want {
			t.Errorf("%s: neverStarted = %v, want %v", name, got, tc.want)
		}
	}
}

func TestListRowLeavesDetailOnlyFieldsNullWhileADetailFillsThem(t *testing.T) {
	row := jobRowFixture()
	row.Status, row.ErrorClass = "failed", sp("permanent")
	d := jobDetails(t, rowCore(row))
	if d.Outcome != nil || d.ExitCode != nil || d.NeedsReview != nil || d.EffectiveTimeoutSeconds != nil || *d.ErrorClass != "permanent" {
		t.Errorf("list-row details = %+v, want detail-only fields null", d)
	}
	if got := jsonOf(t, d); strings.Contains(got, `"result"`) || strings.Contains(got, `"input"`) {
		t.Errorf("list-row details JSON = %s, want no result/input keys", got)
	}

	full := jobDetailFixture()
	full.Status, full.ExitCode, full.Outcome, full.NeedsReview = "failed", new(21), sp("business_failure"), true
	fd := jobDetails(t, detailCore(full))
	if *fd.Outcome != "business_failure" || *fd.ExitCode != 21 || !*fd.NeedsReview || *fd.EffectiveTimeoutSeconds != 900 || string(fd.Result) != `{"result":"approved"}` {
		t.Errorf("detail details = %+v, want the detail-only fields filled", fd)
	}
}

func TestInputDropsTokenAndCallbackContextButKeepsTheRest(t *testing.T) {
	d := jobDetails(t, detailCore(jobDetailFixture()))
	if d.Input == nil || !reflect.DeepEqual(*d.Input, map[string]any{"orderUid": "order-1", "retryCount": 0}) {
		t.Errorf("input = %v, want orderUid + retryCount only", d.Input)
	}
	nullInput := jobDetailFixture()
	nullInput.InputPayload = nil
	if got := jsonOf(t, jobDetails(t, detailCore(nullInput))); strings.Contains(got, `"input"`) {
		t.Errorf("details JSON = %s, want no input key for a null input_payload", got)
	}
}

func TestDurationIsNullWhenTheJobNeverRan(t *testing.T) {
	row := jobRowFixture()
	row.DurationSeconds = nil
	if rowCore(row).DurationMs != nil {
		t.Error("durationMs set for a job without duration_seconds")
	}
}

func failedDetail(mod func(*JobDetail)) JobDetail {
	d := jobDetailFixture()
	d.Status = "failed"
	mod(&d)
	return d
}

func faultOf(d JobDetail) *orchestrator.JobFault { return apexFault(d.JobRow, &d) }

func TestFaultIsNilForCompletedAndForAStillRunningDetail(t *testing.T) {
	d := jobDetailFixture()
	d.Status, d.ErrorMessage = "completed", sp("ignored")
	if faultOf(d) != nil {
		t.Error("completed job has a fault")
	}
	d.Status, d.ErrorMessage = "running", sp("x")
	if faultOf(d) != nil {
		t.Error("running job has a fault")
	}
}

func TestFaultMessagePrefersErrorMessageAndClassPrefersOutcome(t *testing.T) {
	f := faultOf(failedDetail(func(d *JobDetail) {
		d.ErrorMessage, d.OutcomeLabel, d.Outcome, d.ErrorClass = sp("referred provider not found"), sp("Business failure"), sp("business_failure"), sp("permanent")
	}))
	if *f.Message != "referred provider not found" || *f.Class != "business_failure" {
		t.Errorf("fault = %s", jsonOf(t, f))
	}
}

func TestFaultFallsBackToOutcomeLabelErrorClassThenAStatusHeadline(t *testing.T) {
	if f := faultOf(failedDetail(func(d *JobDetail) { d.OutcomeLabel = sp("Callback rejected") })); *f.Message != "Callback rejected" {
		t.Errorf("message = %q, want the outcome label", *f.Message)
	}
	if f := faultOf(failedDetail(func(d *JobDetail) { d.ErrorClass = sp("transient") })); *f.Class != "transient" {
		t.Errorf("class = %q, want the error class", *f.Class)
	}
	bare := faultOf(failedDetail(func(d *JobDetail) {}))
	if *bare.Message != "Job ended in status failed" || bare.Class != nil {
		t.Errorf("bare fault = %s", jsonOf(t, bare))
	}
	if want := "apex:unclassified: job ended in status failed"; *bare.Signature != want {
		t.Errorf("signature = %q, want %q", *bare.Signature, want)
	}
}

func TestFaultSignatureIsApexPrefixedAndStableAcrossIDsAndTimestamps(t *testing.T) {
	sig := func(msg string) string {
		return *faultOf(failedDetail(func(d *JobDetail) { d.Outcome, d.ErrorMessage = sp("x"), sp(msg) })).Signature
	}
	a := sig("job aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee failed at 2026-10-08T00:44:14Z")
	b := sig("job 11111111-2222-3333-4444-555555555555 failed at 2026-10-09T09:00:00Z")
	if !strings.HasPrefix(a, "apex:") || a != b {
		t.Errorf("signatures = %q, %q, want equal apex:-prefixed", a, b)
	}
}

func TestListRowFaultCarriesOnlyTheClass(t *testing.T) {
	row := jobRowFixture()
	row.Status, row.ErrorClass = "failed", sp("needs_human")
	if got := jsonOf(t, apexFault(row, nil)); got != `{"message":null,"signature":null,"class":"needs_human"}` {
		t.Errorf("fault = %s", got)
	}
	row.ErrorClass = nil
	if apexFault(row, nil) != nil {
		t.Error("fault without an error class")
	}
	row.Status, row.ErrorClass = "completed", sp("permanent")
	if apexFault(row, nil) != nil {
		t.Error("completed row has a fault")
	}
}

func TestJobAttemptCarriesLabelAndNeverStarted(t *testing.T) {
	row := jobRowFixture()
	row.Status, row.StartedAt = "failed", nil
	if got := jsonOf(t, toJobAttempt(row)); got != `{"orchestrator":"apex","id":"`+jobID+`","state":"Faulted","neverStarted":true}` {
		t.Errorf("attempt = %s", got)
	}
}

func TestRunSummaryFooterOutranksTheDetailAndTheDetailCoversAMissingFooter(t *testing.T) {
	d := jobDetailFixture()
	d.Status, d.ExitCode, d.Outcome, d.ErrorClass = "failed", new(1), sp("crash"), sp("permanent")
	withFooter := toRunSummary(d, parseJobLog("=== apex job x finished: exit_code=21 timed_out=True cancelled=False suspended=False ==="))
	want := `{"exitCode":21,"outcome":"crash","errorClass":"permanent","timedOut":true,"cancelled":false,"adapterResult":null}`
	if got := jsonOf(t, withFooter); got != want {
		t.Errorf("summary = %s, want %s", got, want)
	}
	d.Status = "timed_out"
	noFooter := toRunSummary(d, parseJobLog("plain"))
	if *noFooter.ExitCode != 1 || !noFooter.TimedOut || noFooter.Cancelled {
		t.Errorf("summary = %+v, want the detail's verdict", noFooter)
	}
}

func itemCore(item QueueItem, attempts []orchestrator.JobAttempt, content bool, queue string) orchestrator.QueueItemCore {
	return toItemCore(item, nil, attempts, content, queue)
}

func TestItemCoreLabelsStatusKeepsRawAndExtractsOrderUID(t *testing.T) {
	item := itemFixture()
	item.Status = "in_progress"
	c := itemCore(item, nil, false, "DEMO auth submit queue")
	if c.QueueName != "DEMO auth submit queue" || c.Status != "InProgress" || c.RawStatus != "in_progress" || *c.OrderUID != "order-1" || *c.ExceptionType != "business" {
		t.Errorf("core = %+v", c)
	}
	if c.CreatedAt != "2026-10-08T00:29:26.164862Z" || *c.EndedAt != "2026-10-08T00:44:14.655419Z" {
		t.Errorf("timing = %v %v", c.CreatedAt, *c.EndedAt)
	}
}

func TestItemRetryFieldsLandInDetails(t *testing.T) {
	item := itemFixture()
	item.RetryCount, item.ParentItemID, item.Priority, item.MatchValue = new(2), sp("parent-1"), new(10), sp("auto-9")
	want := `{"retryCount":2,"parentItemId":"parent-1","priority":10,"matchValue":"auto-9","memberId":"","member":""}`
	if got := jsonOf(t, itemCore(item, nil, false, "q").Details); got != want {
		t.Errorf("details = %s, want %s", got, want)
	}
}

func TestMemberFieldsComeFromTheContentEvenWhenContentIsNotRequested(t *testing.T) {
	item := itemFixture()
	item.SpecificContent = map[string]any{"orderUid": "o", "MemberID": "M-1", "MemberFullName": "Doe, Jane", "token": "t"}
	c := itemCore(item, nil, false, "q")
	d := c.Details.(ItemDetails)
	if c.SpecificContent != nil || d.MemberID != "M-1" || d.Member != "Doe, Jane" {
		t.Errorf("core = %+v, want null content with the member projection", c)
	}
	item.SpecificContent = map[string]any{"MemberID": float64(7)}
	if d := itemCore(item, nil, false, "q").Details.(ItemDetails); d.MemberID != "" || d.Member != "" {
		t.Errorf("details = %+v, want empty strings for a non-string MemberID", d)
	}
}

func TestItemContentIsNullUnlessRequestedAndTheTokenIsRedactedWhenItIs(t *testing.T) {
	if itemCore(itemFixture(), nil, false, "q").SpecificContent != nil {
		t.Error("content present without includeContent")
	}
	sc := itemCore(itemFixture(), nil, true, "q").SpecificContent
	if sc["token"] != "[redacted]" || sc["orderUid"] != "order-1" {
		t.Errorf("content = %v, want the token redacted", sc)
	}
}

func TestUnexpectedExceptionTypeAndMissingOrderUIDDegradeToNull(t *testing.T) {
	item := itemFixture()
	item.ExceptionType, item.SpecificContent = sp("weird"), map[string]any{"orderUid": float64(5)}
	if c := itemCore(item, nil, false, "q"); c.ExceptionType != nil || c.OrderUID != nil {
		t.Errorf("core = %+v, want null exceptionType and orderUid", c)
	}
}

func TestItemDetailAddsCallbackStatusAndCamelCasedLineage(t *testing.T) {
	detail := QueueItemDetail{QueueItem: itemFixture(), CallbackStatus: sp("failed"), Lineage: []LineageRow{
		{ItemID: "i1", Reference: "r1", Status: "retried", Job: &LineageJob{ID: "j1", Status: "cancelled", ErrorClass: sp("transient")}},
		{ItemID: "i2", Reference: "r2", Status: "new"},
	}}
	c := toItemCore(detail.QueueItem, &detail, nil, false, "q")
	d := c.Details.(ItemDetailsFull)
	if *d.CallbackStatus != "failed" {
		t.Errorf("callbackStatus = %v", d.CallbackStatus)
	}
	want := `[{"itemId":"i1","reference":"r1","status":"Retried","exceptionType":null,"jobId":"j1","jobStatus":"Stopped","errorClass":"transient"},` +
		`{"itemId":"i2","reference":"r2","status":"New","exceptionType":null,"jobId":null,"jobStatus":null,"errorClass":null}]`
	if got := jsonOf(t, d.Lineage); got != want {
		t.Errorf("lineage = %s, want %s", got, want)
	}
}

func TestAttemptsFromItemExposesOnlyTheLatestJobWithUnknownState(t *testing.T) {
	want := `[{"orchestrator":"apex","id":"` + jobID + `","state":null,"neverStarted":null}]`
	if got := jsonOf(t, attemptsFromItem(itemFixture())); got != want {
		t.Errorf("attempts = %s, want %s", got, want)
	}
	noJob := itemFixture()
	noJob.JobID = nil
	if got := jsonOf(t, attemptsFromItem(noJob)); got != "[]" {
		t.Errorf("attempts = %s, want []", got)
	}
}

func TestQueueCore(t *testing.T) {
	q := queueFixture()
	q.IsActive, q.MaxRetries = false, new(3)
	want := `{"orchestrator":"apex","id":"` + queueID + `","name":"DEMO auth submit queue","processId":"` + workflowID + `","active":false,` +
		`"details":{"autoDrain":true,"maxConcurrentJobs":1,"maxRetries":3,"enforceUniqueReference":false}}`
	if got := jsonOf(t, toQueueCore(q)); got != want {
		t.Errorf("queue core = %s, want %s", got, want)
	}
}

func TestProcessCoreRenamesTheWorkflowFields(t *testing.T) {
	want := `{"orchestrator":"apex","id":"` + workflowID + `","name":"demo-auth-submit","package":"demo-auth-submit","version":"1.0.2","active":true,` +
		`"details":{"timeoutSeconds":900,"maxAttempts":3,"backoffStrategy":"exponential","artifactPolicy":"on_failure"}}`
	if got := jsonOf(t, toProcessCore(workflowFixture())); got != want {
		t.Errorf("process core = %s, want %s", got, want)
	}
}

func TestQueueTriggerIsEnabledOnlyWhenActiveAndAutoDraining(t *testing.T) {
	paused, noDrain := queueFixture(), queueFixture()
	paused.IsActive = false
	noDrain.AutoDrain, noDrain.MaxConcurrentJobs = false, new(4)
	got := toQueueTriggers([]Queue{queueFixture(), paused, noDrain})
	if b := got[0]; b.Kind != orchestrator.TriggerQueue || !b.Enabled || b.Cron != nil || *b.QueueID != queueID || *b.ProcessID != workflowID || *b.Details.(QueueTriggerDetails).MaxConcurrent != 1 {
		t.Errorf("active trigger = %+v", b)
	}
	if got[1].Enabled {
		t.Error("paused queue trigger is enabled")
	}
	if got[2].Enabled || *got[2].Details.(QueueTriggerDetails).MaxConcurrent != 4 {
		t.Errorf("non-draining trigger = %+v", got[2])
	}
}

func TestScheduleBecomesATimeTriggerAndPausedMeansDisabled(t *testing.T) {
	s := Schedule{ID: "s1", WorkflowID: workflowID, CronExpression: "0 6 * * *", Timezone: "UTC"}
	tr := toScheduleTrigger(s)
	if tr.Kind != orchestrator.TriggerTime || *tr.Cron != "0 6 * * *" || !tr.Enabled || tr.QueueID != nil || *tr.ProcessID != workflowID || tr.Name != "0 6 * * * (UTC)" {
		t.Errorf("trigger = %+v", tr)
	}
	s.IsPaused = true
	if toScheduleTrigger(s).Enabled {
		t.Error("paused schedule is enabled")
	}
}

func TestQueueAndTriggerKeepNullLimitsNull(t *testing.T) {
	q := queueFixture()
	q.MaxConcurrentJobs = nil
	if got := jsonOf(t, toQueueCore(q).Details); !strings.Contains(got, `"maxConcurrentJobs":null`) {
		t.Errorf("queue details = %s", got)
	}
	if got := jsonOf(t, toQueueTriggers([]Queue{q})[0].Details); got != `{"maxConcurrent":null}` {
		t.Errorf("trigger details = %s", got)
	}
}
