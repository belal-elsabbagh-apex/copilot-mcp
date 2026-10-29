package apex

import (
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

const faultSig = "apex:business_failure: portal rejected order <n> at <ts>"

func faultJobFixture(mod func(*orchestrator.JobCore)) orchestrator.JobCore {
	msg, sig, class := "Portal rejected order 998877 at 2026-10-11T10:00:09Z", faultSig, "business_failure"
	started, ended := "2026-10-11T10:00:00Z", "2026-10-11T10:01:00Z"
	job := orchestrator.JobCore{
		Orchestrator: orchestrator.Apex, ID: "job-2", State: "Faulted", RawState: "failed", Terminal: true,
		Process:   orchestrator.JobProcess{ID: sp("wf-1"), Name: "optum-auth-submit", Version: sp("1.4.2")},
		QueueItem: &orchestrator.JobQueueItem{QueueID: "q-1", ItemID: "item-1", Reference: "0123456789"},
		CreatedAt: "2026-10-11T09:59:00Z", StartedAt: &started, EndedAt: &ended, Attempt: new(2), RetryOfJobID: sp("job-1"),
		Fault:   &orchestrator.JobFault{Message: &msg, Signature: &sig, Class: &class},
		Link:    "https://apex.example.com/jobs/job-2",
		Details: JobDetails{ErrorClass: sp("permanent"), Outcome: sp("business_failure"), ExitCode: new(21)},
	}
	if mod != nil {
		mod(&job)
	}
	return job
}

func attemptOf(id, state string, neverStarted bool) orchestrator.JobAttempt {
	return orchestrator.JobAttempt{Orchestrator: orchestrator.Apex, ID: id, State: sp(state), NeverStarted: &neverStarted}
}

func faultOpts() faultOptions {
	return faultOptions{Env: config.Prod, Attempts: []orchestrator.JobAttempt{attemptOf("job-2", "Faulted", false), attemptOf("job-1", "Stopped", false)}}
}

func logOf(level, msg string) orchestrator.JobLog {
	return orchestrator.JobLog{Level: level, Message: msg, TimeStamp: "2026-10-11T10:00:00Z"}
}

func contains(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("missing %q in:\n%s", n, haystack)
		}
	}
}

func TestFaultIssueTitlesByClassAndFirstLineAndCarriesTheSignatureEverywhere(t *testing.T) {
	issue := formatFaultIssue(faultJobFixture(nil), nil, faultOpts())
	if want := "[Apex Fault] business_failure: Portal rejected order 998877 at 2026-10-11T10:00:09Z (prod)"; issue.Title != want {
		t.Errorf("title = %q, want %q", issue.Title, want)
	}
	if issue.Repo != orchestrator.DefaultRepo || !slices.Equal(issue.Labels, []string{"apex-fault", "bug"}) || !slices.Equal(issue.Labels, DefaultLabels) {
		t.Errorf("repo/labels = %s %v", issue.Repo, issue.Labels)
	}
	if issue.FaultSignature != faultSig || issue.SearchQuery != orchestrator.FaultSearchQuery(orchestrator.DefaultRepo, faultSig) {
		t.Errorf("signature/search = %q / %q", issue.FaultSignature, issue.SearchQuery)
	}
	contains(t, issue.Body, orchestrator.SignatureMarker(faultSig), "optum-auth-submit@1.4.2", "https://apex.example.com/jobs/job-2")
	contains(t, issue.RecurrenceComment, "job-2", "prod")
}

func TestFaultIssueWithANilClassIsTitledUnclassified(t *testing.T) {
	job := faultJobFixture(func(j *orchestrator.JobCore) {
		j.Fault = &orchestrator.JobFault{Message: sp("boom"), Signature: sp("apex:unclassified: boom")}
	})
	if got := formatFaultIssue(job, nil, faultOpts()).Title; got != "[Apex Fault] unclassified: boom (prod)" {
		t.Errorf("title = %q", got)
	}
}

func TestFaultIssueListsTheAttemptChainOldestFirstAndMarksTheFaultedJob(t *testing.T) {
	body := formatFaultIssue(faultJobFixture(nil), nil, faultOpts()).Body
	contains(t, body, "1. `job-1` — Stopped", "2. `job-2` — Faulted (this job)")
	if strings.Index(body, "1. `job-1`") > strings.Index(body, "2. `job-2`") {
		t.Error("attempts are not listed oldest first")
	}
}

