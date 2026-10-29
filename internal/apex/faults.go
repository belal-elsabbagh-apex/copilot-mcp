package apex

// Apex fault -> ready-to-post GitHub issue payload. Pure: fully determined by its inputs, posts
// nothing (the report-faulted-jobs prompt hands the payload to the host's GitHub MCP server, which
// creates the issue or comments on a matching one).

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

// DefaultLabels are the issue labels used when the caller gives none.
var DefaultLabels = []string{"apex-fault", "bug"}

// faultOptions are the inputs of formatFaultIssue besides the job and its logs.
type faultOptions struct {
	Env      config.Env
	Attempts []orchestrator.JobAttempt // newest first, as /jobs lists them
	Repo     string                    // "" = orchestrator.DefaultRepo
	Labels   []string                  // empty = DefaultLabels
}

const logTailCap = 4000

var significantLevels = map[string]bool{"warn": true, "warning": true, "error": true, "critical": true, "fatal": true}

func fmtLog(l orchestrator.JobLog) string {
	return strings.TrimSpace(l.TimeStamp + " [" + l.Level + "] " + l.Message)
}

// logTail keeps the tail: the rows nearest the end of the run explain the fault.
// ponytail: cap counts runes where JS counted UTF-16 units.
func logTail(rows []orchestrator.JobLog) string {
	var lines []string
	for _, l := range rows {
		if significantLevels[strings.ToLower(l.Level)] {
			lines = append(lines, fmtLog(l))
		}
	}
	r := []rune(strings.Join(lines, "\n"))
	return string(r[max(0, len(r)-logTailCap):])
}

func attemptLine(a orchestrator.JobAttempt, i int, currentID string) string {
	state := "unknown"
	if a.State != nil {
		state = *a.State
	}
	line := fmt.Sprintf("  %d. `%s` — %s", i+1, a.ID, state)
	if a.NeverStarted != nil && *a.NeverStarted {
		line += ", never started"
	}
	if a.ID == currentID {
		line += " (this job)"
	}
	return line
}

// classification: outcome / class / exit code are only known from the job detail; nil means "not
// available", so it is left out rather than printed. Exit code 0 is a real value.
func classification(d JobDetails) []string {
	var out []string
	if d.Outcome != nil {
		out = append(out, "- **Outcome:** "+*d.Outcome)
	}
	if d.ErrorClass != nil {
		out = append(out, "- **Error class:** "+*d.ErrorClass)
	}
	if d.ExitCode != nil {
		out = append(out, "- **Exit code:** "+strconv.Itoa(*d.ExitCode))
	}
	return out
}

// formatFaultIssue builds the GitHub issue payload for a faulted Apex job from its core, its
// parsed log rows and its attempt chain. job.Fault must carry a message and signature (as
// apexFault sets for a terminal detail) and job.Details must be JobDetails.
func formatFaultIssue(job orchestrator.JobCore, rows []orchestrator.JobLog, opts faultOptions) orchestrator.FaultedJobIssue {
	repo := opts.Repo
	if repo == "" {
		repo = orchestrator.DefaultRepo
	}
	labels := opts.Labels
	if len(labels) == 0 {
		labels = DefaultLabels
	}
	message, signature := *job.Fault.Message, *job.Fault.Signature
	faultClass := "unclassified"
	if job.Fault.Class != nil {
		faultClass = *job.Fault.Class
	}
	process := job.Process.Name
	if job.Process.Version != nil && *job.Process.Version != "" {
		process += "@" + *job.Process.Version
	}
	details, _ := job.Details.(JobDetails)

	lines := []string{
		fmt.Sprintf("**Apex job faulted** in `%s`.", opts.Env),
		"",
		"- **Process:** " + process,
		fmt.Sprintf("- **Job:** `%s`", job.ID),
		fmt.Sprintf("- **State:** %s (`%s`)", job.State, job.RawState),
	}
	lines = append(lines, classification(details)...)
	lines = append(lines, "- **Created:** "+job.CreatedAt)
	if job.EndedAt != nil && *job.EndedAt != "" {
		lines = append(lines, "- **Ended:** "+*job.EndedAt)
	}
	if job.Link != "" {
		lines = append(lines, "- **Dashboard:** "+job.Link)
	}
	if len(opts.Attempts) > 0 {
		lines = append(lines, "- **Attempts (oldest first):**")
		for i, a := range slices.Backward(opts.Attempts) {
			lines = append(lines, attemptLine(a, len(opts.Attempts)-1-i, job.ID))
		}
	}
	tail := logTail(rows)
	if tail == "" {
		tail = "(no warn/error log rows)"
	}
	lines = append(lines,
		"", "**Error**", "```", message, "```", "",
		"<details><summary>Last warn/error log rows</summary>", "", "```", tail, "```", "", "</details>", "",
		"_Filed by copilot-mcp `build_faulted_job_issue`. Recurrences of this fault are added as comments below._",
		orchestrator.SignatureMarker(signature),
	)

	recurrence := []string{
		fmt.Sprintf("Recurred in `%s` — same fault signature.", opts.Env),
		"",
		fmt.Sprintf("- **Job:** `%s`", job.ID),
		"- **Created:** " + job.CreatedAt,
	}
	if job.Link != "" {
		recurrence = append(recurrence, "- **Dashboard:** "+job.Link)
	}
	recurrence = append(recurrence, "", "```", orchestrator.FirstLine(message), "```")

	return orchestrator.FaultedJobIssue{
		Repo:              repo,
		Title:             fmt.Sprintf("[Apex Fault] %s: %s (%s)", faultClass, orchestrator.FirstLine(message), opts.Env),
		Body:              strings.Join(lines, "\n"),
		Labels:            labels,
		FaultSignature:    signature,
		SearchQuery:       orchestrator.FaultSearchQuery(repo, signature),
		RecurrenceComment: strings.Join(recurrence, "\n"),
	}
}
