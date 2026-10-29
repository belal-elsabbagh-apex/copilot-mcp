package orchestrator

import (
	"context"
	"encoding/json"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Backend is the one interface every orchestrator backend implements. The server dispatches each
// orchestrator tool through backends.BackendFor(orchestrator) and wraps the result as
// { orchestrator, env, ...result }, so backends never shape tool envelopes themselves.
//
// Optional input strings use "" for "not given"; optional input numbers use a pointer. Slices a
// result marshals MUST be non-nil (they are `[]`, never `null`, on the TS wire).
type Backend interface {
	// Name is the orchestrator this backend serves.
	Name() Orchestrator
	// ListJobs lists recent jobs in the env's folder (or args.Folder), newest first.
	ListJobs(ctx context.Context, args ListJobsArgs) (ListJobsResult, error)
	// GetJobs reads one or more jobs, each optionally with a log digest; progress ticks per job.
	GetJobs(ctx context.Context, args GetJobsArgs, onProgress shared.StepProgress) (GetJobsResult, error)
	// GetJobLogs reads (filtered) robot logs for one or more jobs; progress ticks per job.
	GetJobLogs(ctx context.Context, args GetJobLogsArgs, onProgress shared.StepProgress) (GetJobLogsResult, error)
	// ListQueues lists the folder's queues, optionally filtered by name.
	ListQueues(ctx context.Context, args ListQueuesArgs) ([]QueueCore, error)
	// ListQueueItems lists a queue's items, newest first; Queue is a portal key/alias or the backend's queue id.
	ListQueueItems(ctx context.Context, args ListQueueItemsArgs) (ListQueueItemsResult, error)
	// FindOrderQueueItems finds the queue items (and their jobs) for one Copilot orderUid.
	FindOrderQueueItems(ctx context.Context, args FindOrderArgs) (FindOrderResult, error)
	// PullQueueItem reads one queue item with its SpecificContent, IsApproved forced false.
	PullQueueItem(ctx context.Context, args PullQueueItemArgs) (PullQueueItemResult, error)
	// ListProcesses lists the folder's processes (UiPath releases / Apex workflows).
	ListProcesses(ctx context.Context, args ListProcessesArgs) ([]ProcessCore, error)
	// ListTriggers lists the folder's triggers, optionally for one queue.
	ListTriggers(ctx context.Context, args ListTriggersArgs) ([]TriggerCore, error)
	// BuildFaultedJobIssue turns a faulted job into a ready-to-post GitHub issue payload; posts nothing.
	BuildFaultedJobIssue(ctx context.Context, args FaultIssueArgs) (FaultedJobIssueResult, error)
	// BuildQueueItemPayload serializes the canonical SpecificContent into the backend's wire payload; BUILD ONLY, never POSTs.
	BuildQueueItemPayload(ctx context.Context, args QueueItemPayloadArgs) (QueueItemPayload, error)
	// AddQueueItem posts a guarded queue item (pre_prod only; guardQueueItemSafety, preProdPost mode).
	AddQueueItem(ctx context.Context, args AddQueueItemArgs) (AddQueueItemResult, error)
	// DeleteQueueItem deletes a queue item (pre_prod only; fetch-first, refuses anything not New).
	DeleteQueueItem(ctx context.Context, args DeleteQueueItemArgs) (DeleteQueueItemResult, error)
	// StartJob starts jobs for a process (pre_prod only).
	StartJob(ctx context.Context, args StartJobArgs) (StartJobResult, error)
	// StopJob stops a non-terminal job (pre_prod only; fetch-first).
	StopJob(ctx context.Context, args StopJobArgs) (StopJobResult, error)
}

// ListJobsArgs are the inputs of ListJobs.
type ListJobsArgs struct {
	Env         config.Env
	Folder      string // "" = the env's default folder
	Since       string // "" = unset
	Top         int
	ProcessName string // "" = unset
}

// ListJobsResult is the output of ListJobs.
type ListJobsResult struct {
	Folder string    `json:"folder"`
	Jobs   []JobCore `json:"jobs"`
}

// LogsUnavailableReason says why a job's logs could not be read.
type LogsUnavailableReason string

// The reasons logs can be unavailable.
const (
	LogsRunning        LogsUnavailableReason = "running"
	LogsNeverStarted   LogsUnavailableReason = "never-started"
	LogsArtifactPolicy LogsUnavailableReason = "artifact-policy"
)

// LogsUnavailable explains why logs are missing.
type LogsUnavailable struct {
	Reason LogsUnavailableReason `json:"reason"`
	Detail string                `json:"detail"`
}

// GetJobsArgs are the inputs of GetJobs. Batch=false: a lookup failure of the single id returns
// an error (fails the tool call); Batch=true: it lands on that entry's Error and the rest still
// return.
type GetJobsArgs struct {
	Env              config.Env
	JobIDs           []string
	Batch            bool
	Folder           string // "" = the env's default folder
	IncludeLogDigest bool
}

// JobEntry is one job of a GetJobs answer.
type JobEntry struct {
	ID    string   `json:"id"`
	Found bool     `json:"found"`
	Job   *JobCore `json:"job,omitempty"`
	// LogDigest is a JobLogDigest or a backend struct embedding one (UiPath +transactionOutcome,
	// Apex +run).
	LogDigest       any              `json:"logDigest,omitempty"`
	LogsUnavailable *LogsUnavailable `json:"logsUnavailable,omitempty"`
	LogDigestError  string           `json:"logDigestError,omitempty"`
	Error           string           `json:"error,omitempty"`
}

// GetJobsResult is the output of GetJobs.
type GetJobsResult struct {
	Folder  string     `json:"folder"`
	Entries []JobEntry `json:"entries"`
}

// LogLevel is a minimum severity for log filtering.
type LogLevel string

// The filterable minimum levels.
const (
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
)

// LogFilter selects which log rows a GetJobLogs call keeps.
type LogFilter struct {
	MinLevel     LogLevel // "" = unset
	Contains     string   // "" = unset
	OnlyFailures bool
	Tail         *int // nil = unset (UiPath distinguishes unset from any number)
}

// GetJobLogsArgs are the inputs of GetJobLogs.
type GetJobLogsArgs struct {
	Env              config.Env
	JobIDs           []string
	Batch            bool
	Folder           string // "" = the env's default folder
	Filter           LogFilter
	FullMessages     bool
	IncludeRawFields bool
	IncludeVideo     bool
}

// LogRow is one log row of a GetJobLogs answer.
type LogRow struct {
	Level     string `json:"Level"`
	Message   string `json:"Message"`
	TimeStamp string `json:"TimeStamp"`
	// Raw is set only when includeRawFields: UiPath RawMessage fields | Apex NDJSON `fields`.
	// Empty = key omitted; the literal `null` = the key present with a null value (TS `raw: null`).
	Raw json.RawMessage `json:"raw,omitempty"`
}

// The VideoRef kinds.
const (
	VideoURL      = "url"
	VideoArtifact = "artifact"
)

// VideoRef is a run's video: a direct URL, or a downloadable artifact (build with URLVideo /
// ArtifactVideo; unused fields stay omitted).
type VideoRef struct {
	Kind      string `json:"kind"`
	URL       string `json:"url,omitempty"`
	Artifact  string `json:"artifact,omitempty"`
	SizeBytes *int64 `json:"sizeBytes,omitempty"`
	FetchWith string `json:"fetchWith,omitempty"` // always "get_job_artifacts" for an artifact
}

// URLVideo is a direct-URL VideoRef.
func URLVideo(url string) *VideoRef { return &VideoRef{Kind: VideoURL, URL: url} }

// ArtifactVideo is a VideoRef fetched with get_job_artifacts.
func ArtifactVideo(artifact string, sizeBytes int64) *VideoRef {
	return &VideoRef{Kind: VideoArtifact, Artifact: artifact, SizeBytes: &sizeBytes, FetchWith: "get_job_artifacts"}
}

// JobLogsEntry is one job of a GetJobLogs answer. Logs MUST be non-nil.
type JobLogsEntry struct {
	JobID           string           `json:"jobId"`
	Logs            []LogRow         `json:"logs"`
	Returned        int              `json:"returned"`
	TotalMatching   *int             `json:"totalMatching,omitempty"`
	Truncated       bool             `json:"truncated"`
	LogsUnavailable *LogsUnavailable `json:"logsUnavailable,omitempty"`
	Error           string           `json:"error,omitempty"`
	Video           *VideoRef        `json:"video,omitempty"`
	VideoError      string           `json:"videoError,omitempty"`
}

// GetJobLogsResult is the output of GetJobLogs.
type GetJobLogsResult struct {
	Folder  string         `json:"folder"`
	Entries []JobLogsEntry `json:"entries"`
}
