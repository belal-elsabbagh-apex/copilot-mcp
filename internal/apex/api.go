package apex

// Typed Apex endpoint functions over the client chokepoint. Raw row structs list exactly the live
// keys (verified 2026-10-11) the mappers read — nothing speculative — and filters are limited to
// the query params verified to filter server-side: Apex silently ignores unknown params and
// returns everything (e.g. `queue_item_id`). `folder` is a folder NAME override ("" = the env's
// configured folder).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// JobRow is one row of GET /jobs.
type JobRow struct {
	ID string `json:"id"`
	// Status: queued|claimed|running|completed|failed|cancelled|cancelling|lost|timed_out|suspended|resumed.
	Status          string   `json:"status"`
	WorkflowID      string   `json:"workflow_id"`
	FolderID        string   `json:"folder_id"`
	Source          *string  `json:"source"`        // sqs | manual | schedule | job
	QueueItemID     *string  `json:"queue_item_id"` // null on manual jobs
	QueueID         *string  `json:"queue_id"`
	QueueItemRef    *string  `json:"queue_item_reference"`
	WorkflowName    string   `json:"workflow_name"`
	QueueName       *string  `json:"queue_name"`
	CredentialName  *string  `json:"credential_name"`
	RetryOfJobID    *string  `json:"retry_of_job_id"`
	AttemptNumber   int      `json:"attempt_number"`
	WorkerID        *string  `json:"worker_id"`
	ErrorClass      *string  `json:"error_class"` // permanent|transient|missing_credential|needs_human|credential
	WorkflowVersion *string  `json:"workflow_version"`
	CallbackStatus  *string  `json:"callback_status"`
	StartedAt       *string  `json:"started_at"` // null = never started (e.g. missing_credential reap)
	CompletedAt     *string  `json:"completed_at"`
	CreatedAt       string   `json:"created_at"`
	DurationSeconds *float64 `json:"duration_seconds"`
}

// JobDetail is GET /jobs/{id}: the row plus the detail-only keys the list rows do not carry.
type JobDetail struct {
	JobRow
	InputPayload            map[string]any  `json:"input_payload"`
	ResultPayload           json.RawMessage `json:"result_payload"`
	ErrorMessage            *string         `json:"error_message"`
	ExitCode                *int            `json:"exit_code"`
	Outcome                 *string         `json:"outcome"`
	OutcomeLabel            *string         `json:"outcome_label"`
	NeedsReview             bool            `json:"needs_review"`
	EffectiveTimeoutSeconds *int            `json:"effective_timeout_seconds"`
}

// Queue is one row of GET /queues.
type Queue struct {
	ID                     string  `json:"id"`
	Name                   string  `json:"name"`
	WorkflowID             *string `json:"workflow_id"`
	IsActive               bool    `json:"is_active"`
	AutoDrain              bool    `json:"auto_drain"`
	MaxConcurrentJobs      *int    `json:"max_concurrent_jobs"`
	MaxRetries             *int    `json:"max_retries"`
	EnforceUniqueReference bool    `json:"enforce_unique_reference"`
}

// QueueItem is one row of GET /queues/{id}/items.
type QueueItem struct {
	ID              string         `json:"id"`
	QueueID         string         `json:"queue_id"`
	Reference       string         `json:"reference"`
	SpecificContent map[string]any `json:"specific_content"`
	MatchValue      *string        `json:"match_value"`
	Priority        *int           `json:"priority"`
	Status          string         `json:"status"`         // new|in_progress|successful|failed|retried|abandoned|deleted
	ExceptionType   *string        `json:"exception_type"` // business | application
	RetryCount      *int           `json:"retry_count"`
	ParentItemID    *string        `json:"parent_item_id"`
	JobID           *string        `json:"job_id"` // the latest job
	CreatedAt       string         `json:"created_at"`
	EndedAt         *string        `json:"ended_at"`
}

// LineageJob is the job of a lineage row.
type LineageJob struct {
	ID         string  `json:"id"`
	Status     string  `json:"status"`
	ErrorClass *string `json:"error_class"`
}

// LineageRow is one retry-chain entry of a queue-item detail.
type LineageRow struct {
	ItemID        string      `json:"item_id"`
	Reference     string      `json:"reference"`
	Status        string      `json:"status"`
	ExceptionType *string     `json:"exception_type"`
	Job           *LineageJob `json:"job"`
}

// QueueItemDetail is GET /queues/{id}/items/{itemId}.
type QueueItemDetail struct {
	QueueItem
	CallbackStatus *string      `json:"callback_status"`
	Lineage        []LineageRow `json:"lineage"`
}

