package uipath

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// UiPath Orchestrator WRITES — the only file that mutates Orchestrator state. Everything here is
// pre_prod-only (the dev clone, folder 434039): each function asserts env == pre_prod as defense
// in depth beneath the tools' schema-level literal. Prod is never written to (repo invariant).
//
// Atomic, portal-agnostic operations extracted from the dev-clone-e2e-testing skill's per-portal
// scripts: enqueue an item (with the shared test-safety guard), delete a stale New item, and
// start/stop a job for a release. The pre-write reads (GetQueueItem, FetchJobByKey) are called
// directly — never through a cache — so a guard never judges stale state.

const (
	addQueueItemPath = "/odata/Queues/UiPathODataSvc.AddQueueItem"
	startJobsPath    = "/odata/Jobs/UiPath.Server.Configuration.OData.StartJobs"
)

func assertPreProd(env config.Env) error {
	if env != config.PreProd {
		return fmt.Errorf("UiPath writes are pre_prod-only (dev clone) — refusing env '%s'", env) //nolint:staticcheck // user-visible wording
	}
	return nil
}

// safetyLimits are the configured safe values the guard pins posted items to.
func safetyLimits(cfg config.UipathConfig) orchestrator.QueueSafetyLimits {
	return orchestrator.QueueSafetyLimits{
		PreProdServerURL: cfg.ServerURLByEnv[config.PreProd],
		QueueURL:         cfg.QueueURL,
		ConfigSection:    orchestrator.SectionUiPath,
	}
}

// ---- add_queue_item --------------------------------------------------------

type addResult struct {
	queueName, reference string
	itemID               int64 // id of the created item (0 if the response omitted it)
	queueDefinitionID    int64 // the queue it landed in (0 if the response omitted it)
	status               string
	forced               []string // fields the safety guard overrode, e.g. ["IsApproved"]
}

// buildAddQueueItemBody wraps already-guarded content in the AddQueueItem itemData envelope.
func buildAddQueueItemBody(queueName, reference string, priority orchestrator.QueueItemPriority, specificContent map[string]any) map[string]any {
	return map[string]any{"itemData": map[string]any{
		"Name":            queueName,
		"Priority":        priority,
		"Reference":       reference,
		"SpecificContent": specificContent,
	}}
}

// addQueueItem POSTs one item to a dev-clone queue. Every payload passes the test-safety guard
// first (IsApproved forced false; serverURL/queueUrl pinned to the configured pre-prod values;
// <TO-FILL> placeholders rejected).
func addQueueItem(ctx context.Context, args orchestrator.AddQueueItemArgs) (addResult, error) {
	if err := assertPreProd(args.Env); err != nil {
		return addResult{}, err
	}
	if strings.TrimSpace(args.Queue) == "" {
		return addResult{}, errors.New("queueName is required")
	}
	if strings.TrimSpace(args.Reference) == "" {
		return addResult{}, errors.New("reference is required")
	}
	cfg, err := config.Uipath()
	if err != nil {
		return addResult{}, err
	}
	guarded, err := orchestrator.GuardQueueItemSafety(args.SpecificContent, orchestrator.SafetyPreProdPost, safetyLimits(cfg))
	if err != nil {
		return addResult{}, err
	}
	path := cfg.AddQueueItemPath
	if path == "" {
		path = addQueueItemPath
	}
	scope, err := scopeFor(config.PreProd, "")
	if err != nil {
		return addResult{}, err
	}
	data, err := post(ctx, path, buildAddQueueItemBody(args.Queue, args.Reference, args.Priority, guarded.SpecificContent), scope)
	if err != nil {
		return addResult{}, err
	}
	item := toQueueItem(data)
	return addResult{
		queueName: args.Queue, reference: args.Reference, itemID: item.ID,
		queueDefinitionID: item.QueueDefinitionID, status: item.Status, forced: guarded.Forced,
	}, nil
}

// ---- delete_queue_item -----------------------------------------------------

// assertDeletable: only never-picked-up items may be deleted — an InProgress/Successful/Failed
// item is execution history (and possibly a running robot's transaction).
func assertDeletable(item QueueItem) error {
	if item.Status != string(orchestrator.StatusNew) {
		return fmt.Errorf("refusing to delete queue item %d: status is '%s' (only 'New' items may be deleted)", item.ID, item.Status)
	}
	return nil
}

type deleteResult struct {
	reference string
}