func TestFaultIssueFlagsANeverStartedAttempt(t *testing.T) {
	opts := faultOpts()
	opts.Attempts = []orchestrator.JobAttempt{attemptOf("job-2", "Faulted", true)}
	contains(t, formatFaultIssue(faultJobFixture(nil), nil, opts).Body, "`job-2` — Faulted, never started (this job)")
}

func TestFaultIssueShowsExitCodeZeroAndOmitsNullClassification(t *testing.T) {
	zero := formatFaultIssue(faultJobFixture(func(j *orchestrator.JobCore) { j.Details = JobDetails{ExitCode: new(0)} }), nil, faultOpts())
	contains(t, zero.Body, "Exit code")
	if strings.Contains(zero.Body, "Outcome") || strings.Contains(zero.Body, "Error class") {
		t.Errorf("body lists null outcome/class:\n%s", zero.Body)
	}
	none := formatFaultIssue(faultJobFixture(func(j *orchestrator.JobCore) { j.Details = JobDetails{} }), nil, faultOpts())
	if strings.Contains(none.Body, "Exit code") {
		t.Errorf("body lists a null exit code:\n%s", none.Body)
	}
}

func TestFaultIssueWithANilProcessVersionPrintsTheBareName(t *testing.T) {
	job := faultJobFixture(func(j *orchestrator.JobCore) { j.Process.Version = nil })
	body := formatFaultIssue(job, nil, faultOpts()).Body
	contains(t, body, "optum-auth-submit")
	if strings.Contains(body, "optum-auth-submit@") {
		t.Error("body carries an @ for a null version")
	}
}

func TestFaultIssueCustomRepoAndLabelsWinAndEmptyLabelsFallBack(t *testing.T) {
	opts := faultOpts()
	opts.Repo, opts.Labels = "acme/other", []string{"triage"}
	got := formatFaultIssue(faultJobFixture(nil), nil, opts)
	if got.Repo != "acme/other" || !slices.Equal(got.Labels, []string{"triage"}) || got.SearchQuery != orchestrator.FaultSearchQuery("acme/other", faultSig) {
		t.Errorf("custom = %+v", got)
	}
	opts.Labels = []string{}
	if got := formatFaultIssue(faultJobFixture(nil), nil, opts).Labels; !slices.Equal(got, DefaultLabels) {
		t.Errorf("labels = %v, want the default", got)
	}
}

func TestFaultSignatureSearchQueryAndMarkerAreStableAcrossJobsOfTheSameFault(t *testing.T) {
	a := formatFaultIssue(faultJobFixture(nil), nil, faultOpts())
	b := formatFaultIssue(faultJobFixture(func(j *orchestrator.JobCore) {
		j.ID, j.CreatedAt, j.Link = "job-9", "2026-10-12T01:02:03Z", "https://apex.example.com/jobs/job-9"
		j.Fault.Message = sp("Portal rejected order 112233 at 2026-10-12T01:02:09Z")
	}), nil, faultOptions{Env: config.Prod})
	if b.FaultSignature != a.FaultSignature || b.SearchQuery != a.SearchQuery {
		t.Errorf("signature/search differ: %q/%q vs %q/%q", a.FaultSignature, a.SearchQuery, b.FaultSignature, b.SearchQuery)
	}
	contains(t, b.Body, orchestrator.SignatureMarker(faultSig))
	if b.Body == a.Body || b.RecurrenceComment == a.RecurrenceComment {
		t.Error("body/recurrence comment ignore the job identity")
	}
}

func TestFaultLogTailKeepsOnlyWarnErrorRowsAndPastTheCapTheNewestOnes(t *testing.T) {
	rows := []orchestrator.JobLog{
		logOf("INFO", "login ok"), logOf("ERROR", "first-"+strings.Repeat("x", 5000)),
		logOf("warning", "retrying selector"), logOf("ERROR", "last-row"),
	}
	body := formatFaultIssue(faultJobFixture(nil), rows, faultOpts()).Body
	contains(t, body, "retrying selector", "last-row")
	for _, absent := range []string{"login ok", "first-"} {
		if strings.Contains(body, absent) {
			t.Errorf("body contains %q, want it dropped", absent)
		}
	}
}

func TestFaultLogTailWithOnlyInfoRowsIsEmptyNotACrash(t *testing.T) {
	body := formatFaultIssue(faultJobFixture(nil), []orchestrator.JobLog{logOf("INFO", "only info")}, faultOpts()).Body
	if strings.Contains(body, "only info") {
		t.Error("an INFO row reached the tail")
	}
	contains(t, body, "(no warn/error log rows)")
}
