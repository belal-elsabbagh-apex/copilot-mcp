package uipath

import (
	"net/http"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

var faultedJob = Job{
	ID: "12345", Key: testKey, State: "Faulted",
	CreationTime: "2026-06-30T10:00:00Z", EndTime: "2026-06-30T10:01:00Z",
}

func errLog(msg string) Log {
	return Log{JobLog: orchestrator.JobLog{Level: "Error", Message: msg, TimeStamp: "2026-06-30T10:00:30Z"}}
}

func endedRow(raw string) Log {
	return Log{JobLog: orchestrator.JobLog{Level: "Info", Message: "Transaction Ended", TimeStamp: "2026-07-01T10:00:00Z"}, RawMessage: raw}
}

func TestExtractTransactionOutcomeReadsTheQueuesOwnClassification(t *testing.T) {
	ended := `{"transactionState":"Ended","transactionId":"d211b063-20eb-45a9-801a-5634cd13568a","queueName":"CenCal auth sync queue",
		"processingExceptionType":"BusinessException","processingExceptionReason":"","transactionExecutionTime":49.93,
		"queueItemPriority":"Normal","queueItemReviewStatus":"None"}`
	got := extractTransactionOutcome([]Log{logRow("Info", "Transaction Started"), endedRow(ended)})
	if got == nil || deref(got.TransactionID) != "d211b063-20eb-45a9-801a-5634cd13568a" || deref(got.QueueName) != "CenCal auth sync queue" ||
		deref(got.ProcessingExceptionType) != "BusinessException" || got.ProcessingExceptionReason != nil ||
		*got.TransactionExecutionTimeSec != 49.93 || deref(got.QueueItemPriority) != "Normal" || deref(got.QueueItemReviewStatus) != "None" {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestExtractTransactionOutcomeIsNilWithoutRawMessageOrAnEndedRow(t *testing.T) {
	if extractTransactionOutcome([]Log{logRow("Info", "Transaction Ended")}) != nil {
		t.Error("RawMessage never fetched must give nil")
	}
	started := Log{JobLog: logRow("Info", "Transaction Started").JobLog, RawMessage: `{"transactionState":"Started"}`}
	if extractTransactionOutcome([]Log{started, errLog("Job crashed before ending the transaction")}) != nil {
		t.Error("no Ended row must give nil")
	}
}

func TestExtractTransactionOutcomeTakesTheLastEndedRow(t *testing.T) {
	first := endedRow(`{"transactionState":"Ended","processingExceptionType":"BusinessException"}`)
	second := endedRow(`{"transactionState":"Ended","processingExceptionType":""}`)
	if got := extractTransactionOutcome([]Log{first, second}); got == nil || got.ProcessingExceptionType != nil {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestFormatFaultedJobIssueBuildsTitleBodyMarkerAndSearchQuery(t *testing.T) {
	issue := FormatFaultedJobIssue(faultedJob, []Log{errLog("Selector not found for order 998877")}, FormatOptions{
		Env: config.Prod, JobKey: faultedJob.Key, DeepLink: "https://cloud.uipath.com/org/tenant/orchestrator_/jobs",
	})
	if issue.Repo != orchestrator.DefaultRepo || strings.Join(issue.Labels, ",") != "uipath-fault,bug" {
		t.Errorf("repo/labels = %q %v", issue.Repo, issue.Labels)
	}
	if issue.Title != "[UiPath Fault] Selector not found for order 998877 (prod)" {
		t.Errorf("title = %q", issue.Title)
	}
	// The signature is the dedupe key across versions: it must stay byte-identical to the TS one.
	if issue.FaultSignature != "selector not found for order <n>" {
		t.Errorf("signature = %q", issue.FaultSignature)
	}
	for _, want := range []string{
		"<!-- fault-signature: " + issue.FaultSignature + " -->", "**State:** Faulted", "**Job Key:** `" + faultedJob.Key + "`",
		"https://cloud.uipath.com/org/tenant/orchestrator_/jobs", "**Job Id:** 12345", "**Ended:** 2026-06-30T10:01:00Z",
	} {
		if !strings.Contains(issue.Body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	wantQuery := `repo:` + orchestrator.DefaultRepo + ` is:issue is:open in:body "fault-signature: ` + issue.FaultSignature + `"`
	if issue.SearchQuery != wantQuery {
		t.Errorf("searchQuery = %q", issue.SearchQuery)
	}
	if !strings.Contains(issue.RecurrenceComment, "**Job Key:** `"+faultedJob.Key+"`") || !strings.Contains(issue.RecurrenceComment, "Recurred in `prod`") {
		t.Errorf("recurrence = %q", issue.RecurrenceComment)
	}
}

func TestFormatFaultedJobIssueHonoursRepoAndLabelOverrides(t *testing.T) {
	issue := FormatFaultedJobIssue(faultedJob, []Log{errLog("boom")}, FormatOptions{Env: config.PreProd, JobKey: faultedJob.Key, Repo: "acme/repo", Labels: []string{"rpa"}})
	if issue.Repo != "acme/repo" || len(issue.Labels) != 1 || issue.Labels[0] != "rpa" ||
		!strings.Contains(issue.SearchQuery, "repo:acme/repo") || !strings.Contains(issue.Title, "(pre_prod)") {
		t.Fatalf("issue = %+v", issue)
	}
}

func TestFormatFaultedJobIssueFallsBackToTheJobStateWithoutErrorLogs(t *testing.T) {
	issue := FormatFaultedJobIssue(faultedJob, nil, FormatOptions{Env: config.Prod, JobKey: faultedJob.Key})
	if issue.Title != "[UiPath Fault] Job ended in state Faulted (prod)" || issue.FaultSignature != "job ended in state faulted" || !strings.Contains(issue.Body, "(no logs)") {
		t.Fatalf("issue = %+v", issue)
	}
}

func TestFormatFaultedJobIssueAddsTheQueueExceptionLine(t *testing.T) {
	ended := endedRow(`{"transactionState":"Ended","processingExceptionType":"BusinessException","processingExceptionReason":"missing required field"}`)
	issue := FormatFaultedJobIssue(faultedJob, []Log{errLog("Throw: Notification sync request failed with status: 422"), ended}, FormatOptions{Env: config.Prod, JobKey: faultedJob.Key})
	if !strings.Contains(issue.Body, "**Queue exception:** BusinessException — missing required field") {
		t.Fatalf("body = %s", issue.Body)
	}
	plain := FormatFaultedJobIssue(faultedJob, []Log{errLog("boom")}, FormatOptions{Env: config.Prod, JobKey: faultedJob.Key})
	if strings.Contains(plain.Body, "**Queue exception:**") {
		t.Error("no queue-exception line without RawMessage")
	}
}

func TestBuildFaultedJobIssueFoundWithNoLogsError(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/odata/Jobs") {
			respond(w, http.StatusOK, page(map[string]any{"Id": 1, "Key": "job-1", "State": "Faulted"}))
			return
		}
		okEmpty(w, r)
	})
	f.useConfig(t, nil)
	res, err := BuildFaultedJobIssue(t.Context(), config.Prod, "job-1", BuildOptions{})
	if err != nil || !res.Found || res.LogsError != "" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestBuildFaultedJobIssueDegradesALogsFailureToLogsError(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/odata/Jobs") {
			respond(w, http.StatusOK, page(map[string]any{"Id": 1, "Key": "job-1", "State": "Faulted"}))
			return
		}
		http.Error(w, "server error", http.StatusInternalServerError)
	})
	f.useConfig(t, nil)
	res, err := BuildFaultedJobIssue(t.Context(), config.Prod, "job-1", BuildOptions{})
	if err != nil || !res.Found || res.LogsError == "" || !strings.Contains(res.Body, "(no logs)") {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}
