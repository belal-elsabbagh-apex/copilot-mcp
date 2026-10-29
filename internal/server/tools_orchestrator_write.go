package server

import (
	"context"
	"strconv"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerOrchestratorWriteTools) }

// Every write is pre_prod-only: the schema refuses prod (preProdOnly), the backend asserts it
// again, and each write is logged to the client as an audit trail.

type addQueueItemArgs struct {
	Env             config.Env                     `json:"env" jsonschema:"Writes are pre_prod-only (UiPath: the 'Authorization Dev Clone' folder; Apex: the 'Authorization Test' folder); 'prod' is rejected by the schema (required)"`
	Orchestrator    orchestrator.Orchestrator      `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	Queue           string                         `json:"queue" jsonschema:"UiPath: exact queue Name in the dev-clone folder. Apex: queue name or uuid in the Authorization Test folder (see list_queues)"`
	Reference       string                         `json:"reference" jsonschema:"Queue-item Reference, e.g. '<PORTAL>-DEVTEST-<n>-<orderUid8>'"`
	SpecificContent map[string]any                 `json:"specificContent" jsonschema:"The item's SpecificContent (fixture record or build_queue_item output)"`
	Priority        orchestrator.QueueItemPriority `json:"priority,omitempty"`
}

type addQueueItemOut struct {
	Orchestrator orchestrator.Orchestrator `json:"orchestrator"`
	orchestrator.AddQueueItemResult
}

type deleteQueueItemArgs struct {
	Env          config.Env                `json:"env" jsonschema:"Writes are pre_prod-only (UiPath: the 'Authorization Dev Clone' folder; Apex: the 'Authorization Test' folder); 'prod' is rejected by the schema (required)"`
	Orchestrator orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	ItemID       string                    `json:"itemId" jsonschema:"Queue item id (UiPath: a positive integer; from list_queue_items)"`
	QueueID      string                    `json:"queueId,omitempty" jsonschema:"Queue uuid the item sits in — REQUIRED for Apex, ignored by UiPath"`
}

type deleteQueueItemOut struct {
	Orchestrator orchestrator.Orchestrator `json:"orchestrator"`
	orchestrator.DeleteQueueItemResult
}

type startJobArgs struct {
	Env          config.Env                `json:"env" jsonschema:"Writes are pre_prod-only (UiPath: the 'Authorization Dev Clone' folder; Apex: the 'Authorization Test' folder); 'prod' is rejected by the schema (required)"`
	Orchestrator orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	ProcessID    string                    `json:"processId" jsonschema:"Process id (from list_processes)"`
	Input        map[string]any            `json:"input,omitempty" jsonschema:"Job input arguments (omit for none)"`
	JobsCount    int                       `json:"jobsCount,omitempty" jsonschema:"Jobs to start"`
	MatchValue   string                    `json:"matchValue,omitempty" jsonschema:"Apex: REQUIRED credential match value (the account's automationId for auth-submit portals). UiPath: ignored"`
}

type startJobOut struct {
	Orchestrator orchestrator.Orchestrator `json:"orchestrator"`
	orchestrator.StartJobResult
}

type stopJobArgs struct {
	Env          config.Env                `json:"env" jsonschema:"Writes are pre_prod-only (UiPath: the 'Authorization Dev Clone' folder; Apex: the 'Authorization Test' folder); 'prod' is rejected by the schema (required)"`
	Orchestrator orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	JobID        string                    `json:"jobId" jsonschema:"Job id (from start_job / list_jobs / get_job)"`
	Strategy     orchestrator.StopStrategy `json:"strategy,omitempty" jsonschema:"SoftStop = graceful (default), Kill = immediate"`
}

type stopJobOut struct {
	Orchestrator orchestrator.Orchestrator `json:"orchestrator"`
	orchestrator.StopJobResult
}

func registerOrchestratorWriteTools(s *Server) {
	addTool(s, &mcp.Tool{
		Name:  "add_queue_item",
		Title: "Post a queue item to a pre-prod queue",
		// additive — creates an item, never overwrites; each call mints a new item
		Annotations: writeHints(false, false),
		Description: "POST one item to a queue in the pre-prod test folder (pre_prod ONLY — the schema rejects " +
			"prod; UiPath: 'Authorization Dev Clone', Apex: 'Authorization Test'). Every payload passes " +
			"the test-safety guard first: IsApproved is forced false; a non-empty serverURL/queueUrl " +
			"must match the config's pre-prod values (the chosen orchestrator's section); <TO-FILL> " +
			"placeholders are rejected. `queue`: UiPath = the exact queue Name; Apex = the queue name " +
			"(exact, case-insensitive) or uuid, resolved in the Authorization Test folder (see " +
			"list_queues). Apex derives match_value from specificContent.automationId (required) and " +
			"uses the queue's default priority (`priority` is ignored). To post a build_queue_item " +
			"result: UiPath — payload.itemData.Name as queue, .Reference as reference, .SpecificContent " +
			"as specificContent; Apex — meta.queueName as queue, payload.body.reference as reference, " +
			"payload.body.specific_content as specificContent. When enqueueing many items, pace ~1/s. " +
			"Returns {orchestrator, env, queueId, queueName, reference, itemId, status, forced}.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addQueueItemArgs) (any, error) {
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		res, err := b.AddQueueItem(ctx, orchestrator.AddQueueItemArgs{
			Env: in.Env, Queue: in.Queue, Reference: in.Reference, SpecificContent: in.SpecificContent, Priority: in.Priority,
		})
		if err != nil {
			return nil, err
		}
		// Audit trail: surface every orchestrator write to the client log.
		s.mcpLog("warning", "posted queue item to pre-prod queue '"+in.Queue+"'", map[string]any{
			"orchestrator": in.Orchestrator, "reference": in.Reference, "itemId": res.ItemID, "forced": res.Forced,
		})
		return addQueueItemOut{in.Orchestrator, res}, nil
	},
		preProdOnly(), orchestratorEnum(), MinLen("queue", 1), MinLen("reference", 1),
		Enum("priority", orchestrator.PriorityLow, orchestrator.PriorityNormal, orchestrator.PriorityHigh),
		Default("priority", orchestrator.PriorityNormal),
	)

	addTool(s, &mcp.Tool{
		Name:  "delete_queue_item",
		Title: "Delete a New queue item from the pre-prod folder",
		// deletes an item; idempotent because re-deleting fails safe (status is no longer New)
		Annotations: writeHints(true, true),
		Description: "Delete one queue item from the pre-prod test folder (pre_prod ONLY — the schema rejects " +
			"prod). Fetch-first: refuses unless the item's current status is New (Apex 'new'), so " +
			"InProgress/Successful/Failed history is never destroyed. `queueId` is REQUIRED for Apex " +
			"(from list_queue_items / find_order_queue_items) and ignored by UiPath. Use to clear stale " +
			"test items before re-enqueueing after a re-mint. Returns " +
			"{orchestrator, env, itemId, deleted, previousStatus, reference}.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteQueueItemArgs) (any, error) {
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		res, err := b.DeleteQueueItem(ctx, orchestrator.DeleteQueueItemArgs{Env: in.Env, ItemID: in.ItemID, QueueID: in.QueueID})
		if err != nil {
			return nil, err
		}
		s.mcpLog("warning", "deleted pre-prod queue item "+in.ItemID, map[string]any{
			"orchestrator": in.Orchestrator, "reference": res.Reference,
		})
		return deleteQueueItemOut{in.Orchestrator, res}, nil
	},
		preProdOnly(), orchestratorEnum(), MinLen("itemId", 1), MinLen("queueId", 1),
	)

	addTool(s, &mcp.Tool{
		Name:  "start_job",
		Title: "Start a job in the pre-prod test folder",
		// starts a run; existing state untouched, but each call starts new job(s)
		Annotations: writeHints(false, false),
		Description: "Start job(s) for a Process in the pre-prod test folder (pre_prod ONLY — the schema " +
			"rejects prod). Get processId from list_processes. UiPath: PIN RACE — a job resolves its " +
			"package version at START, so verify the release's version via list_processes right " +
			"before starting (repinning is manual; this MCP never repins). Apex: matchValue is " +
			"REQUIRED (the credential match value the job runs under — for auth-submit portals the " +
			"account's automationId); the job runs the Process's current_version at dispatch; the " +
			"jobsCount POSTs are sequential and never retried, and a mid-loop failure names the jobs " +
			"already started. A started job performs a real (dry-run) portal login. Poll with get_job. " +
			"Returns {orchestrator, env, jobs:[{id,state,processName,link}]}.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in startJobArgs) (any, error) {
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		res, err := b.StartJob(ctx, orchestrator.StartJobArgs{
			Env: in.Env, ProcessID: in.ProcessID, Input: in.Input, JobsCount: in.JobsCount, MatchValue: in.MatchValue,
		})
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(res.Jobs))
		for i, j := range res.Jobs {
			ids[i] = j.ID
		}
		s.mcpLog("warning", "started "+strconv.Itoa(len(res.Jobs))+" pre-prod job(s)", map[string]any{
			"orchestrator": in.Orchestrator, "processId": in.ProcessID, "ids": ids,
		})
		return startJobOut{in.Orchestrator, res}, nil
	},
		preProdOnly(), orchestratorEnum(), MinLen("processId", 8), MinLen("matchValue", 1),
		OnProp("input", func(p *jsonschema.Schema) { p.Default = mustMarshal(map[string]any{}) }),
		Range("jobsCount", 1, 5), Default("jobsCount", 1),
	)

	addTool(s, &mcp.Tool{
		Name:  "stop_job",
		Title: "Stop or kill a running job in the pre-prod folder",
		// aborts a run in progress; idempotent because it is fetch-first and refuses (no write) once terminal
		Annotations: writeHints(true, true),
		Description: "Stop (SoftStop, graceful) or Kill (immediate) a job in the pre-prod test folder " +
			"(pre_prod ONLY — the schema rejects prod). Takes the job id from start_job / list_jobs / " +
			"get_job; it is resolved scoped to the pre-prod folder, so a prod job's id resolves to " +
			"nothing. Fetch-first: refuses a job already in a terminal state (UiPath Successful/" +
			"Faulted/Stopped; Apex completed/failed/cancelled/lost/timed_out). Apex Kill uses " +
			"force_cancel, which needs the 'jobs:force_cancel' permission (a 403 is reported as such). " +
			"Returns {orchestrator, env, jobId, strategy, stopped}.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in stopJobArgs) (any, error) {
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		res, err := b.StopJob(ctx, orchestrator.StopJobArgs{Env: in.Env, JobID: in.JobID, Strategy: in.Strategy})
		if err != nil {
			return nil, err
		}
		s.mcpLog("warning", "stopped pre-prod job "+in.JobID+" ("+string(in.Strategy)+")", map[string]any{
			"orchestrator": in.Orchestrator,
		})
		return stopJobOut{in.Orchestrator, res}, nil
	},
		preProdOnly(), orchestratorEnum(), MinLen("jobId", 8),
		Enum("strategy", orchestrator.StopSoft, orchestrator.StopKill), Default("strategy", orchestrator.StopSoft),
	)
}
