package uipath

import (
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Parsing for RobotLogs.RawMessage: the full JSON payload UiPath's NLog-based robot logger emits
// per log row, of which `Message` (the field the rest of this server already reads) is just one
// key. RawMessage carries everything else the robot logged for that row — but only the fields
// relevant to that specific log event, and only when the caller opted into fetching it
// (LogFilter.IncludeRawFields: benchmarked live at ~4-6x the byte size of every already-selected
// row — a bigger cost than a mere doubling — so it's never fetched by default).
//
// Ground-truthed live (2026-09-07, this org's Orchestrator, both envs, across every
// RPAPlaywright/AuthSubmit process) rather than assumed from docs:
//   - transactionId/queueName/processingExceptionType/processingExceptionReason/
//     transactionExecutionTime/queueItemPriority/queueItemReviewStatus appear ONLY on the
//     "Transaction Started"/"Transaction Ended" log rows — an ordinary Info line has none of them.
//   - totalExecutionTimeInSeconds/totalExecutionTime appear ONLY on the final
//     "<process name> execution ended" row.
//   - transactionStatus is present but USELESS: every job observed (Successful and Faulted alike,
//     across every distinct process) logs the *unresolved* .NET binding literally, e.g.
//     `System.Activities.InArgument`1[UiPath.Core.ProcessingStatus]`, never the actual enum value
//     — a platform/activity-package quirk, not specific to one automation. This module
//     deliberately does NOT surface it; use processingExceptionType instead — it IS populated
//     correctly.
//   - activityInfo (which Studio activity was executing/faulted) only appears when the process has
//     Debugging-level logging enabled — not observed on any job in this org's live traffic, so
//     treat it as opportunistic, not load-bearing.
//   - Custom fields added via Studio's "Add Log Fields" activity show up as ordinary top-level
//     RawMessage keys (e.g. this org's `healingAgentConfig: {...}`), attached only to the specific
//     log row(s) emitted after the activity ran, under whatever name the process author chose —
//     there is no way to know them ahead of time, so they are collected into `custom` verbatim
//     instead of enumerated.
//   - RawMessage is not guaranteed to be valid JSON (UiPath's own NLog target format embeds it
//     inside "${timestamp} ${loglevel} ${message}" in some configurations, per community reports)
//     — parse defensively, never assume success.

// knownRawKeys are the keys surfaced as typed fields below; anything else lands in `custom`
// unchanged. `transactionStatus` is intentionally excluded — see above.
var knownRawKeys = map[string]bool{
	"message": true, "level": true, "logType": true, "timeStamp": true, "fingerprint": true,
	"windowsIdentity": true, "machineName": true, "fileName": true, "jobId": true, "robotName": true,
	"machineId": true, "userKey": true, "processName": true, "processVersion": true,
	"organizationUnitId": true, "initiatedBy": true, "businessOperationId": true,
	"transactionId": true, "transactionState": true, "transactionStatus": true, "queueName": true,
	"processingExceptionType": true, "processingExceptionReason": true,
	"transactionExecutionTime": true, "queueItemPriority": true, "queueItemReviewStatus": true,
	"totalExecutionTimeInSeconds": true, "totalExecutionTime": true, "activityInfo": true,
}

// RawMessageFields are the parsed fields of one RawMessage. Every nullable field marshals null
// when absent.
type RawMessageFields struct {
	TransactionID *string `json:"transactionId"`
	// TransactionState is "Started" | "Ended" — a phase marker, NOT an outcome.
	TransactionState *string `json:"transactionState"`
	QueueName        *string `json:"queueName"`
	// ProcessingExceptionType is e.g. "BusinessException" — the queue's own retry classification.
	ProcessingExceptionType     *string  `json:"processingExceptionType"`
	ProcessingExceptionReason   *string  `json:"processingExceptionReason"`
	TransactionExecutionTimeSec *float64 `json:"transactionExecutionTimeSec"`
	TotalExecutionTimeInSeconds *float64 `json:"totalExecutionTimeInSeconds"`
	QueueItemPriority           *string  `json:"queueItemPriority"`
	// QueueItemReviewStatus is as logged at the time this row was written — a snapshot, not live
	// queue-item state. Re-check via find_order_queue_items for the current value.
	QueueItemReviewStatus *string `json:"queueItemReviewStatus"`
	BusinessOperationID   *string `json:"businessOperationId"`
	// ActivityInfo is Debugging-log only; rarely present — see above. Its shape is intentionally
	// left unvalidated since it has never been observed live here.
	ActivityInfo any `json:"activityInfo"`
	// Custom is any RawMessage key outside the known schema above — Add Log Fields activity
	// output, verbatim, under whatever name the process author chose.
	Custom map[string]any `json:"custom"`
}

func number(v any) *float64 {
	if n, ok := v.(float64); ok {
		return &n
	}
	return nil
}

// parseRawMessage parses one log row's RawMessage. It returns nil when the field is absent, not
// an object, or not valid JSON — all three are normal (most rows carry none of this, and
// RawMessage isn't guaranteed parseable), never an error condition.
func parseRawMessage(l Log) *RawMessageFields {
	if l.RawMessage == "" {
		return nil
	}
	parsed, ok := shared.SafeJSONParse(l.RawMessage).(map[string]any)
	if !ok {
		return nil
	}
	custom := map[string]any{}
	for k, v := range parsed {
		if !knownRawKeys[k] {
			custom[k] = v
		}
	}
	return &RawMessageFields{
		TransactionID:               shared.NonEmpty(shared.Str(parsed, "transactionId")),
		TransactionState:            shared.NonEmpty(shared.Str(parsed, "transactionState")),
		QueueName:                   shared.NonEmpty(shared.Str(parsed, "queueName")),
		ProcessingExceptionType:     shared.NonEmpty(shared.Str(parsed, "processingExceptionType")),
		ProcessingExceptionReason:   shared.NonEmpty(shared.Str(parsed, "processingExceptionReason")),
		TransactionExecutionTimeSec: number(parsed["transactionExecutionTime"]),
		TotalExecutionTimeInSeconds: number(parsed["totalExecutionTimeInSeconds"]),
		QueueItemPriority:           shared.NonEmpty(shared.Str(parsed, "queueItemPriority")),
		QueueItemReviewStatus:       shared.NonEmpty(shared.Str(parsed, "queueItemReviewStatus")),
		BusinessOperationID:         shared.NonEmpty(shared.Str(parsed, "businessOperationId")),
		ActivityInfo:                parsed["activityInfo"],
		Custom:                      custom,
	}
}
