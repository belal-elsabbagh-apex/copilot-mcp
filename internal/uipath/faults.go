package uipath

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// build_faulted_job_issue: turn a faulted UiPath job into a ready-to-post GitHub issue payload —
// WITHOUT touching GitHub. This server holds no GitHub credentials; the `report-faulted-jobs`
// prompt hands the payload to the host's GitHub MCP server, which creates the issue (or comments
// on a matching one). Keeping the formatting here makes it consistent and lets the pure formatter
// be unit-tested.

// DefaultLabels are the labels of a filed fault issue when the caller gives none.
var DefaultLabels = []string{"uipath-fault", "bug"}

// logTailCap bounds the log tail quoted in an issue body (UTF-16 code units, as JS slice).
const logTailCap = 4000

// FormatOptions are the inputs of FormatFaultedJobIssue; Repo "" = orchestrator.DefaultRepo,
// empty Labels = DefaultLabels, DeepLink "" = none.
type FormatOptions struct {
	Env      config.Env
	JobKey   string
	Repo     string
	Labels   []string
	DeepLink string
}

func isLevel(l orchestrator.JobLog, level string) bool { return strings.ToLower(l.Level) == level }

// topError is the headline error: the first Error-level log, else the last non-empty log line,
// else a synthetic line from the job State.
func topError(job Job, logs []orchestrator.JobLog) string {
	for _, l := range logs {
		if isLevel(l, "error") && strings.TrimSpace(l.Message) != "" {
			return strings.TrimSpace(l.Message)
		}
	}
	for _, l := range slices.Backward(logs) {
		if strings.TrimSpace(l.Message) != "" {
			return strings.TrimSpace(l.Message)
		}
	}
	return "Job ended in state " + cmpState(job.State)
}

func cmpState(state string) string {
	if state == "" {
		return "Faulted"
	}
	return state
}

// exceptionTypeRE matches a dotted .NET-style type ending in Exception/Error, e.g.
// System.NullReferenceException.
var exceptionTypeRE = regexp.MustCompile(`\b(?:[A-Za-z_]\w*\.)*[A-Z]\w*(?:Exception|Error)\b`)

// TransactionOutcome is the Transaction Ended row's structured fields (see rawmessage.go) — the
// queue's OWN record of how this job's transaction concluded, independent of (and more reliable
// for exception classification than) a regex guess over free text. nil whenever RawMessage wasn't
// fetched, or the job never reached a queue transaction end (faulted before starting one, or has
// no queue item at all).
type TransactionOutcome struct {
	TransactionID               *string  `json:"transactionId"`
	QueueName                   *string  `json:"queueName"`
	ProcessingExceptionType     *string  `json:"processingExceptionType"`
	ProcessingExceptionReason   *string  `json:"processingExceptionReason"`
	TransactionExecutionTimeSec *float64 `json:"transactionExecutionTimeSec"`
	QueueItemPriority           *string  `json:"queueItemPriority"`
	// QueueItemReviewStatus is as logged at the time — a snapshot, not live queue-item state.
	QueueItemReviewStatus *string `json:"queueItemReviewStatus"`
}

// extractTransactionOutcome reads the queue transaction's own outcome fields off the
// "Transaction Ended" row (the last one, if a job somehow logs more than one). It reverse-scans
// like topError — the terminal transaction is what matters, not the first.
func extractTransactionOutcome(logs []Log) *TransactionOutcome {
	for _, l := range slices.Backward(logs) {
		raw := parseRawMessage(l)
		if raw == nil || raw.TransactionState == nil || *raw.TransactionState != "Ended" {
			continue
		}
		return &TransactionOutcome{
			TransactionID:               raw.TransactionID,
			QueueName:                   raw.QueueName,
			ProcessingExceptionType:     raw.ProcessingExceptionType,
			ProcessingExceptionReason:   raw.ProcessingExceptionReason,
			TransactionExecutionTimeSec: raw.TransactionExecutionTimeSec,
			QueueItemPriority:           raw.QueueItemPriority,
			QueueItemReviewStatus:       raw.QueueItemReviewStatus,
		}
	}
	return nil
}

func jobLogs(logs []Log) []orchestrator.JobLog {
	out := make([]orchestrator.JobLog, len(logs))
	for i, l := range logs {
		out[i] = l.JobLog
	}
	return out
}

