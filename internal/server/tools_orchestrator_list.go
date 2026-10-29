package server

import (
	"context"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerOrchestratorListTools) }

type listJobsArgs struct {
	Env          config.Env                `json:"env" jsonschema:"Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; Apex prod='Authorization', pre_prod='Authorization Test' (required)"`
	Orchestrator orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	Folder       string                    `json:"folder,omitempty" jsonschema:"Explicit folder name override (wins over env)"`
	Since        string                    `json:"since,omitempty" jsonschema:"ISO date lower bound on creation time"`
	Top          int                       `json:"top,omitempty" jsonschema:"Max jobs to return"`
	ProcessName  string                    `json:"processName,omitempty" jsonschema:"Substring filter on the process name, e.g. 'OPTUM'"`
}

type listJobsOut struct {
	head
	Folder string                 `json:"folder"`
	Count  int                    `json:"count"`
	Jobs   []orchestrator.JobCore `json:"jobs"`
}

type listQueueItemsArgs struct {
	Env          config.Env                   `json:"env" jsonschema:"Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; Apex prod='Authorization', pre_prod='Authorization Test' (required)"`
	Orchestrator orchestrator.Orchestrator    `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	Queue        string                       `json:"queue" jsonschema:"Portal key/alias, e.g. 'PMG', or the orchestrator's queue id (see list_queues)"`
	QueueKind    orchestrator.QueueKind       `json:"queueKind,omitempty" jsonschema:"Which queue for a portal key (default submit)"`
	Status       orchestrator.QueueItemStatus `json:"status,omitempty" jsonschema:"Optional queue-item status filter"`
	Top          int                          `json:"top,omitempty" jsonschema:"Max items to return"`
}

type listQueueItemsOut struct {
	head
	Count int `json:"count"`
	orchestrator.ListQueueItemsResult
}

type listQueuesArgs struct {
	Env          config.Env                `json:"env" jsonschema:"Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; Apex prod='Authorization', pre_prod='Authorization Test' (required)"`
	Orchestrator orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	Folder       string                    `json:"folder,omitempty" jsonschema:"Explicit folder name override (wins over env)"`
	NameContains string                    `json:"nameContains,omitempty" jsonschema:"Substring filter on the queue name"`
	Top          int                       `json:"top,omitempty" jsonschema:"Max queues"`
}

type listQueuesOut struct {
	head
	Count  int                      `json:"count"`
	Queues []orchestrator.QueueCore `json:"queues"`
}

type listProcessesArgs struct {
	Env          config.Env                `json:"env" jsonschema:"Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; Apex prod='Authorization', pre_prod='Authorization Test' (required)"`
	Orchestrator orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	Folder       string                    `json:"folder,omitempty" jsonschema:"Explicit folder name override (wins over env)"`
	NameContains string                    `json:"nameContains,omitempty" jsonschema:"Substring filter on the process name"`
	Top          int                       `json:"top,omitempty" jsonschema:"Max processes"`
}

type listProcessesOut struct {
	head
	Count     int                        `json:"count"`
	Processes []orchestrator.ProcessCore `json:"processes"`
}

type listTriggersArgs struct {
	Env          config.Env                `json:"env" jsonschema:"Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; Apex prod='Authorization', pre_prod='Authorization Test' (required)"`
	Orchestrator orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	Folder       string                    `json:"folder,omitempty" jsonschema:"Explicit folder name override (wins over env)"`
	QueueID      string                    `json:"queueId,omitempty" jsonschema:"Only triggers watching this queue"`
	Top          int                       `json:"top,omitempty" jsonschema:"Max triggers"`
}

type listTriggersOut struct {
	head
	Count    int                        `json:"count"`
	Triggers []orchestrator.TriggerCore `json:"triggers"`
}

type faultedJobIssueArgs struct {
	Env          config.Env                `json:"env" jsonschema:"Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; Apex prod='Authorization', pre_prod='Authorization Test' (required)"`
	Orchestrator orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	JobID        string                    `json:"jobId" jsonschema:"The faulted job's id (from list_jobs)"`
	Folder       string                    `json:"folder,omitempty" jsonschema:"Explicit folder name override (wins over env)"`
	Repo         string                    `json:"repo,omitempty" jsonschema:"Target repo owner/name (default Apex-Medical-AI-Inc/RPAPlaywright)"`
	Labels       []string                  `json:"labels,omitempty" jsonschema:"Issue labels (default uipath-fault, bug)"`
	Refresh      bool                      `json:"refresh,omitempty" jsonschema:"Bypass cached results and re-fetch from upstream; the fresh result replaces the cached entry. Use when a cached terminal job looks stale."`
}

type faultedJobIssueOut struct {
	head
	orchestrator.FaultedJobIssueResult
}

func registerOrchestratorListTools(s *Server) {
	addTool(s, &mcp.Tool{
		Name:        "list_queue_items",
		Title:       "Browse a queue",
		Annotations: readOnlyHints(true),
		Description: "List items in a portal's queue for triage, newest first. `queue` is a portal key/alias " +
			"(e.g. 'PMG', 'MOLINA' — resolved to that portal's submit or sync queue) or the " +
			"orchestrator's own queue id (from list_queues; required for pre_prod UiPath queues, whose " +
			"ids differ from the static portal registry). Optionally filter by status. READ-ONLY, " +
			"PHI-light projection (specificContent is null; member id/name live under each item's " +
			"details). Returns {orchestrator, env, queueId, queueName, count, items:[{orchestrator," +
			"queueId,queueName,itemId,reference,orderUid,status,rawStatus,exceptionType,createdAt," +
			"endedAt,jobs,specificContent:null,details}]}.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listQueueItemsArgs) (any, error) {
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		res, err := withHeartbeat(ctx, req, "Orchestrator", heartbeatEvery, func() (orchestrator.ListQueueItemsResult, error) {
			return b.ListQueueItems(ctx, orchestrator.ListQueueItemsArgs{
				Env: in.Env, Queue: in.Queue, QueueKind: in.QueueKind, Status: in.Status, Top: in.Top,
			})
		})
		if err != nil {
			return nil, err
		}
		return listQueueItemsOut{head{in.Orchestrator, in.Env}, len(res.Items), res}, nil
	},
		envEnum(), orchestratorEnum(), MinLen("queue", 1),
		Enum("queueKind", orchestrator.QueueSubmit, orchestrator.QueueSync), Default("queueKind", orchestrator.QueueSubmit),
		Enum("status", orchestrator.QueueItemStatuses...),
		Range("top", 1, 200), Default("top", 50),
	)

	addTool(s, &mcp.Tool{
		Name:        "list_jobs",
		Title:       "List recent orchestrator jobs",
		Annotations: readOnlyHints(true),
		Description: "List the most recent jobs in an env's folder (newest first), without order correlation. " +
			"READ-ONLY. To diagnose a job from the list, call get_job with includeLogDigest=true " +
			"(condensed failure digest + fault signature) — get_job also takes a jobIds array to " +
			"diagnose several jobs from this list in one call. Returns {orchestrator, env, folder, " +
			"count, jobs:[{orchestrator,id,state (dashboard label),rawState (the backend's own status " +
			"string),terminal,process:{id,name,version},queueItem:{queueId,itemId,reference}|null," +
			"createdAt,startedAt,endedAt,durationMs,attempt,retryOfJobId,fault,link (dashboard deep " +
			"link),details}]}.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listJobsArgs) (any, error) {
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		res, err := withHeartbeat(ctx, req, "Orchestrator", heartbeatEvery, func() (orchestrator.ListJobsResult, error) {
			return b.ListJobs(ctx, orchestrator.ListJobsArgs{
				Env: in.Env, Folder: in.Folder, Since: in.Since, Top: in.Top, ProcessName: in.ProcessName,
			})
		})
		if err != nil {
			return nil, err
		}
		return listJobsOut{head{in.Orchestrator, in.Env}, res.Folder, len(res.Jobs), res.Jobs}, nil
	},
		envEnum(), orchestratorEnum(), Range("top", 1, 500), Default("top", 50),
	)

	addTool(s, &mcp.Tool{
		Name:        "build_faulted_job_issue",
		Title:       "Build a GitHub issue payload for a faulted job",
		Annotations: readOnlyHints(true),
		Description: "Read a faulted job (by its id) and its logs, and BUILD a ready-to-post GitHub issue " +
			"payload for the RPA repo. READ-ONLY: this does NOT post to GitHub — it returns " +
			"{orchestrator,env,repo,title,body,labels,faultSignature,searchQuery,recurrenceComment," +
			"found,logsError?}. The logs fetch is best-effort: a failure surfaces as logsError (issue " +
			"still built from the job alone) instead of failing the whole call. The caller (the " +
			"report-faulted-jobs prompt) hands this to the host's GitHub MCP server: search " +
			"with `searchQuery`, then add `recurrenceComment` to the matching open issue, or " +
			"create_issue with title/body/labels. `faultSignature` is the normalized error (stable " +
			"across reruns) used to dedupe.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in faultedJobIssueArgs) (any, error) {
		ctx = memo.RefreshIf(ctx, in.Refresh)
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		res, err := b.BuildFaultedJobIssue(ctx, orchestrator.FaultIssueArgs{
			Env: in.Env, JobID: in.JobID, Folder: in.Folder, Repo: in.Repo, Labels: in.Labels,
		})
		if err != nil {
			return nil, err
		}
		return faultedJobIssueOut{head{in.Orchestrator, in.Env}, res}, nil
	},
		envEnum(), orchestratorEnum(), MinLen("jobId", 1), Default("refresh", false),
	)

	addTool(s, &mcp.Tool{
		Name:        "list_queues",
		Title:       "List queues in a folder",
		Annotations: readOnlyHints(true),
		Description: "List the queues (the queues themselves) in an env's folder, sorted by name. READ-ONLY. Use " +
			"this to discover a queue id for list_queue_items / pull_queue_item and the exact queue " +
			"name for add_queue_item — pre_prod queue ids differ from the prod ids in the static portal " +
			"registry. Queue CREATION is deliberately not a tool (rare one-time setup — do it in the " +
			"orchestrator UI). Returns {orchestrator, env, count, queues:[{orchestrator,id,name," +
			"processId,active,details}]}.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listQueuesArgs) (any, error) {
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		queues, err := b.ListQueues(ctx, orchestrator.ListQueuesArgs{
			Env: in.Env, Folder: in.Folder, NameContains: in.NameContains, Top: in.Top,
		})
		if err != nil {
			return nil, err
		}
		return listQueuesOut{head{in.Orchestrator, in.Env}, len(queues), queues}, nil
	},
		envEnum(), orchestratorEnum(), Range("top", 1, 200), Default("top", 100),
	)

	addTool(s, &mcp.Tool{
		Name:        "list_processes",
		Title:       "List processes in a folder",
		Annotations: readOnlyHints(true),
		Description: "List the processes (UiPath releases / Apex workflows) in an env's folder, sorted by name. " +
			"READ-ONLY. `id` is what start_job takes as processId; `version` is the pinned package " +
			"version — a UiPath job resolves it at START, so verify the pin here right before starting " +
			"(repinning is manual in the orchestrator UI; this MCP never repins). Returns " +
			"{orchestrator, env, count, processes:[{orchestrator,id,name,package,version,active,details}]}.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listProcessesArgs) (any, error) {
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		processes, err := b.ListProcesses(ctx, orchestrator.ListProcessesArgs{
			Env: in.Env, Folder: in.Folder, NameContains: in.NameContains, Top: in.Top,
		})
		if err != nil {
			return nil, err
		}
		return listProcessesOut{head{in.Orchestrator, in.Env}, len(processes), processes}, nil
	},
		envEnum(), orchestratorEnum(), Range("top", 1, 200), Default("top", 100),
	)

	addTool(s, &mcp.Tool{
		Name:        "list_triggers",
		Title:       "List triggers (queue + time) in a folder",
		Annotations: readOnlyHints(true),
		Description: "List the triggers (queue + time schedules) in an env's folder. READ-ONLY. Queue triggers " +
			"carry a queueId + the processId they start — use this to verify a queue's trigger points " +
			"at YOUR pre-prod process before enqueueing (cross-check processId against list_processes). " +
			"Time triggers have queueId null and a cron. Returns {orchestrator, env, count, " +
			"triggers:[{orchestrator,id,name,kind:'queue'|'time',processId,queueId,enabled,cron," +
			"details}]}.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTriggersArgs) (any, error) {
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		triggers, err := b.ListTriggers(ctx, orchestrator.ListTriggersArgs{
			Env: in.Env, Folder: in.Folder, QueueID: in.QueueID, Top: in.Top,
		})
		if err != nil {
			return nil, err
		}
		return listTriggersOut{head{in.Orchestrator, in.Env}, len(triggers), triggers}, nil
	},
		envEnum(), orchestratorEnum(), MinLen("queueId", 1), Range("top", 1, 200), Default("top", 100),
	)
}