// Workflow is one row of GET /workflows (an Apex "Process").
type Workflow struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	PackageName     *string `json:"package_name"`
	IsActive        bool    `json:"is_active"`
	TimeoutSeconds  *int    `json:"timeout_seconds"`
	MaxAttempts     *int    `json:"max_attempts"`
	BackoffStrategy string  `json:"backoff_strategy"`
	ArtifactPolicy  string  `json:"artifact_policy"` // always | on_failure | never
	CurrentVersion  *string `json:"current_version"`
}

// Schedule is one row of GET /schedules.
type Schedule struct {
	ID             string `json:"id"`
	WorkflowID     string `json:"workflow_id"`
	CronExpression string `json:"cron_expression"`
	Timezone       string `json:"timezone"`
	IsPaused       bool   `json:"is_paused"`
}

// Artifact is one entry of a job's artifact listing.
type Artifact struct {
	ID           string `json:"id"`
	Filename     string `json:"filename"`      // "job.log", "trace.zip", "recording.mp4", …
	ArtifactType string `json:"artifact_type"` // logs | videos | other
	SizeBytes    int64  `json:"size_bytes"`
	DownloadURL  string `json:"download_url"` // "/api/v1/jobs/{id}/artifacts/{artifact_id}" — Bearer required
}

// JobFilters: ONLY these filter (queue_item_id / search / queue_item_reference are ignored by
// /jobs). Status is the wire value (queued, failed, …), not the dashboard label. "" = unset.
type JobFilters struct {
	Status       string
	QueueID      string
	WorkflowID   string
	CreatedAfter string
	Reference    string // exact; this is how every attempt of one queue item is enumerated
}

// ItemFilters: Status is the wire value (new, in_progress, …); Search is fuzzy over the content.
type ItemFilters struct {
	Status string
	Search string
}

func (f JobFilters) query() map[string]string {
	return nonEmpty(map[string]string{
		"status": f.Status, "queue_id": f.QueueID, "workflow_id": f.WorkflowID,
		"created_after": f.CreatedAfter, "reference": f.Reference,
	})
}

func (f ItemFilters) query() map[string]string {
	return nonEmpty(map[string]string{"status": f.Status, "search": f.Search})
}

func nonEmpty(m map[string]string) map[string]string {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	return m
}

const bigList = 1000 // directory-sized lists (queues, workflows, schedules) per folder

var seg = url.PathEscape

// listJobs lists jobs newest first: Apex lists default to created_at desc, pinned here so a
// default change can't flip `top`.
func listJobs(ctx context.Context, env config.Env, folder string, f JobFilters, limit int) ([]JobRow, error) {
	q := f.query()
	q["sort_by"], q["sort_dir"] = "created_at", "desc"
	return apexPages[JobRow](ctx, env, "/jobs", q, limit, folder)
}

// getJob is the ONE raw single-job fetch.
func getJob(ctx context.Context, env config.Env, folder, id string) (JobDetail, error) {
	return getJSON[JobDetail](ctx, env, "/jobs/"+seg(id), folder)
}

func listWorkflows(ctx context.Context, env config.Env, folder string) ([]Workflow, error) {
	return apexPages[Workflow](ctx, env, "/workflows", nil, bigList, folder)
}

func listQueues(ctx context.Context, env config.Env, folder string) ([]Queue, error) {
	return apexPages[Queue](ctx, env, "/queues", nil, bigList, folder)
}

func getQueue(ctx context.Context, env config.Env, folder, queueID string) (Queue, error) {
	return getJSON[Queue](ctx, env, "/queues/"+seg(queueID), folder)
}

// listQueueItems lists newest first by default (live-verified); the server has no verified sort param.
func listQueueItems(ctx context.Context, env config.Env, folder, queueID string, f ItemFilters, limit int) ([]QueueItem, error) {
	return apexPages[QueueItem](ctx, env, "/queues/"+seg(queueID)+"/items", f.query(), limit, folder)
}

func getQueueItem(ctx context.Context, env config.Env, folder, queueID, itemID string) (QueueItemDetail, error) {
	return getJSON[QueueItemDetail](ctx, env, "/queues/"+seg(queueID)+"/items/"+seg(itemID), folder)
}

func listSchedules(ctx context.Context, env config.Env, folder string) ([]Schedule, error) {
	return apexPages[Schedule](ctx, env, "/schedules", nil, bigList, folder)
}

// listArtifacts is the ONE raw artifact listing (a bare-array route; the listing needs the
// folder header outside the default folder).
func listArtifacts(ctx context.Context, env config.Env, folder, jobID string) ([]Artifact, error) {
	raw, err := apexRequest(ctx, env, http.MethodGet, "/jobs/"+seg(jobID)+"/artifacts", reqOpts{Folder: folder})
	if err != nil {
		return nil, err
	}
	var rows []Artifact
	if json.Unmarshal(raw, &rows) != nil || rows == nil {
		return nil, shared.Expectedf("Apex artifact listing was not a list")
	}
	return rows, nil
}
