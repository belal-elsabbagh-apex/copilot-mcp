package apex

import (
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ---- queue items --------------------------------------------------------------

// LineageEntry is one retry-chain entry of an item detail.
type LineageEntry struct {
	ItemID        string  `json:"itemId"`
	Reference     string  `json:"reference"`
	Status        string  `json:"status"`
	ExceptionType *string `json:"exceptionType"`
	JobID         *string `json:"jobId"`
	JobStatus     *string `json:"jobStatus"`
	ErrorClass    *string `json:"errorClass"`
}

// ItemDetails is QueueItemCore.Details for a list row.
type ItemDetails struct {
	RetryCount   *int    `json:"retryCount"`
	ParentItemID *string `json:"parentItemId"`
	Priority     *int    `json:"priority"`
	MatchValue   *string `json:"matchValue"`
	MemberID     string  `json:"memberId"` // PHI-light triage projection, as UiPath's list_queue_items; "" when absent
	Member       string  `json:"member"`
}

// ItemDetailsFull is QueueItemCore.Details for an item detail (adds callback status + lineage).
type ItemDetailsFull struct {
	ItemDetails
	CallbackStatus *string        `json:"callbackStatus"`
	Lineage        []LineageEntry `json:"lineage"`
}

func toLineage(l LineageRow) LineageEntry {
	e := LineageEntry{ItemID: l.ItemID, Reference: l.Reference, Status: itemLabel(l.Status), ExceptionType: l.ExceptionType}
	if l.Job != nil {
		e.JobID, e.JobStatus, e.ErrorClass = new(l.Job.ID), new(jobLabel(l.Job.Status)), l.Job.ErrorClass
	}
	return e
}

// attemptsFromItem: the item names only its latest job; its state is unknown without fetching the job.
func attemptsFromItem(item QueueItem) []orchestrator.JobAttempt {
	if item.JobID == nil || *item.JobID == "" {
		return []orchestrator.JobAttempt{}
	}
	return []orchestrator.JobAttempt{{Orchestrator: orchestrator.Apex, ID: *item.JobID}}
}

func redactToken(sc map[string]any) map[string]any {
	out := make(map[string]any, len(sc))
	for k, v := range sc {
		if k == "token" {
			v = "[redacted]"
		}
		out[k] = v
	}
	return out
}

// toItemCore shapes a queue item. queueName is the owning queue's name — an item row carries only
// the queue id. detail is non-nil for a GET item (adds callbackStatus + lineage).
func toItemCore(item QueueItem, detail *QueueItemDetail, attempts []orchestrator.JobAttempt, includeContent bool, queueName string) orchestrator.QueueItemCore {
	core := orchestrator.QueueItemCore{
		Orchestrator: orchestrator.Apex,
		QueueID:      item.QueueID,
		QueueName:    queueName,
		ItemID:       item.ID,
		Reference:    item.Reference,
		Status:       itemLabel(item.Status),
		RawStatus:    item.Status,
		CreatedAt:    item.CreatedAt,
		EndedAt:      item.EndedAt,
		Jobs:         attempts,
	}
	if uid, ok := item.SpecificContent["orderUid"].(string); ok {
		core.OrderUID = &uid
	}
	if et := item.ExceptionType; et != nil && (*et == "business" || *et == "application") {
		core.ExceptionType = et
	}
	if includeContent {
		core.SpecificContent = redactToken(item.SpecificContent)
	}
	memberID, _ := shared.StringProp(item.SpecificContent, "MemberID")
	member, _ := shared.StringProp(item.SpecificContent, "MemberFullName")
	base := ItemDetails{
		RetryCount: item.RetryCount, ParentItemID: item.ParentItemID, Priority: item.Priority,
		MatchValue: item.MatchValue, MemberID: memberID, Member: member,
	}
	core.Details = base
	if detail != nil {
		lineage := make([]LineageEntry, len(detail.Lineage))
		for i, l := range detail.Lineage {
			lineage[i] = toLineage(l)
		}
		core.Details = ItemDetailsFull{ItemDetails: base, CallbackStatus: detail.CallbackStatus, Lineage: lineage}
	}
	return core
}