func fmtLog(l orchestrator.JobLog) string {
	return strings.TrimSpace(fmt.Sprintf("%s [%s] %s", l.TimeStamp, l.Level, l.Message))
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// FormatFaultedJobIssue is pure: it builds the GitHub issue payload for a faulted job from its
// detail + logs. It does not call GitHub or UiPath — fully determined by its inputs.
func FormatFaultedJobIssue(job Job, logs []Log, opts FormatOptions) orchestrator.FaultedJobIssue {
	repo := opts.Repo
	if repo == "" {
		repo = orchestrator.DefaultRepo
	}
	labels := opts.Labels
	if len(labels) == 0 {
		labels = DefaultLabels
	}
	jl := jobLogs(logs)
	errText := topError(job, jl)
	signature := orchestrator.NormalizeError(errText)
	title := fmt.Sprintf("[UiPath Fault] %s (%s)", orchestrator.FirstLine(errText), opts.Env)

	tail := slices.DeleteFunc(slices.Clone(jl), func(l orchestrator.JobLog) bool { return !isLevel(l, "error") })
	if len(tail) == 0 {
		tail = jl[max(0, len(jl)-10):]
	}
	lines := make([]string, len(tail))
	for i, l := range tail {
		lines[i] = fmtLog(l)
	}
	logTail := shared.SliceUTF16(strings.Join(lines, "\n"), logTailCap)

	// Only populated when the caller fetched RawMessage (BuildFaultedJobIssue always does); a
	// caller that didn't gets no queue-exception line, never a crash — extractTransactionOutcome
	// degrades to nil on plain rows.
	queueException := extractTransactionOutcome(logs)

	body := []string{
		fmt.Sprintf("**UiPath job faulted** in `%s`.", opts.Env),
		"",
		"- **State:** " + cmpState(job.State),
		"- **Job Id:** " + orUnknown(job.ID),
		fmt.Sprintf("- **Job Key:** `%s`", opts.JobKey),
		"- **Created:** " + orUnknown(job.CreationTime),
	}
	if job.EndTime != "" {
		body = append(body, "- **Ended:** "+job.EndTime)
	}
	if opts.DeepLink != "" {
		body = append(body, "- **Orchestrator:** "+opts.DeepLink)
	}
	if queueException != nil && queueException.ProcessingExceptionType != nil && *queueException.ProcessingExceptionType != "" {
		line := "- **Queue exception:** " + *queueException.ProcessingExceptionType
		if r := queueException.ProcessingExceptionReason; r != nil && *r != "" {
			line += " — " + *r
		}
		body = append(body, line)
	}
	if logTail == "" {
		logTail = "(no logs)"
	}
	body = append(body,
		"", "**Error**", "```", errText, "```", "",
		"<details><summary>Log tail</summary>", "", "```", logTail, "```", "", "</details>", "",
		"_Filed by copilot-mcp `build_faulted_job_issue`. Recurrences of this fault are added as comments below._",
		orchestrator.SignatureMarker(signature),
	)

	recurrence := []string{
		fmt.Sprintf("Recurred in `%s` — same fault signature.", opts.Env),
		"",
		fmt.Sprintf("- **Job Key:** `%s`", opts.JobKey),
		"- **Created:** " + orUnknown(job.CreationTime),
	}
	if opts.DeepLink != "" {
		recurrence = append(recurrence, "- **Orchestrator:** "+opts.DeepLink)
	}
	recurrence = append(recurrence, "", "```", orchestrator.FirstLine(errText), "```")

	return orchestrator.FaultedJobIssue{
		Repo:              repo,
		Title:             title,
		Body:              strings.Join(body, "\n"),
		Labels:            labels,
		FaultSignature:    signature,
		SearchQuery:       orchestrator.FaultSearchQuery(repo, signature),
		RecurrenceComment: strings.Join(recurrence, "\n"),
	}
}

// BuildOptions are the optional inputs of BuildFaultedJobIssue ("" / nil = unset).
type BuildOptions struct {
	Folder string
	Repo   string
	Labels []string
}

// BuildFaultedJobIssue fetches a faulted job (by Key) + its robot logs from UiPath and formats the
// issue payload. Found reports whether the job Key actually resolved (the payload is still
// produced from a synthetic stub if not, so the caller can decide).
func BuildFaultedJobIssue(ctx context.Context, env config.Env, jobKey string, opts BuildOptions) (orchestrator.FaultedJobIssueResult, error) {
	folder, err := ResolveFolder(env, opts.Folder)
	if err != nil {
		return orchestrator.FaultedJobIssueResult{}, err
	}
	// The job and its logs are independent reads (logs need only the key): fetch both at once. The
	// logs' cache gate waits on the job read, so a hit is only served on a live settled verdict.
	var (
		job  *Job
		jerr error
		wg   sync.WaitGroup
	)
	jobDone := make(chan struct{})
	wg.Go(func() {
		defer close(jobDone)
		job, jerr = cachedJobByKey(ctx, jobKey, folder)
	})
	verdict := func(ctx context.Context) (*Job, error) {
		select {
		case <-jobDone:
			return job, jerr
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Logs are a best-effort attachment — a transient logs-fetch failure must not discard an
	// otherwise-valid job lookup.
	logsResult, logsErr := cachedJobLogs(ctx, jobKey, folder, LogFilter{IncludeRawFields: true}, verdict)
	wg.Wait()
	if jerr != nil {
		return orchestrator.FaultedJobIssueResult{}, jerr
	}
	resolved := Job{Key: jobKey, State: "Faulted"}
	if job != nil {
		resolved = *job
	}
	issue := FormatFaultedJobIssue(resolved, logsResult.Logs, FormatOptions{
		Env: env, JobKey: jobKey, Repo: opts.Repo, Labels: opts.Labels, DeepLink: jobDeepLink(jobKey),
	})
	res := orchestrator.FaultedJobIssueResult{FaultedJobIssue: issue, Found: job != nil}
	if logsErr != nil {
		res.LogsError = logsErr.Error()
	}
	return res, nil
}
