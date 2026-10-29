package uipath

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

const testLink = "https://cloud.uipath.com/org/tenant/orchestrator_/jobs/" + testKey

var mapJob = Job{
	ID: "12345", Key: testKey, State: "Faulted", ReleaseName: "PMG auth submit", ProcessVersion: "1.2.3",
	RobotName: "robot-7", CreationTime: "2026-07-01T10:00:00Z", EndTime: "2026-07-01T10:01:30Z",
}

func TestTerminalStatesAreExactlySuccessfulFaultedStopped(t *testing.T) {
	if len(TerminalStates) != 3 || !TerminalStates["Successful"] || !TerminalStates["Faulted"] || !TerminalStates["Stopped"] {
		t.Fatalf("TerminalStates = %v", TerminalStates)
	}
}

func TestToJobCoreMapsAFaultedJobVerbatim(t *testing.T) {
	core := toJobCore(mapJob, testLink, nil)
	want := `{"orchestrator":"uipath","id":"` + testKey + `","state":"Faulted","rawState":"Faulted","terminal":true,` +
		`"process":{"id":null,"name":"PMG auth submit","version":"1.2.3"},"queueItem":null,"createdAt":"2026-07-01T10:00:00Z",` +
		`"startedAt":null,"endedAt":"2026-07-01T10:01:30Z","durationMs":90000,"attempt":null,"retryOfJobId":null,"fault":null,` +
		`"link":"` + testLink + `","details":{"numericId":"12345","robotName":"robot-7"}}`
	if got := jsonOf(t, core); got != want {
		t.Fatalf("core =\n%s\nwant\n%s", got, want)
	}
}

func TestToJobCoreRunningJobHasNoEndOrDuration(t *testing.T) {
	core := toJobCore(Job{Key: testKey, State: "Running", CreationTime: "2026-07-01T10:00:00Z"}, "", nil)
	if core.Terminal || core.EndedAt != nil || core.DurationMs != nil || core.Process.Version != nil {
		t.Fatalf("core = %+v", core)
	}
	if got := jsonOf(t, core.Details); got != `{"numericId":null,"robotName":""}` {
		t.Fatalf("details = %s", got)
	}
}

func TestToJobCoreCarriesOutputOnlyWhenTheReadFetchedIt(t *testing.T) {
	if got := jsonOf(t, toJobCore(mapJob, "", nil).Details); strings.Contains(got, "output") {
		t.Errorf("unfetched output must be omitted: %s", got)
	}
	var null any
	if got := jsonOf(t, toJobCore(mapJob, "", &null).Details); !strings.Contains(got, `"output":null`) {
		t.Errorf("null output is a value: %s", got)
	}
	var obj any = map[string]any{"ok": 1}
	if got := jsonOf(t, toJobCore(mapJob, "", &obj).Details); !strings.Contains(got, `"output":{"ok":1}`) {
		t.Errorf("object output: %s", got)
	}
}

func TestToJobCoreNonTerminalStates(t *testing.T) {
	for _, state := range []string{"Pending", "Running", "Stopping", "Suspended", "Resumed"} {
		if toJobCore(Job{Key: testKey, State: state}, "", nil).Terminal {
			t.Errorf("%s must be non-terminal", state)
		}
	}
}

func TestExceptionTypeOf(t *testing.T) {
	cases := map[string]string{
		"BusinessException": "business", "ApplicationException": "application", "Business": "business",
		"Application": "application", "System": "application", " businessexception ": "business",
		"": "", "SomethingElse": "",
	}
	for in, want := range cases {
		got := exceptionTypeOf(in)
		if (want == "") != (got == nil) || (got != nil && *got != want) {
			t.Errorf("exceptionTypeOf(%q) = %v, want %q", in, got, want)
		}
	}
}

var mapItem = QueueItem{
	ID: 5, Status: "Failed", Reference: "REF-1", CreationTime: "2026-07-01T09:00:00Z", RetryNumber: 1, QueueDefinitionID: 77,
	Name: "PMG auth submit queue", RobotName: "robot-7",
	SpecificContent: map[string]any{"orderUid": "order-uid-1", "MemberID": "m1", "MemberFullName": "Doe, Jane"},
}

func TestToQueueItemCoreFromItemWithholdsSpecificContentByDefault(t *testing.T) {
	core := toQueueItemCoreFromItem(mapItem, "PMG", nil)
	want := `{"orchestrator":"uipath","queueId":"77","queueName":"PMG","itemId":"5","reference":"REF-1","orderUid":"order-uid-1",` +
		`"status":"Failed","rawStatus":"Failed","exceptionType":null,"createdAt":"2026-07-01T09:00:00Z","endedAt":null,"jobs":[],` +
		`"specificContent":null,"details":{"retryNumber":1,"robotName":"robot-7","queueDefinitionId":77,"memberId":"m1","member":"Doe, Jane"}}`
	if got := jsonOf(t, core); got != want {
		t.Fatalf("core =\n%s\nwant\n%s", got, want)
	}
}

