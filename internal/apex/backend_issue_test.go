package apex

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const (
	faultJobID = "11111111-aaaa-bbbb-cccc-000000000001"
	parentID   = "11111111-aaaa-bbbb-cccc-000000000000"
)

func rawFaultJob(mod func(*JobDetail)) JobDetail {
	d := jobDetailFixture()
	d.ID, d.Status, d.WorkflowID, d.FolderID = faultJobID, "failed", "wf-1", folderIDs["Authorization"]
	d.WorkflowName, d.WorkflowVersion = "optum-auth-submit", sp("1.4.2")
	d.QueueID, d.QueueItemID, d.QueueItemRef = sp("q-1"), sp("item-1"), sp("0123456789")
	d.ErrorClass = sp("permanent")
	d.ErrorMessage = sp("Portal rejected order 998877 at 2026-10-11T10:00:09Z")
	d.ExitCode, d.Outcome, d.OutcomeLabel = new(21), sp("business_failure"), sp("Business failure")
	d.CreatedAt, d.StartedAt, d.CompletedAt = "2026-10-11T09:59:00Z", sp("2026-10-11T10:00:00Z"), sp("2026-10-11T10:01:00Z")
	d.InputPayload, d.ResultPayload = map[string]any{}, nil
	if mod != nil {
		mod(&d)
	}
	return d
}

// serveFaultJob: routes match by path prefix and the latest registration wins, so the broad detail
// route goes first and the narrower artifacts/list routes override it.
func serveFaultJob(f *fakeApex, id string, detail JobDetail, attempts []JobDetail) {
	f.on("GET", "/jobs/"+id, jsonReply(detail))
	f.on("GET", "/jobs?", pagedReply(attempts, len(attempts)))
	f.on("GET", "/jobs/"+id+"/artifacts", jsonReply([]any{}))
}

const faultLog = `=== apex job x started (workflow=optum-auth-submit runtime=python) ===
{"v":1,"timestamp":"2026-10-11T10:00:01Z","level":"INFO","portal":"OPTUM","message":"login ok"}
{"v":1,"timestamp":"2026-10-11T10:00:09Z","level":"ERROR","portal":"OPTUM","message":"selector not found"}
[apex-adapter] result=FAILED reason=- exit=21 outcomes=-
=== apex job x finished: exit_code=21 timed_out=False cancelled=False suspended=False ===`

func serveFaultLog(f *fakeApex, id, text string) {
	a := Artifact{ID: "art-1", Filename: "job.log", ArtifactType: "logs", SizeBytes: int64(len(text)), DownloadURL: apiPrefix + "/jobs/" + id + "/artifacts/art-1"}
	f.on("GET", "/jobs/"+id+"/artifacts", jsonReply([]Artifact{a}))
	f.on("GET", "/jobs/"+id+"/artifacts/art-1", reply{status: 200, raw: []byte(text)})
}

func buildIssue(t *testing.T, id string) (orchestrator.FaultedJobIssueResult, error) {
	t.Helper()
	return Backend.BuildFaultedJobIssue(t.Context(), orchestrator.FaultIssueArgs{Env: config.Prod, JobID: id})
}

func TestBuildFaultedJobIssueUsesTheJobItsAttemptChainAndTheWarnErrorLogRows(t *testing.T) {
	f := newFakeApex(t, nil)
	detail := rawFaultJob(func(d *JobDetail) { d.RetryOfJobID, d.AttemptNumber = sp(parentID), 2 })
	parent := rawFaultJob(func(d *JobDetail) { d.ID, d.Status, d.AttemptNumber, d.ErrorClass = parentID, "cancelled", 1, nil })
	otherItem := rawFaultJob(func(d *JobDetail) { d.ID, d.QueueItemID = "other-item-job", sp("item-2") }) // same reference, different item
	serveFaultJob(f, faultJobID, detail, []JobDetail{detail, parent, otherItem})
	serveFaultLog(f, faultJobID, faultLog)

	out, err := buildIssue(t, faultJobID)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Found || out.LogsError != "" || !strings.HasPrefix(out.FaultSignature, "apex:") || !slices.Equal(out.Labels, []string{"apex-fault", "bug"}) {
		t.Errorf("result = %+v", out)
	}
	if want := "[Apex Fault] business_failure: Portal rejected order 998877 at 2026-10-11T10:00:09Z (prod)"; out.Title != want {
		t.Errorf("title = %q, want %q", out.Title, want)
	}
	contains(t, out.Body, "1. `"+parentID+"`", "2. `"+faultJobID+"`", "selector not found")
	for _, absent := range []string{"other-item-job", "login ok"} {
		if strings.Contains(out.Body, absent) {
			t.Errorf("body contains %q", absent)
		}
	}
	if p := f.callsTo("GET", "/jobs?")[0].Path; !strings.Contains(p, "reference=0123456789") {
		t.Errorf("attempt listing = %s, want the reference filter", p)
	}
}

