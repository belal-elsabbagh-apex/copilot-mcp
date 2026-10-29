package orchestrator

import "github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"

// QueueItemPayloadArgs are the inputs of BuildQueueItemPayload. Content is the canonical
// native-typed SpecificContent from internal/queueitem, WITHOUT serverURL/queueUrl; the backend
// fills those from its own config and serializes.
type QueueItemPayloadArgs struct {
	Env       config.Env
	QueueName string
	OrderUID  string
	Content   map[string]any
	BeBase    string // the Copilot BE the order was read from; UiPath serverURL fallback; "" = unset
}

// QueueItemPayload is the backend's wire payload, ready to hand to AddQueueItem's endpoint.
type QueueItemPayload struct {
	Payload any            `json:"payload"`
	Notes   []string       `json:"notes"`
	Meta    map[string]any `json:"meta"`
}

// QueueItemPriority is a UiPath queue-item priority.
type QueueItemPriority string

// The queue-item priorities.
const (
	PriorityLow    QueueItemPriority = "Low"
	PriorityNormal QueueItemPriority = "Normal"
	PriorityHigh   QueueItemPriority = "High"
)

// AddQueueItemArgs are the inputs of AddQueueItem.
type AddQueueItemArgs struct {
	Env             config.Env
	Queue           string // UiPath: exact queue Name; Apex: queue name or uuid
	Reference       string
	SpecificContent map[string]any
	Priority        QueueItemPriority // UiPath only; Apex uses the queue's default
}

// AddQueueItemResult is the output of AddQueueItem.
type AddQueueItemResult struct {
	Env       config.Env `json:"env"`
	QueueID   string     `json:"queueId"`
	QueueName string     `json:"queueName"`
	Reference string     `json:"reference"`
	ItemID    string     `json:"itemId"`
	Status    string     `json:"status"`
	Forced    []string   `json:"forced"`
}

// DeleteQueueItemArgs are the inputs of DeleteQueueItem.
type DeleteQueueItemArgs struct {
	Env     config.Env
	ItemID  string
	QueueID string // required by Apex; "" = unset
}

// DeleteQueueItemResult is the output of DeleteQueueItem.
type DeleteQueueItemResult struct {
	Env            config.Env `json:"env"`
	ItemID         string     `json:"itemId"`
	Deleted        bool       `json:"deleted"` // always true
	PreviousStatus string     `json:"previousStatus"`
	Reference      string     `json:"reference"`
}

// StartJobArgs are the inputs of StartJob.
type StartJobArgs struct {
	Env        config.Env
	ProcessID  string
	Input      map[string]any
	JobsCount  int
	MatchValue string // required by Apex; "" = unset
}

// StartedJob is one job StartJob started.
type StartedJob struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	ProcessName string `json:"processName"`
	Link        string `json:"link"`
}

// StartJobResult is the output of StartJob.
type StartJobResult struct {
	Env  config.Env   `json:"env"`
	Jobs []StartedJob `json:"jobs"`
}

// StopStrategy is how StopJob stops a job.
type StopStrategy string

// The stop strategies.
const (
	StopSoft StopStrategy = "SoftStop"
	StopKill StopStrategy = "Kill"
)

// StopJobArgs are the inputs of StopJob.
type StopJobArgs struct {
	Env      config.Env
	JobID    string
	Strategy StopStrategy
}

// StopJobResult is the output of StopJob.
type StopJobResult struct {
	Env      config.Env   `json:"env"`
	JobID    string       `json:"jobId"`
	Strategy StopStrategy `json:"strategy"`
	Stopped  bool         `json:"stopped"` // always true
}
