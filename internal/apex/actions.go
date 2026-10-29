package apex

// Apex WRITES — the only module that mutates Apex state. Everything here is pre_prod-only (the
// 'Authorization Test' folder): every function asserts env == pre_prod BEFORE any I/O, beneath the
// tools' schema-level pre_prod enum and the apexRequest chokepoint's own non-GET gate. Prod and
// test folders share one deployment and one login, so the folder is the only boundary (repo
// invariant).
//
// The same four operations as uipath/actions: enqueue a guarded item, delete a New item, start
// jobs, stop a job — Apex-shaped: queues and Processes are resolved by name/id at call time
// (nothing hardcoded), and every refusal (guard violation, non-new item, terminal job) happens
// before the write is issued. Writes are never retried. The guards call the raw API functions
// directly (never a cache): they must see live state.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func assertPreProd(env config.Env) error {
	if env != config.PreProd {
		return shared.Expectedf("Apex writes are pre_prod-only (Authorization Test folder) — refusing env '%s'", env)
	}
	return nil
}

func required(name, v string) (string, error) {
	t := strings.TrimSpace(v)
	if t == "" {
		return "", shared.Expectedf("%s is required", name)
	}
	return t, nil
}

// resolveWriteQueue finds a pre_prod queue by uuid or exact name, both case-insensitive.
// Ambiguity is refused, never guessed: queue names in the wild differ only by case
// (`Cencal` / `CENCAL`).
func resolveWriteQueue(ctx context.Context, queue string) (Queue, error) {
	wanted := strings.ToLower(strings.TrimSpace(queue))
	queues, err := listQueues(ctx, config.PreProd, "")
	if err != nil {
		return Queue{}, err
	}
	var matches []Queue
	for _, q := range queues {
		if strings.ToLower(q.ID) == wanted || strings.ToLower(q.Name) == wanted {
			matches = append(matches, q)
		}
	}
	switch len(matches) {
	case 0:
		return Queue{}, shared.Expectedf("no queue named or with id '%s' in the pre_prod folder — see list_queues", queue)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, q := range matches {
		ids[i] = q.ID
	}
	return Queue{}, shared.Expectedf("'%s' matches %d queues (%s) — pass the queue uuid", queue, len(matches), strings.Join(ids, ", "))
}

// ---- add_queue_item ---------------------------------------------------------

// AddQueueItem POSTs one item to a pre-prod queue. The payload passes the test-safety guard first
// (IsApproved forced false; serverURL/queueUrl pinned to the configured pre-prod values;
// <TO-FILL> placeholders rejected). Priority is the queue's default (args.Priority is UiPath-only).
// Every check that needs no I/O runs before the queue lookup.
func (backend) AddQueueItem(ctx context.Context, args orchestrator.AddQueueItemArgs) (orchestrator.AddQueueItemResult, error) {
	if err := assertPreProd(args.Env); err != nil {
		return orchestrator.AddQueueItemResult{}, err
	}
	reference, err := required("reference", args.Reference)
	if err != nil {
		return orchestrator.AddQueueItemResult{}, err
	}
	apex, err := config.ResolveApex()
	if err != nil {
		return orchestrator.AddQueueItemResult{}, err
	}
	guarded, err := orchestrator.GuardQueueItemSafety(args.SpecificContent, orchestrator.SafetyPreProdPost, orchestrator.QueueSafetyLimits{
		PreProdServerURL: apex.ServerURLByEnv[config.PreProd],
		QueueURL:         apex.QueueURL,
		ConfigSection:    orchestrator.SectionApex,
	})
	if err != nil {
		return orchestrator.AddQueueItemResult{}, err
	}
	matchValue, err := matchValueOf(guarded.SpecificContent)
	if err != nil {
		return orchestrator.AddQueueItemResult{}, err
	}
	queueName, err := required("queue", args.Queue)
	if err != nil {
		return orchestrator.AddQueueItemResult{}, err
	}
	queue, err := resolveWriteQueue(ctx, queueName)
	if err != nil {
		return orchestrator.AddQueueItemResult{}, err
	}
	raw, err := apexRequest(ctx, config.PreProd, http.MethodPost, "/queues/"+seg(queue.ID)+"/items", reqOpts{
		Body: map[string]any{"specific_content": guarded.SpecificContent, "reference": reference, "match_value": matchValue},
	})
	if err != nil {
		return orchestrator.AddQueueItemResult{}, err
	}
	created := shared.SafeJSONParse(string(raw))
	status, _ := shared.StringProp(created, "status")
	id, _ := shared.StringProp(created, "id")
	return orchestrator.AddQueueItemResult{
		Env: args.Env, QueueID: queue.ID, QueueName: queue.Name, Reference: reference,
		ItemID: id, Status: itemLabel(status), Forced: guarded.Forced,
	}, nil
}

// ---- delete_queue_item ------------------------------------------------------

// DeleteQueueItem is fetch-first: only a never-picked-up ('new') item may go — anything else is
// execution history (and possibly a running job's transaction). The dashboard also offers delete
// for failed/abandoned items; parity with UiPath and evidence preservation keep this strict.
// queueId must be a queue in the pre-prod folder's own listing (client-side folder proof).
func (backend) DeleteQueueItem(ctx context.Context, args orchestrator.DeleteQueueItemArgs) (orchestrator.DeleteQueueItemResult, error) {
	if err := assertPreProd(args.Env); err != nil {
		return orchestrator.DeleteQueueItemResult{}, err
	}
	itemID, err := required("itemId", args.ItemID)
	if err != nil {
		return orchestrator.DeleteQueueItemResult{}, err
	}
	queueID := strings.TrimSpace(args.QueueID)
	if queueID == "" {
		return orchestrator.DeleteQueueItemResult{}, shared.Expectedf("queueId is required for Apex (from list_queue_items / find_order_queue_items output)")
	}
	queues, err := listQueues(ctx, config.PreProd, "")
	if err != nil {
		return orchestrator.DeleteQueueItemResult{}, err
	}
	if !containsID(queues, queueID) {
		return orchestrator.DeleteQueueItemResult{}, shared.Expectedf("queueId '%s' not found in the pre_prod folder — see list_queues; nothing deleted", queueID)
	}
	item, err := getQueueItem(ctx, config.PreProd, "", queueID, itemID)
	if err != nil {
		return orchestrator.DeleteQueueItemResult{}, err
	}
	if item.Status != "new" {
		return orchestrator.DeleteQueueItemResult{}, shared.Expectedf("refusing to delete queue item %s: status is '%s' (only 'New' items may be deleted)", itemID, itemLabel(item.Status))
	}
	_, err = apexRequest(ctx, config.PreProd, http.MethodPost, "/queues/"+seg(queueID)+"/items/"+seg(itemID)+":delete", reqOpts{Body: map[string]any{}})
	if err != nil {
		return orchestrator.DeleteQueueItemResult{}, err
	}
	return orchestrator.DeleteQueueItemResult{Env: args.Env, ItemID: itemID, Deleted: true, PreviousStatus: "New", Reference: item.Reference}, nil
}

func containsID(queues []Queue, id string) bool {
	for _, q := range queues {
		if strings.EqualFold(q.ID, id) {
			return true
		}
	}
	return false
}

// ---- start_job --------------------------------------------------------------

// StartJob starts jobsCount jobs for a pre-prod Process. match_value picks the credential the job
// runs under, so it is required (no safe default). Sequential and NEVER retried: a retried POST
// could double-start a real portal login. A mid-loop failure reports the jobs that were already
// started, since they keep running.
func (backend) StartJob(ctx context.Context, args orchestrator.StartJobArgs) (orchestrator.StartJobResult, error) {
	if err := assertPreProd(args.Env); err != nil {
		return orchestrator.StartJobResult{}, err
	}
	processID, err := required("processId", args.ProcessID)
	if err != nil {
		return orchestrator.StartJobResult{}, err
	}
	matchValue := strings.TrimSpace(args.MatchValue)
	if matchValue == "" {
		return orchestrator.StartJobResult{}, shared.Expectedf("matchValue is required for Apex start_job — the credential match value the job runs " +
			"under (for auth-submit portals, the account's automationId; see a queue item's match_value)")
	}
	workflows, err := listWorkflows(ctx, config.PreProd, "")
	if err != nil {
		return orchestrator.StartJobResult{}, err
	}
	var workflow *Workflow
	for i := range workflows {
		if strings.EqualFold(workflows[i].ID, processID) {
			workflow = &workflows[i]
			break
		}
	}
	if workflow == nil {
		return orchestrator.StartJobResult{}, shared.Expectedf("no Process with id '%s' in the pre_prod folder — see list_processes", processID)
	}
	folder, err := ResolveFolderID(ctx, config.PreProd, "")
	if err != nil {
		return orchestrator.StartJobResult{}, err
	}
	body := map[string]any{"workflow_id": workflow.ID, "folder_id": folder.ID, "match_value": matchValue, "bucket_id": nil}
	if args.Input != nil {
		body["input_payload"] = args.Input
	}
	jobs := []orchestrator.StartedJob{}
	for range args.JobsCount {
		raw, err := apexRequest(ctx, config.PreProd, http.MethodPost, "/jobs", reqOpts{Body: body})
		if err != nil {
			if len(jobs) == 0 {
				return orchestrator.StartJobResult{}, err
			}
			ids := make([]string, len(jobs))
			for i, j := range jobs {
				ids[i] = j.ID
			}
			return orchestrator.StartJobResult{}, shared.Expectedf("start_job failed on job %d of %d (not retried); already started: %s — %s",
				len(jobs)+1, args.JobsCount, strings.Join(ids, ", "), err.Error())
		}
		created := shared.SafeJSONParse(string(raw))
		id, _ := shared.StringProp(created, "id")
		status, _ := shared.StringProp(created, "status")
		job := orchestrator.StartedJob{ID: id, State: labelFor(jobLabelOf, status), ProcessName: workflow.Name}
		if id != "" {
			job.Link = DashboardLink("/jobs/" + id)
		}
		jobs = append(jobs, job)
	}
	return orchestrator.StartJobResult{Env: args.Env, Jobs: jobs}, nil
}

// ---- stop_job ---------------------------------------------------------------

type stopRoute struct {
	action    string
	body      map[string]any
	forbidden string
}

var stopRoutes = map[orchestrator.StopStrategy]stopRoute{
	orchestrator.StopSoft: {
		action:    "cancel",
		body:      map[string]any{"force": false},
		forbidden: "this account may lack permission to cancel jobs in the Authorization Test folder",
	},
	orchestrator.StopKill: {
		action:    "force_cancel",
		body:      map[string]any{},
		forbidden: "force_cancel needs the 'jobs:force_cancel' permission, which this account may lack — use SoftStop instead",
	},
}

// StopJob is fetch-first: a job already in a terminal state (completed/failed/cancelled/lost/
// timed_out) has nothing left to stop, so it is refused before any write. The job is read scoped to
// the pre-prod folder, and its own folder_id must equal the pre-prod folder's (client-side proof:
// by-id routes are not verified to be folder-scoped server-side).
func (backend) StopJob(ctx context.Context, args orchestrator.StopJobArgs) (orchestrator.StopJobResult, error) {
	if err := assertPreProd(args.Env); err != nil {
		return orchestrator.StopJobResult{}, err
	}
	jobID, err := required("jobId", args.JobID)
	if err != nil {
		return orchestrator.StopJobResult{}, err
	}
	route, ok := stopRoutes[args.Strategy]
	if !ok {
		return orchestrator.StopJobResult{}, shared.Expectedf("unknown stop strategy '%s'", args.Strategy)
	}
	job, err := getJob(ctx, config.PreProd, "", jobID)
	if err != nil {
		return orchestrator.StopJobResult{}, err
	}
	folder, err := ResolveFolderID(ctx, config.PreProd, "")
	if err != nil {
		return orchestrator.StopJobResult{}, err
	}
	if job.FolderID != folder.ID {
		return orchestrator.StopJobResult{}, shared.Expectedf("refusing to stop job '%s': it is not in the pre_prod folder ('%s') — nothing was stopped", jobID, folder.Name)
	}
	if IsTerminal(job.Status) {
		return orchestrator.StopJobResult{}, shared.Expectedf("refusing to stop job '%s': state is already '%s' (terminal)", jobID, jobLabel(job.Status))
	}
	_, err = apexRequest(ctx, config.PreProd, http.MethodPost, "/jobs/"+url.PathEscape(jobID)+"/"+route.action, reqOpts{Body: route.body})
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status == http.StatusForbidden {
		return orchestrator.StopJobResult{}, shared.Expectedf("Apex refused %s for job '%s' (403): %s", args.Strategy, jobID, route.forbidden)
	}
	if err != nil {
		return orchestrator.StopJobResult{}, err
	}
	return orchestrator.StopJobResult{Env: args.Env, JobID: jobID, Strategy: args.Strategy, Stopped: true}, nil
}