func TestBuildFaultedJobIssueLogFetchFailureIsLogsErrorAndTheIssueIsStillBuilt(t *testing.T) {
	f := newFakeApex(t, nil)
	serveFaultJob(f, faultJobID, rawFaultJob(nil), []JobDetail{rawFaultJob(nil)})
	f.on("GET", "/jobs/"+faultJobID+"/artifacts", jsonReply(map[string]any{"detail": "Not Found"}, 404))
	out, err := buildIssue(t, faultJobID)
	if err != nil || !out.Found || out.LogsError == "" || !strings.HasPrefix(out.Title, "[Apex Fault]") {
		t.Errorf("result = %+v, %v", out, err)
	}
}

func TestBuildFaultedJobIssueNeverStartedJobIsNotALogErrorAndIsFlaggedInTheChain(t *testing.T) {
	f := newFakeApex(t, nil)
	reaped := rawFaultJob(func(d *JobDetail) {
		d.ErrorClass, d.ErrorMessage = sp("missing_credential"), sp("No credential bound for portal")
		d.Outcome, d.OutcomeLabel, d.ExitCode, d.StartedAt, d.WorkerID = nil, nil, nil, nil, nil
	})
	serveFaultJob(f, faultJobID, reaped, []JobDetail{reaped})
	out, err := buildIssue(t, faultJobID)
	if err != nil || out.LogsError != "" || out.Title != "[Apex Fault] missing_credential: No credential bound for portal (prod)" {
		t.Fatalf("result = %+v, %v", out, err)
	}
	contains(t, out.Body, "never started")
}

func TestBuildFaultedJobIssueWithoutAQueueItemReferenceListsOnlyItselfAndNeverQueriesJobs(t *testing.T) {
	f := newFakeApex(t, nil)
	serveFaultJob(f, faultJobID, rawFaultJob(func(d *JobDetail) { d.QueueItemRef, d.QueueItemID = nil, nil }), nil)
	out, err := buildIssue(t, faultJobID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.callsTo("GET", "/jobs?")); n != 0 {
		t.Errorf("job listings = %d, want 0", n)
	}
	contains(t, out.Body, "1. `"+faultJobID+"`")
}

func TestBuildFaultedJobIssueRefusesAJobThatDidNotFault(t *testing.T) {
	f := newFakeApex(t, nil)
	done := rawFaultJob(func(d *JobDetail) {
		d.Status, d.ErrorClass, d.ErrorMessage, d.ExitCode, d.Outcome, d.OutcomeLabel = "completed", nil, nil, new(0), sp("success"), sp("Success")
	})
	serveFaultJob(f, faultJobID, done, nil)
	if _, err := buildIssue(t, faultJobID); !shared.IsExpected(err) {
		t.Errorf("error = %v, want an expected refusal", err)
	}
}

func TestBuildFaultedJobIssueRefusesACancelledJobAsNotAFault(t *testing.T) {
	f := newFakeApex(t, nil)
	serveFaultJob(f, faultJobID, rawFaultJob(func(d *JobDetail) { d.Status, d.ErrorClass, d.ErrorMessage = "cancelled", nil, nil }), nil)
	_, err := buildIssue(t, faultJobID)
	if !shared.IsExpected(err) || !strings.Contains(err.Error(), "cancel") {
		t.Errorf("error = %v, want an expected cancellation refusal", err)
	}
}

func TestBuildFaultedJobIssueKeepsOneSignatureForTheSameFaultOnAnotherJob(t *testing.T) {
	f := newFakeApex(t, nil)
	const other = "22222222-aaaa-bbbb-cccc-000000000002"
	serveFaultJob(f, faultJobID, rawFaultJob(func(d *JobDetail) {
		d.ErrorMessage = sp("Selector not found for order 998877 at 2026-10-11T10:00:09Z (job 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d)")
	}), nil)
	a, err := buildIssue(t, faultJobID)
	if err != nil {
		t.Fatal(err)
	}
	serveFaultJob(f, other, rawFaultJob(func(d *JobDetail) {
		d.ID, d.CreatedAt = other, "2026-10-12T01:00:00Z"
		d.ErrorMessage = sp("Selector not found for order 112233 at 2026-10-12T01:00:09Z (job 9f8e7d6c-5b4a-3210-fedc-ba9876543210)")
	}), nil)
	b, err := buildIssue(t, other)
	if err != nil {
		t.Fatal(err)
	}
	if b.FaultSignature != a.FaultSignature || b.SearchQuery != a.SearchQuery || b.Body == a.Body {
		t.Errorf("signatures %q / %q, bodies equal = %v", a.FaultSignature, b.FaultSignature, a.Body == b.Body)
	}
}

// ---- BuildQueueItemPayload ----

func payloadContent() map[string]any {
	return map[string]any{
		"orderUid": "ord-1", "automationId": "auto-1", "IsApproved": true,
		"Units": []any{"1", "2"}, "Diagnoses": []any{"M54.5"}, "ServiceCode": []any{"99213"},
		"DescriptionOfService": []any{"Office visit"}, "Treatments": []any{},
		"IsRetro": false, "IsUrgent": false, "isSpeciality": true, "addManualFacility": false, "IsSameAsRequestingProvider": false,
		"retryCount": float64(0), "callbackContext": map[string]any{"physicianId": float64(7), "orderUid": "ord-1"},
		"eligibilityOtherPatientName": nil,
	}
}

