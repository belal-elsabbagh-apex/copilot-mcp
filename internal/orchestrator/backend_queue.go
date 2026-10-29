package orchestrator

import "github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"

// ListQueuesArgs are the inputs of ListQueues.
type ListQueuesArgs struct {
	Env          config.Env
	Folder       string // "" = the env's default folder
	NameContains string // "" = unset
	Top          int
}

// QueueKind picks a portal's submit or sync queue.
type QueueKind string

// The queue kinds.
const (
	QueueSubmit QueueKind = "submit"
	QueueSync   QueueKind = "sync"
)

// ListQueueItemsArgs are the inputs of ListQueueItems. Queue is a portal key/alias (e.g. "PMG")
// or the backend's own queue id.
type ListQueueItemsArgs struct {
	Env       config.Env
	Queue     string
	QueueKind QueueKind
	Status    QueueItemStatus // "" = unset
	Top       int
}

// ListQueueItemsResult is the output of ListQueueItems.
type ListQueueItemsResult struct {
	QueueID   string          `json:"queueId"`
	QueueName string          `json:"queueName"`
	Items     []QueueItemCore `json:"items"`
}

// FindOrderArgs are the inputs of FindOrderQueueItems.
type FindOrderArgs struct {
	Env                    config.Env
	OrderUID               string
	Folder                 string // "" = the env's default folder
	Since                  string // "" = unset
	Top                    int
	IncludeSpecificContent bool
}

// FindOrderResult is the output of FindOrderQueueItems. Items are newest first, SpecificContent
// token value "[redacted]".
type FindOrderResult struct {
	Folder      string          `json:"folder"`
	SearchMode  string          `json:"searchMode"`
	Scanned     int             `json:"scanned"`
	Items       []QueueItemCore `json:"items"`
	Notes       []string        `json:"notes"`
	SearchError string          `json:"searchError,omitempty"`
}

// PullQueueItemArgs are the inputs of PullQueueItem. UiPath: URL, or ItemID (+ Env, optional
// FolderID). Apex: Env + QueueID + ItemID. Every field is optional at this layer ("" = unset).
type PullQueueItemArgs struct {
	Env      config.Env
	URL      string
	QueueID  string
	ItemID   string
	FolderID string
}

// PullQueueItemResult is the output of PullQueueItem.
type PullQueueItemResult struct {
	Env               config.Env    `json:"env"`
	Item              QueueItemCore `json:"item"` // SpecificContent present, IsApproved forced false
	Portal            PortalRef     `json:"portal"`
	IsApprovedForced  bool          `json:"isApprovedForced"` // always true
	SuggestedFilename string        `json:"suggestedFilename"`
	QueueNameError    string        `json:"queueNameError,omitempty"`
}

// ListProcessesArgs are the inputs of ListProcesses.
type ListProcessesArgs struct {
	Env          config.Env
	Folder       string // "" = the env's default folder
	NameContains string // "" = unset
	Top          int
}

// ListTriggersArgs are the inputs of ListTriggers.
type ListTriggersArgs struct {
	Env     config.Env
	Folder  string // "" = the env's default folder
	QueueID string // "" = unset
	Top     int
}

// FaultIssueArgs are the inputs of BuildFaultedJobIssue.
type FaultIssueArgs struct {
	Env    config.Env
	JobID  string
	Folder string   // "" = the env's default folder
	Repo   string   // "" = unset (callers default to DefaultRepo)
	Labels []string // nil = unset
}

// FaultedJobIssueResult is a FaultedJobIssue plus whether the job was found.
type FaultedJobIssueResult struct {
	FaultedJobIssue
	Found     bool   `json:"found"`
	LogsError string `json:"logsError,omitempty"`
}