func TestToQueueItemCoreMemberColumnsDefaultToEmpty(t *testing.T) {
	item := mapItem
	item.SpecificContent = map[string]any{"MemberID": 5.0}
	d := toQueueItemCoreFromItem(item, "PMG", nil).Details.(ItemDetails)
	if d.MemberID != "" || d.Member != "" {
		t.Fatalf("details = %+v", d)
	}
	if toQueueItemCoreFromItem(item, "PMG", nil).OrderUID != nil {
		t.Error("missing orderUid must be nil")
	}
}

func TestToQueueItemCoreFromMatchCarriesTheJobAndExceptionType(t *testing.T) {
	m := QueueItemMatch{ID: 1, ExecutorJobKey: "k1", ProcessingExceptionType: "BusinessException", QueueDefinitionID: 9, SpecificContent: map[string]any{"orderUid": "u"}}
	core := toQueueItemCoreFromMatch(m, "PMG")
	if len(core.Jobs) != 1 || core.Jobs[0].ID != "k1" || core.Jobs[0].State != nil || core.Jobs[0].NeverStarted != nil {
		t.Fatalf("jobs = %+v", core.Jobs)
	}
	if core.ExceptionType == nil || *core.ExceptionType != "business" {
		t.Fatalf("exceptionType = %v", core.ExceptionType)
	}
	m.ExecutorJobKey = ""
	if got := toQueueItemCoreFromMatch(m, "PMG").Jobs; got == nil || len(got) != 0 {
		t.Fatalf("jobs = %#v, want a non-nil empty slice", got)
	}
}

func TestToQueueProcessAndTriggerCores(t *testing.T) {
	q := toQueueCore(QueueDefinition{ID: 3, Name: "n", Description: "d", CreationTime: "t"})
	if q.ID != "3" || !q.Active || q.ProcessID != nil {
		t.Errorf("queue = %+v", q)
	}
	p := toProcessCore(Release{ID: 8, Key: "rk", Name: "n", ProcessKey: "pkg", ProcessVersion: "", IsLatestVersion: true})
	if p.ID != "rk" || p.Package == nil || *p.Package != "pkg" || p.Version != nil || !p.Active {
		t.Errorf("process = %+v", p)
	}
	qt := toTriggerCore(Trigger{ID: 1, QueueDefinitionID: 77, ReleaseKey: "rk", Enabled: true})
	if qt.Kind != orchestrator.TriggerQueue || qt.QueueID == nil || *qt.QueueID != "77" || qt.ProcessID == nil || qt.Cron != nil {
		t.Errorf("queue trigger = %+v", qt)
	}
	tt := toTriggerCore(Trigger{ID: 2})
	if tt.Kind != orchestrator.TriggerTime || tt.QueueID != nil || tt.ProcessID != nil {
		t.Errorf("time trigger = %+v", tt)
	}
}

func TestUipathFaultClassifiesAndSignsTheHeadlineError(t *testing.T) {
	logs := []orchestrator.JobLog{{Level: "Error", Message: "System.InvalidOperationException: submit rejected for 99887766"}}
	f := uipathFault(mapJob, logs)
	// Signatures dedupe GitHub issues across versions: they must stay byte-identical to the TS ones.
	if deref(f.Message) != "System.InvalidOperationException: submit rejected for 99887766" ||
		deref(f.Signature) != "system.invalidoperationexception: submit rejected for <n>" ||
		deref(f.Class) != "System.InvalidOperationException" {
		t.Fatalf("fault = %+v", f)
	}
	none := uipathFault(mapJob, nil)
	if deref(none.Signature) != "job ended in state faulted" || none.Class != nil {
		t.Fatalf("stateful fault = %+v", none)
	}
}

func TestToLogDigestAddsTheTransactionOutcome(t *testing.T) {
	ended := endedRow(`{"transactionState":"Ended","processingExceptionType":"BusinessException"}`)
	d := toLogDigest([]Log{errLog("Submit failed"), ended})
	if d.Fetched != 2 || len(d.Failures) != 1 || d.Failures[0].Message != "Submit failed" ||
		d.TransactionOutcome == nil || deref(d.TransactionOutcome.ProcessingExceptionType) != "BusinessException" {
		t.Fatalf("digest = %+v", d)
	}
	if toLogDigest([]Log{errLog("x")}).TransactionOutcome != nil {
		t.Error("no RawMessage must give a nil outcome")
	}
	var round map[string]any
	if err := json.Unmarshal([]byte(jsonOf(t, d)), &round); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(round))
	for k := range round {
		keys = append(keys, k)
	}
	if !slices.Contains(keys, "transactionOutcome") || !slices.Contains(keys, "fetched") {
		t.Fatalf("digest json must flatten the base digest beside transactionOutcome: %v", keys)
	}
}