func payloadArgs(mod func(*orchestrator.QueueItemPayloadArgs)) orchestrator.QueueItemPayloadArgs {
	a := orchestrator.QueueItemPayloadArgs{Env: config.PreProd, QueueName: "CENCAL auth submit queue", OrderUID: "ord-1", Content: payloadContent()}
	if mod != nil {
		mod(&a)
	}
	return a
}

func build(t *testing.T, a orchestrator.QueueItemPayloadArgs) (orchestrator.QueueItemPayload, error) {
	t.Helper()
	return Backend.BuildQueueItemPayload(t.Context(), a)
}

func TestBuildPayloadKeepsNativeTypesForcesIsApprovedFalseAndFillsHostsFromConfig(t *testing.T) {
	newFakeApex(t, nil)
	// The incoming content tries to point the robot at another host: config must win.
	out, err := build(t, payloadArgs(func(a *orchestrator.QueueItemPayloadArgs) { a.Content["serverURL"] = "https://evil.example.com" }))
	if err != nil {
		t.Fatal(err)
	}
	sc := out.Payload.(queuePayload).Body.SpecificContent
	if sc["IsApproved"] != false || sc["serverURL"] != testServerURL || sc["queueUrl"] != testQueueURL || sc["isSpeciality"] != true || sc["eligibilityOtherPatientName"] != nil {
		t.Errorf("content = %v", sc)
	}
	if _, ok := sc["Units"].([]any); !ok || sc["retryCount"] != float64(0) {
		t.Errorf("native types lost: %v", sc)
	}
	if !strings.Contains(out.Notes[0], "IsApproved forced to false") {
		t.Errorf("notes = %v, want the forced note first", out.Notes)
	}
}

func TestBuildPayloadShapeReferenceIsOrderUIDDashEpochSecondsAndMatchValueIsAutomationID(t *testing.T) {
	newFakeApex(t, nil)
	out, err := build(t, payloadArgs(nil))
	if err != nil {
		t.Fatal(err)
	}
	built := out.Payload.(queuePayload)
	if built.QueueName != "CENCAL auth submit queue" || built.Body.MatchValue != "auto-1" || !regexp.MustCompile(`^ord-1-\d{10}$`).MatchString(built.Body.Reference) {
		t.Errorf("payload = %+v", built)
	}
	if got := jsonOf(t, built); !strings.HasPrefix(got, `{"queueName":`) || strings.Index(got, `"specific_content"`) > strings.Index(got, `"reference"`) {
		t.Errorf("payload JSON key order = %s, want queueName then body{specific_content,reference,match_value}", got)
	}
	wantMeta := map[string]any{"queueName": "CENCAL auth submit queue", "env": config.PreProd, "orderUid": "ord-1", "note": "post with add_queue_item(orchestrator: apex)"}
	if jsonOf(t, out.Meta) != jsonOf(t, wantMeta) || len(out.Notes) == 0 {
		t.Errorf("meta = %v notes = %v", out.Meta, out.Notes)
	}
}

func TestBuildPayloadDoesNotMutateTheCallersContent(t *testing.T) {
	newFakeApex(t, nil)
	a := payloadArgs(nil)
	if _, err := build(t, a); err != nil {
		t.Fatal(err)
	}
	if _, has := a.Content["serverURL"]; a.Content["IsApproved"] != true || has {
		t.Errorf("caller content mutated: %v", a.Content)
	}
}

func TestBuildPayloadFailsClosedWithoutTheEnvsServerURLNamingTheKey(t *testing.T) {
	newFakeApex(t, nil)
	_, err := build(t, payloadArgs(func(a *orchestrator.QueueItemPayloadArgs) { a.Env = config.Prod }))
	if !shared.IsExpected(err) || !strings.Contains(err.Error(), "apex.serverUrlByEnv.prod") {
		t.Errorf("error = %v, want an expected error naming apex.serverUrlByEnv.prod", err)
	}
}

func TestBuildPayloadRejectsUnroutableAndNonNativeContent(t *testing.T) {
	newFakeApex(t, nil)
	if _, err := build(t, payloadArgs(func(a *orchestrator.QueueItemPayloadArgs) { a.Content["automationId"] = "" })); err == nil || !strings.Contains(err.Error(), "automationId") {
		t.Errorf("empty automationId: error = %v", err)
	}
	if _, err := build(t, payloadArgs(func(a *orchestrator.QueueItemPayloadArgs) { a.Content["Diagnoses"] = `["M54.5"]` })); err == nil || !strings.Contains(err.Error(), "Diagnoses") {
		t.Errorf("stringified Diagnoses: error = %v", err)
	}
}

func TestBuildPayloadWithoutQueueURLIsAnExpectedErrorNamingTheKey(t *testing.T) {
	newFakeApex(t, func(s map[string]any) map[string]any {
		delete(s, "queueUrl")
		return s
	})
	_, err := build(t, payloadArgs(nil))
	if !shared.IsExpected(err) || !strings.Contains(err.Error(), "apex.queueUrl") {
		t.Errorf("error = %v, want an expected error naming apex.queueUrl", err)
	}
}
