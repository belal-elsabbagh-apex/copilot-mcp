package uipath

import (
	"fmt"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Pure UiPath -> orchestrator-core mappers (no I/O). UiPath's own vocabulary is the contract's
// reference vocabulary, so State/Status pass through verbatim; everything UiPath-specific lives
// under each core's Details.

// TerminalStates are the job states with nothing left to run or stop.
var TerminalStates = map[string]bool{"Successful": true, "Faulted": true, "Stopped": true}

// JobDetails is JobCore.Details for a UiPath job.
type JobDetails struct {
	NumericID *string `json:"numericId"` // the OData Id; StopJob addresses jobs by it
	RobotName string  `json:"robotName"` // "" when unassigned
	// Output is the parsed OutputArguments; present only on reads that fetch it (get_job). A
	// pointer so an explicit null is still emitted.
	Output *any `json:"output,omitempty"`
}

// ItemDetails is QueueItemCore.Details for a UiPath queue item.
type ItemDetails struct {
	RetryNumber       int64  `json:"retryNumber"`
	RobotName         string `json:"robotName"` // "" when unassigned or the read doesn't carry it
	QueueDefinitionID int64  `json:"queueDefinitionId"`
	MemberID          string `json:"memberId"` // specificContent.MemberID — the triage columns; "" when absent
	Member            string `json:"member"`   // specificContent.MemberFullName; "" when absent
}

// QueueDetails is QueueCore.Details.
type QueueDetails struct {
	Description  string `json:"description"`
	CreationTime string `json:"creationTime"`
}

// ProcessDetails is ProcessCore.Details.
type ProcessDetails struct {
	NumericID       string `json:"numericId"`
	IsLatestVersion bool   `json:"isLatestVersion"`
}

// TriggerDetails is TriggerCore.Details.
type TriggerDetails struct {
	ReleaseID                int64  `json:"releaseId"`
	ReleaseName              string `json:"releaseName"`
	QueueDefinitionName      string `json:"queueDefinitionName"`
	ItemsActivationThreshold int64  `json:"itemsActivationThreshold"`
	MaxJobsForActivation     int64  `json:"maxJobsForActivation"`
}

// LogDigest is the generic digest plus the queue transaction's own outcome.
type LogDigest struct {
	orchestrator.JobLogDigest
	TransactionOutcome *TransactionOutcome `json:"transactionOutcome"`
}

// toJobCore maps a job. The jobs read carries no fault (it comes from logs) or queue item: both
// stay nil here, and a caller that fetched logs overlays Fault. output is nil when the read didn't
// fetch OutputArguments; a non-nil pointer to a nil value is an explicit null.
func toJobCore(job Job, link string, output *any) orchestrator.JobCore {
	var duration *int64
	if ms, ok := shared.MsBetween(job.CreationTime, job.EndTime); ok {
		duration = &ms
	}
	details := JobDetails{RobotName: job.RobotName, Output: output}
	if job.ID != "" {
		details.NumericID = &job.ID
	}
	return orchestrator.JobCore{
		Orchestrator: orchestrator.UiPath,
		ID:           job.Key,
		State:        job.State,
		RawState:     job.State,
		Terminal:     TerminalStates[job.State],
		Process:      orchestrator.JobProcess{Name: job.ReleaseName, Version: shared.NonEmpty(job.ProcessVersion)},
		CreatedAt:    job.CreationTime,
		EndedAt:      shared.NonEmpty(job.EndTime),
		DurationMs:   duration,
		Link:         link,
		Details:      details,
	}
}

var exceptionTypes = map[string]string{
	"businessexception":    "business",
	"business":             "business",
	"applicationexception": "application",
	"application":          "application",
	"system":               "application",
}

// exceptionTypeOf maps UiPath's ProcessingExceptionType ("BusinessException" |
// "ApplicationException") onto the contract's two-valued classification; "" (none) and unknown
// values are nil.
func exceptionTypeOf(processingExceptionType string) *string {
	if t, ok := exceptionTypes[strings.ToLower(strings.TrimSpace(processingExceptionType))]; ok {
		return &t
	}
	return nil
}

func orderUIDOf(sc map[string]any) *string {
	if s, ok := sc["orderUid"].(string); ok && s != "" {
		return &s
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// toQueueItemCoreFromItem maps a queue item. A nil specificContent is the PHI-light default
// projection; pull passes the guarded payload.
func toQueueItemCoreFromItem(item QueueItem, queueName string, specificContent map[string]any) orchestrator.QueueItemCore {
	return orchestrator.QueueItemCore{
		Orchestrator:    orchestrator.UiPath,
		QueueID:         fmt.Sprint(item.QueueDefinitionID),
		QueueName:       queueName,
		ItemID:          fmt.Sprint(item.ID),
		Reference:       item.Reference,
		OrderUID:        orderUIDOf(item.SpecificContent),
		Status:          item.Status,
		RawStatus:       item.Status,
		ExceptionType:   nil, // the QueueItems read doesn't select ProcessingExceptionType
		CreatedAt:       item.CreationTime,
		Jobs:            []orchestrator.JobAttempt{},
		SpecificContent: specificContent,
		Details: ItemDetails{
			RetryNumber:       item.RetryNumber,
			RobotName:         item.RobotName,
			QueueDefinitionID: item.QueueDefinitionID,
			MemberID:          str(item.SpecificContent["MemberID"]),
			Member:            str(item.SpecificContent["MemberFullName"]),
		},
	}
}

// toQueueItemCoreFromMatch maps the order-correlation read: it carries the job key and exception
// type, no robot.
func toQueueItemCoreFromMatch(m QueueItemMatch, queueName string) orchestrator.QueueItemCore {
	jobs := []orchestrator.JobAttempt{}
	if m.ExecutorJobKey != "" {
		jobs = append(jobs, orchestrator.JobAttempt{Orchestrator: orchestrator.UiPath, ID: m.ExecutorJobKey})
	}
	return orchestrator.QueueItemCore{
		Orchestrator:    orchestrator.UiPath,
		QueueID:         fmt.Sprint(m.QueueDefinitionID),
		QueueName:       queueName,
		ItemID:          fmt.Sprint(m.ID),
		Reference:       m.Reference,
		OrderUID:        orderUIDOf(m.SpecificContent),
		Status:          m.Status,
		RawStatus:       m.Status,
		ExceptionType:   exceptionTypeOf(m.ProcessingExceptionType),
		CreatedAt:       m.CreationTime,
		Jobs:            jobs,
		SpecificContent: m.SpecificContent,
		Details: ItemDetails{
			RetryNumber:       m.RetryNumber,
			QueueDefinitionID: m.QueueDefinitionID,
			MemberID:          str(m.SpecificContent["MemberID"]),
			Member:            str(m.SpecificContent["MemberFullName"]),
		},
	}
}

// toQueueCore maps a queue. Queue definitions and releases have no active/disabled flag in UiPath,
// so both read as active.
func toQueueCore(q QueueDefinition) orchestrator.QueueCore {
	return orchestrator.QueueCore{
		Orchestrator: orchestrator.UiPath,
		ID:           fmt.Sprint(q.ID),
		Name:         q.Name,
		Active:       true,
		Details:      QueueDetails{Description: q.Description, CreationTime: q.CreationTime},
	}
}

func toProcessCore(r Release) orchestrator.ProcessCore {
	return orchestrator.ProcessCore{
		Orchestrator: orchestrator.UiPath,
		ID:           r.Key,
		Name:         r.Name,
		Package:      shared.NonEmpty(r.ProcessKey),
		Version:      shared.NonEmpty(r.ProcessVersion),
		Active:       true,
		Details:      ProcessDetails{NumericID: fmt.Sprint(r.ID), IsLatestVersion: r.IsLatestVersion},
	}
}

// toTriggerCore maps a trigger: a queue trigger carries a non-zero QueueDefinitionID; time
// triggers have 0 sentinels.
func toTriggerCore(t Trigger) orchestrator.TriggerCore {
	kind, queueID := orchestrator.TriggerTime, (*string)(nil)
	if t.QueueDefinitionID != 0 {
		kind, queueID = orchestrator.TriggerQueue, new(fmt.Sprint(t.QueueDefinitionID))
	}
	return orchestrator.TriggerCore{
		Orchestrator: orchestrator.UiPath,
		ID:           fmt.Sprint(t.ID),
		Name:         t.Name,
		Kind:         kind,
		ProcessID:    shared.NonEmpty(t.ReleaseKey),
		QueueID:      queueID,
		Enabled:      t.Enabled,
		Cron:         nil, // the ProcessSchedules read doesn't select the cron fields
		Details: TriggerDetails{
			ReleaseID:                t.ReleaseID,
			ReleaseName:              t.ReleaseName,
			QueueDefinitionName:      t.QueueDefinitionName,
			ItemsActivationThreshold: t.ItemsActivationThreshold,
			MaxJobsForActivation:     t.MaxJobsForActivation,
		},
	}
}

// uipathFault builds a job's fault: Class is the exception type parsed from the headline error;
// the signature is the same NormalizeError dedupe key build_faulted_job_issue uses.
func uipathFault(job Job, logs []orchestrator.JobLog) *orchestrator.JobFault {
	message := topError(job, logs)
	return &orchestrator.JobFault{
		Message:   new(orchestrator.Truncate(message)),
		Signature: new(orchestrator.NormalizeError(message)),
		Class:     shared.NonEmpty(exceptionTypeRE.FindString(message)),
	}
}

func toLogDigest(logs []Log) LogDigest {
	return LogDigest{JobLogDigest: orchestrator.DigestLogs(jobLogs(logs)), TransactionOutcome: extractTransactionOutcome(logs)}
}
