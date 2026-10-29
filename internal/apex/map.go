package apex

// Pure Apex -> backend-neutral core mappers, the dashboard label maps (verbatim from the dashboard
// bundle) and the Apex fault. No I/O: the backend fetches, these shape.

import (
	"encoding/json"
	"math"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

// Label is one wire status and its dashboard label.
type Label struct{ Wire, Label string }

// JobLabels are the dashboard bundle's job labels, verbatim, in dashboard order.
var JobLabels = []Label{
	{"queued", "Pending"}, {"claimed", "Claimed"}, {"running", "Running"}, {"completed", "Successful"},
	{"failed", "Faulted"}, {"cancelled", "Stopped"}, {"cancelling", "Stopping"}, {"lost", "Lost"},
	{"timed_out", "Timed Out"}, {"suspended", "Suspended"}, {"resumed", "Resumed"},
}

// ItemLabels are the queue-item labels. They use UiPath's spelling (the dashboard itself shows
// "In Progress") because orchestrator.QueueItemStatus is UiPath's vocabulary.
var ItemLabels = []Label{
	{"new", "New"}, {"in_progress", "InProgress"}, {"successful", "Successful"}, {"failed", "Failed"},
	{"retried", "Retried"}, {"abandoned", "Abandoned"}, {"deleted", "Deleted"},
}

// TerminalStatuses are the job statuses after which nothing more happens.
var TerminalStatuses = []string{"completed", "failed", "cancelled", "lost", "timed_out"}

var (
	jobLabelOf  = labelMap(JobLabels)
	itemLabelOf = labelMap(ItemLabels)
	itemWire    = map[orchestrator.QueueItemStatus]string{
		orchestrator.StatusNew: "new", orchestrator.StatusInProgress: "in_progress",
		orchestrator.StatusSuccessful: "successful", orchestrator.StatusFailed: "failed",
		orchestrator.StatusRetried: "retried", orchestrator.StatusAbandoned: "abandoned",
		orchestrator.StatusDeleted: "deleted",
	}
)

func labelMap(ls []Label) map[string]string {
	m := make(map[string]string, len(ls))
	for _, l := range ls {
		m[l.Wire] = l.Label
	}
	return m
}

// IsTerminal reports whether a job status is terminal.
func IsTerminal(status string) bool {
	for _, s := range TerminalStatuses {
		if s == status {
			return true
		}
	}
	return false
}

func toItemWireStatus(s orchestrator.QueueItemStatus) string { return itemWire[s] }

func labelFor(m map[string]string, status string) string {
	if l, ok := m[status]; ok {
		return l
	}
	return status
}

func jobLabel(status string) string  { return labelFor(jobLabelOf, status) }
func itemLabel(status string) string { return labelFor(itemLabelOf, status) }

// ---- jobs ---------------------------------------------------------------------

// JobDetails is JobCore.Details. Null on a list row means "not fetched" (list rows carry no
// outcome/exit code), never "absent".
type JobDetails struct {
	ErrorClass     *string `json:"errorClass"`
	Outcome        *string `json:"outcome"`
	OutcomeLabel   *string `json:"outcomeLabel"`
	ExitCode       *int    `json:"exitCode"`
	NeedsReview    *bool   `json:"needsReview"`
	CallbackStatus *string `json:"callbackStatus"`
	CredentialName *string `json:"credentialName"`
	Source         *string `json:"source"`
	QueueName      *string `json:"queueName"`
	WorkerID       *string `json:"workerId"`
	FolderID       string  `json:"folderId"`
	WorkflowID     string  `json:"workflowId"`
	// NeverStarted: terminal and never claimed by a worker (e.g. missing_credential).
	NeverStarted            bool `json:"neverStarted"`
	EffectiveTimeoutSeconds *int `json:"effectiveTimeoutSeconds"`
	// Result is result_payload and Input is input_payload minus token/callbackContext (detail only).
	Result json.RawMessage `json:"result,omitempty"`
	Input  *map[string]any `json:"input,omitempty"`
}

// RunSummary is the run's own verdict inside an Apex log digest.
type RunSummary struct {
	ExitCode      *int           `json:"exitCode"`
	Outcome       *string        `json:"outcome"`
	ErrorClass    *string        `json:"errorClass"`
	TimedOut      bool           `json:"timedOut"`
	Cancelled     bool           `json:"cancelled"`
	AdapterResult *AdapterResult `json:"adapterResult"`
}

// LogDigest is the shared digest plus the run summary.
type LogDigest struct {
	orchestrator.JobLogDigest
	Run RunSummary `json:"run"`
}

var secretInputKeys = map[string]bool{"token": true, "callbackContext": true}

func withoutSecrets(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for k, v := range input {
		if !secretInputKeys[k] {
			out[k] = v
		}
	}
	return out
}

// apexFault: message = error_message ?? outcome_label; class = outcome ?? error_class. nil for a
// completed or still-running job. A list row (detail == nil) has only error_class, so it carries
// the class with no message/signature (get_job fetches the detail that has them).
func apexFault(row JobRow, detail *JobDetail) *orchestrator.JobFault {
	if row.Status == "completed" {
		return nil
	}
	if detail == nil {
		if row.ErrorClass == nil || *row.ErrorClass == "" {
			return nil
		}
		return &orchestrator.JobFault{Class: row.ErrorClass}
	}
	if !IsTerminal(row.Status) {
		return nil
	}
	var message string
	switch {
	case detail.ErrorMessage != nil:
		message = *detail.ErrorMessage
	case detail.OutcomeLabel != nil:
		message = *detail.OutcomeLabel
	default:
		message = "Job ended in status " + row.Status
	}
	class := detail.Outcome
	if class == nil {
		class = row.ErrorClass
	}
	label := "unclassified"
	if class != nil {
		label = *class
	}
	return &orchestrator.JobFault{
		Message:   &message,
		Signature: new("apex:" + orchestrator.NormalizeError(label+": "+message)),
		Class:     class,
	}
}

func neverStarted(j JobRow) bool { return IsTerminal(j.Status) && j.StartedAt == nil }

func toJobCore(row JobRow, detail *JobDetail, link string) orchestrator.JobCore {
	core := orchestrator.JobCore{
		Orchestrator: orchestrator.Apex,
		ID:           row.ID,
		State:        jobLabel(row.Status),
		RawState:     row.Status,
		Terminal:     IsTerminal(row.Status),
		Process:      orchestrator.JobProcess{ID: new(row.WorkflowID), Name: row.WorkflowName, Version: row.WorkflowVersion},
		CreatedAt:    row.CreatedAt,
		StartedAt:    row.StartedAt,
		EndedAt:      row.CompletedAt,
		Attempt:      new(row.AttemptNumber),
		RetryOfJobID: row.RetryOfJobID,
		Fault:        apexFault(row, detail),
		Link:         link,
	}
	if row.QueueItemID != nil && *row.QueueItemID != "" && row.QueueID != nil && *row.QueueID != "" {
		ref := ""
		if row.QueueItemRef != nil {
			ref = *row.QueueItemRef
		}
		core.QueueItem = &orchestrator.JobQueueItem{QueueID: *row.QueueID, ItemID: *row.QueueItemID, Reference: ref}
	}
	if row.DurationSeconds != nil {
		core.DurationMs = new(int64(math.Round(*row.DurationSeconds * 1000)))
	}
	d := JobDetails{
		ErrorClass: row.ErrorClass, CallbackStatus: row.CallbackStatus, CredentialName: row.CredentialName,
		Source: row.Source, QueueName: row.QueueName, WorkerID: row.WorkerID, FolderID: row.FolderID,
		WorkflowID: row.WorkflowID, NeverStarted: neverStarted(row),
	}
	if detail != nil {
		d.Outcome, d.OutcomeLabel, d.ExitCode = detail.Outcome, detail.OutcomeLabel, detail.ExitCode
		d.NeedsReview = &detail.NeedsReview
		d.EffectiveTimeoutSeconds = detail.EffectiveTimeoutSeconds
		d.Result = detail.ResultPayload
		if detail.InputPayload != nil {
			d.Input = new(withoutSecrets(detail.InputPayload))
		}
	}
	core.Details = d
	return core
}

func toJobAttempt(row JobRow) orchestrator.JobAttempt {
	return orchestrator.JobAttempt{
		Orchestrator: orchestrator.Apex, ID: row.ID, State: new(jobLabel(row.Status)), NeverStarted: new(neverStarted(row)),
	}
}

// toRunSummary: the footer is the run's own verdict; the detail fills in when the log has none.
func toRunSummary(detail JobDetail, parsed ParsedJobLog) RunSummary {
	s := RunSummary{
		ExitCode: detail.ExitCode, Outcome: detail.Outcome, ErrorClass: detail.ErrorClass,
		TimedOut: detail.Status == "timed_out", Cancelled: detail.Status == "cancelled",
		AdapterResult: parsed.AdapterResult,
	}
	if f := parsed.Footer; f != nil {
		s.ExitCode, s.TimedOut, s.Cancelled = f.ExitCode, f.TimedOut, f.Cancelled
	}
	return s
}