// deleteQueueItem is a fetch-first delete: read the item, refuse unless Status is "New", then DELETE.
func deleteQueueItem(ctx context.Context, itemID int64, env config.Env) (deleteResult, error) {
	if err := assertPreProd(env); err != nil {
		return deleteResult{}, err
	}
	scope, err := scopeFor(config.PreProd, "")
	if err != nil {
		return deleteResult{}, err
	}
	item, err := GetQueueItem(ctx, itemID, scope)
	if err != nil {
		return deleteResult{}, err
	}
	if err := assertDeletable(item); err != nil {
		return deleteResult{}, err
	}
	if _, err := del(ctx, fmt.Sprintf("/odata/QueueItems(%d)", itemID), scope); err != nil {
		return deleteResult{}, err
	}
	return deleteResult{reference: item.Reference}, nil
}

// ---- start_job -------------------------------------------------------------

type startArgs struct {
	env            config.Env
	releaseKey     string         // Release GUID Key (from listReleases / list_processes)
	inputArguments map[string]any // empty = none
	jobsCount      int
}

type startedJob struct {
	key, state, releaseName string
}

// buildStartJobBody builds the StartJobs body. InputArguments is the JSON-stringified map
// (Orchestrator's wire format) and is omitted entirely when empty.
func buildStartJobBody(args startArgs) (map[string]any, error) {
	info := map[string]any{"ReleaseKey": args.releaseKey, "Strategy": "ModernJobsCount", "JobsCount": args.jobsCount}
	if len(args.inputArguments) > 0 {
		b, err := shared.MarshalJSON(args.inputArguments)
		if err != nil {
			return nil, err
		}
		info["InputArguments"] = string(b)
	}
	return map[string]any{"startInfo": info}, nil
}

// startJob starts job(s) for a dev-clone release. NOTE the pin race: a job resolves its package
// version at START (not creation) — verify the release's ProcessVersion via listReleases right
// before starting.
func startJob(ctx context.Context, args startArgs) ([]startedJob, error) {
	if err := assertPreProd(args.env); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.releaseKey) == "" {
		return nil, errors.New("releaseKey is required")
	}
	body, err := buildStartJobBody(args)
	if err != nil {
		return nil, err
	}
	scope, err := scopeFor(config.PreProd, "")
	if err != nil {
		return nil, err
	}
	data, err := post(ctx, startJobsPath, body, scope)
	if err != nil {
		return nil, err
	}
	rows := odataValues(data)
	jobs := make([]startedJob, len(rows))
	for i, r := range rows {
		jobs[i] = startedJob{key: shared.Str(r, "Key"), state: shared.Str(r, "State"), releaseName: shared.Str(r, "ReleaseName")}
	}
	return jobs, nil
}

// ---- stop_job ---------------------------------------------------------------

func assertStoppable(job Job) error {
	if TerminalStates[job.State] {
		return fmt.Errorf("refusing to stop job '%s': state is already '%s' (terminal)", job.Key, job.State)
	}
	return nil
}

// stopJob stops (SoftStop) or kills a dev-clone job and returns its key. StopJob addresses
// Orchestrator jobs by numeric Id, not the GUID Key the rest of this API uses, so resolve the Key
// first — scoped to the pre-prod folder, so a prod job's Key resolves to nothing here.
// Fetch-first, like deleteQueueItem: refuses (no HTTP write issued) once the job is already in a
// terminal state.
func stopJob(ctx context.Context, env config.Env, jobKey string, strategy orchestrator.StopStrategy) (string, error) {
	if err := assertPreProd(env); err != nil {
		return "", err
	}
	key := strings.TrimSpace(jobKey)
	if key == "" {
		return "", errors.New("jobKey is required")
	}
	folder, err := ResolveFolder(config.PreProd, "")
	if err != nil {
		return "", err
	}
	job, err := FetchJobByKey(ctx, key, folder)
	if err != nil {
		return "", err
	}
	if job == nil || job.ID == "" {
		return "", fmt.Errorf("no dev-clone job found for key '%s'", key)
	}
	if err := assertStoppable(*job); err != nil {
		return "", err
	}
	id, err := strconv.ParseInt(job.ID, 10, 64)
	if err != nil {
		return "", shared.Expectedf("job '%s' has a non-numeric Id '%s'", key, job.ID)
	}
	scope, err := scopeFor(config.PreProd, "")
	if err != nil {
		return "", err
	}
	_, err = post(ctx, fmt.Sprintf("/odata/Jobs(%d)/UiPath.Server.Configuration.OData.StopJob", id), map[string]any{"strategy": strategy}, scope)
	return key, err
}
