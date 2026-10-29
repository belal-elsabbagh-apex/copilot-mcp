// Package orchestrator is the backend-neutral orchestrator contract: the normalized shapes every
// orchestrator tool returns, whichever backend served the call, plus the pure helpers the
// backends share (log digest, failure semantics, fault signatures, the queue-item safety guard).
// Backend-specific fields live under each core's Details, never at the top level, so agents read
// one vocabulary across backends. It imports nothing from the copilot/uipath/apex packages.
package orchestrator

// Orchestrator names a backend.
type Orchestrator string

// The registered orchestrators.
const (
	UiPath Orchestrator = "uipath"
	Apex   Orchestrator = "apex"
)

// Orchestrators lists every Orchestrator, in the order tool schemas advertise them.
var Orchestrators = []Orchestrator{UiPath, Apex}

// JobLog is one log row as the shared digest/filter code reads it. Backends embed it (UiPath adds
// RawMessage, Apex adds fields); shared code reads only these three.
type JobLog struct {
	Level     string `json:"Level"`
	Message   string `json:"Message"`
	TimeStamp string `json:"TimeStamp"`
}

// JobFault is the fault of a job. Class is the backend's failure classification: UiPath = the
// exception type parsed from the headline error; Apex = the Process outcome, else error_class.
// nil fields mean "not available from this read", never "no fault".
type JobFault struct {
	Message   *string `json:"message"`
	Signature *string `json:"signature"`
	Class     *string `json:"class"`
}

// JobAttempt is one execution attempt of a transaction, as known from the queue-item read alone.
// State/NeverStarted are nil when the backend can't tell without fetching the job.
type JobAttempt struct {
	Orchestrator Orchestrator `json:"orchestrator"`
	ID           string       `json:"id"`
	State        *string      `json:"state"`
	NeverStarted *bool        `json:"neverStarted"`
}

// JobProcess is the process a job ran. ID and Version are nil when unknown.
type JobProcess struct {
	ID      *string `json:"id"`
	Name    string  `json:"name"`
	Version *string `json:"version"`
}

// JobQueueItem links a job to the queue item it processes.
type JobQueueItem struct {
	QueueID   string `json:"queueId"`
	ItemID    string `json:"itemId"`
	Reference string `json:"reference"`
}

// JobCore is the normalized job. Details carries the backend's own struct.
type JobCore struct {
	Orchestrator Orchestrator  `json:"orchestrator"`
	ID           string        `json:"id"`       // UiPath job Key (GUID) | Apex job uuid
	State        string        `json:"state"`    // dashboard label (UiPath State vocabulary)
	RawState     string        `json:"rawState"` // the backend's own status string, verbatim
	Terminal     bool          `json:"terminal"`
	Process      JobProcess    `json:"process"`
	QueueItem    *JobQueueItem `json:"queueItem"`
	CreatedAt    string        `json:"createdAt"`
	StartedAt    *string       `json:"startedAt"`
	EndedAt      *string       `json:"endedAt"`
	DurationMs   *int64        `json:"durationMs"`
	Attempt      *int          `json:"attempt"`
	RetryOfJobID *string       `json:"retryOfJobId"`
	Fault        *JobFault     `json:"fault"`
	Link         string        `json:"link"` // dashboard deep link ("" when unknown)
	Details      any           `json:"details"`
}

// QueueItemStatus is a queue-item status in UiPath's spelling; Apex statuses are mapped onto these.
type QueueItemStatus string

// The queue-item statuses.
const (
	StatusNew        QueueItemStatus = "New"
	StatusInProgress QueueItemStatus = "InProgress"
	StatusSuccessful QueueItemStatus = "Successful"
	StatusFailed     QueueItemStatus = "Failed"
	StatusRetried    QueueItemStatus = "Retried"
	StatusAbandoned  QueueItemStatus = "Abandoned"
	StatusDeleted    QueueItemStatus = "Deleted"
)

// QueueItemStatuses lists every QueueItemStatus, in the order tool schemas advertise them.
var QueueItemStatuses = []QueueItemStatus{
	StatusNew, StatusInProgress, StatusSuccessful, StatusFailed, StatusRetried, StatusAbandoned, StatusDeleted,
}

// QueueItemCore is the normalized queue item. Details carries the backend's own struct.
type QueueItemCore struct {
	Orchestrator Orchestrator `json:"orchestrator"`
	QueueID      string       `json:"queueId"`
	QueueName    string       `json:"queueName"`
	ItemID       string       `json:"itemId"`
	Reference    string       `json:"reference"`
	OrderUID     *string      `json:"orderUid"`
	Status       string       `json:"status"` // QueueItemStatus spelling when the backend value maps, else verbatim
	RawStatus    string       `json:"rawStatus"`
	// ExceptionType is "business" | "application" | nil.
	ExceptionType *string `json:"exceptionType"`
	CreatedAt     string  `json:"createdAt"`
	EndedAt       *string `json:"endedAt"`
	// Jobs are newest first, at most one on UiPath. Backends MUST set a non-nil slice (marshals []).
	Jobs []JobAttempt `json:"jobs"`
	// SpecificContent is nil (JSON null) when not part of this projection.
	SpecificContent map[string]any `json:"specificContent"`
	Details         any            `json:"details"`
}

// QueueCore is the normalized queue.
type QueueCore struct {
	Orchestrator Orchestrator `json:"orchestrator"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	ProcessID    *string      `json:"processId"`
	Active       bool         `json:"active"`
	Details      any          `json:"details"`
}

// ProcessCore is the normalized process.
type ProcessCore struct {
	Orchestrator Orchestrator `json:"orchestrator"`
	ID           string       `json:"id"` // what start_job takes as processId (UiPath release Key | Apex workflow uuid)
	Name         string       `json:"name"`
	Package      *string      `json:"package"`
	Version      *string      `json:"version"`
	Active       bool         `json:"active"`
	Details      any          `json:"details"`
}

// TriggerKind is "queue" or "time".
type TriggerKind string

// The trigger kinds.
const (
	TriggerQueue TriggerKind = "queue"
	TriggerTime  TriggerKind = "time"
)

// TriggerCore is the normalized trigger.
type TriggerCore struct {
	Orchestrator Orchestrator `json:"orchestrator"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Kind         TriggerKind  `json:"kind"`
	ProcessID    *string      `json:"processId"`
	QueueID      *string      `json:"queueId"`
	Enabled      bool         `json:"enabled"`
	Cron         *string      `json:"cron"`
	Details      any          `json:"details"`
}

// PortalRef is a portal reference exposed on a pull result. Matched=false carries empty sentinels.
type PortalRef struct {
	Matched       bool   `json:"matched"`
	Key           string `json:"key"`
	PortalDir     string `json:"portalDir"`
	BuildArtifact string `json:"buildArtifact"`
	Family        string `json:"family"`
}
