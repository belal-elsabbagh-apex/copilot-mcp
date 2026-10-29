package server

import (
	"context"
	"fmt"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/apex"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerOrchestratorJobTools) }

// maxJobIDs is the jobIds batch ceiling both job tools advertise.
const maxJobIDs = 25

// maxDownloads is the ceiling on named artifacts per get_job_artifacts download.
const maxDownloads = 50

type getJobArgs struct {
	Env              config.Env                `json:"env" jsonschema:"Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; Apex prod='Authorization', pre_prod='Authorization Test' (required)"`
	Orchestrator     orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	JobID            string                    `json:"jobId,omitempty" jsonschema:"The job's id (from start_job / list_jobs). Provide this OR jobIds, not both"`
	JobIDs           []string                  `json:"jobIds,omitempty" jsonschema:"Batch: diagnose several jobs in one call. Provide this OR jobId, not both"`
	Folder           string                    `json:"folder,omitempty" jsonschema:"Explicit folder name override (wins over env)"`
	IncludeLogDigest bool                      `json:"includeLogDigest,omitempty" jsonschema:"Also fetch each job's logs (extra call per job) and return a condensed failure digest + structured fault — use get_job_logs for the complete logs"`
	Refresh          bool                      `json:"refresh,omitempty" jsonschema:"Bypass cached results and re-fetch from upstream; the fresh result replaces the cached entry. Use when a cached terminal job looks stale."`
}

type getJobLogsArgs struct {
	Env              config.Env                `json:"env" jsonschema:"Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; Apex prod='Authorization', pre_prod='Authorization Test' (required)"`
	Orchestrator     orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	JobID            string                    `json:"jobId,omitempty" jsonschema:"The job's id (from find_order_queue_items / list_jobs). Provide this OR jobIds, not both"`
	JobIDs           []string                  `json:"jobIds,omitempty" jsonschema:"Batch: fetch logs for several jobs in one call. Provide this OR jobId, not both"`
	Folder           string                    `json:"folder,omitempty" jsonschema:"Explicit folder name override (wins over env)"`
	IncludeVideo     bool                      `json:"includeVideo,omitempty" jsonschema:"Also fetch the job's video recording (extra round-trip)"`
	MinLevel         orchestrator.LogLevel     `json:"minLevel,omitempty" jsonschema:"Only logs at or above this level (warn=Warn/Error/Fatal, error=Error/Fatal). Default: all levels"`
	Contains         string                    `json:"contains,omitempty" jsonschema:"Only logs whose Message contains this substring (case-sensitive)"`
	OnlyFailures     bool                      `json:"onlyFailures,omitempty" jsonschema:"Semantic failure filter: keep only logs at error/fatal level OR whose message mentions failure indicators (exception, failed, timeout, 'unable to', denied, …) regardless of level. Catches failures logged at Info"`
	Tail             *int                      `json:"tail,omitempty" jsonschema:"Only the last N matching logs (still returned oldest first)"`
	FullMessages     bool                      `json:"fullMessages,omitempty" jsonschema:"Return full untruncated Message text (default: truncated to 400 chars, matching get_job's log digest). Use when you need a complete stack trace."`
	IncludeRawFields bool                      `json:"includeRawFields,omitempty"`
	Refresh          bool                      `json:"refresh,omitempty" jsonschema:"Bypass cached results and re-fetch from upstream; the fresh result replaces the cached entry. Use when a cached terminal job looks stale."`
}

type getJobArtifactsArgs struct {
	Env      config.Env `json:"env" jsonschema:"Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; Apex prod='Authorization', pre_prod='Authorization Test' (required)"`
	JobID    string     `json:"jobId" jsonschema:"The Apex job id (from get_job / find_order_queue_items)"`
	Folder   string     `json:"folder,omitempty" jsonschema:"Explicit folder name override for the listing (wins over env)"`
	Download any        `json:"download,omitempty" jsonschema:"Artifact filenames to download (e.g. [\"trace.zip\"]) or \"all\". Omit to only list."`
	Refresh  bool       `json:"refresh,omitempty" jsonschema:"Bypass cached results and re-fetch from upstream; the fresh result replaces the cached entry. Use when a cached terminal job looks stale. Downloads: re-download even when the local file's sidecar matches."`
}

type artifactListing struct {
	Env       config.Env          `json:"env"`
	Folder    string              `json:"folder"`
	Artifacts []apex.ArtifactInfo `json:"artifacts"`
}

type artifactDownload struct {
	Env   config.Env                `json:"env"`
	Dir   string                    `json:"dir"`
	Files []apex.DownloadedArtifact `json:"files"`
}

// downloadRequest decodes the `download` union ("all" | non-empty filename list). The schema has
// already validated its shape; a miss here means the client skipped validation.
func downloadRequest(v any) (names []string, all bool, err error) {
	switch d := v.(type) {
	case string:
		return nil, true, nil
	case []any:
		for _, n := range d {
			name, ok := n.(string)
			if !ok {
				return nil, false, fmt.Errorf("download must be \"all\" or a list of artifact filenames")
			}
			names = append(names, name)
		}
		return names, false, nil
	}
	return nil, false, fmt.Errorf("download must be \"all\" or a list of artifact filenames")
}

func registerOrchestratorJobTools(s *Server) {
	addTool(s, &mcp.Tool{
		Name:        "get_job_logs",
		Title:       "Get a job's robot logs",
		Annotations: readOnlyHints(true),
		Description: "Fetch execution logs (oldest first, capped 500 per job) for a job by its id, optionally " +
			"with the video recording. Pass jobIds (up to 25) instead of jobId to fetch logs for several " +
			"jobs in ONE call — use this instead of looping get_job_logs once per job. All row filters " +
			"are opt-in and apply to every job in the batch — by default the full (capped) log set is " +
			"returned, but each Message is truncated to 400 chars (pass fullMessages=true for the " +
			"untruncated text, e.g. to read a complete stack trace). READ-ONLY. A jobIds batch never " +
			"fails as a whole on one job's error — that job's entry carries error instead, the rest " +
			"still return. `video` is always best-effort: a failure there surfaces as videoError, never " +
			"blocks the logs; it is {kind:'url',url} when the orchestrator serves a link, or " +
			"{kind:'artifact',artifact,sizeBytes,fetchWith:'get_job_artifacts'} when the recording is a " +
			"job artifact to download. A job whose logs cannot be read carries logsUnavailable " +
			"{reason: running | never-started | artifact-policy, detail}. includeRawFields=true adds " +
			"each row's backend-specific structured fields as `raw` (UiPath: the parsed RawMessage — " +
			"transactionId, queueName, processingExceptionType/Reason, execution timings, any custom " +
			"Add-Log-Fields data, ~3x the response size on real jobs; Apex: the bot's NDJSON `fields`); " +
			"`raw` is null when a row has none. Returns {orchestrator, env, folder, jobId, logs:[{Level," +
			"Message,TimeStamp,raw?}], returned, totalMatching?, truncated, logsUnavailable?, video?, " +
			"videoError?, error?} for a single jobId, or {orchestrator, env, folder, count, jobs:[{jobId," +
			"logs,returned,totalMatching?,truncated,logsUnavailable?,error?,video?,videoError?}]} for jobIds.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in getJobLogsArgs) (any, error) {
		ctx = memo.RefreshIf(ctx, in.Refresh)
		ids, err := orchestrator.ResolveJobIDs(in.JobID, in.JobIDs)
		if err != nil {
			return nil, err
		}
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		res, err := b.GetJobLogs(ctx, orchestrator.GetJobLogsArgs{
			Env: in.Env, JobIDs: ids.IDs, Batch: ids.Batch, Folder: in.Folder,
			Filter:       orchestrator.LogFilter{MinLevel: in.MinLevel, Contains: in.Contains, OnlyFailures: in.OnlyFailures, Tail: in.Tail},
			FullMessages: in.FullMessages, IncludeRawFields: in.IncludeRawFields, IncludeVideo: in.IncludeVideo,
		}, stepProgress(ctx, req))
		if err != nil {
			return nil, err
		}
		return orchestrator.JobEntriesEnvelope(in.Orchestrator, in.Env, res.Folder, ids.Batch, res.Entries), nil
	},
		envEnum(), orchestratorEnum(),
		MinLen("jobId", 1), Default("includeVideo", false), Default("onlyFailures", false),
		Default("fullMessages", false), Default("includeRawFields", false), Default("refresh", false),
		MinItems("jobIds", 1), OnProp("jobIds", func(p *jsonschema.Schema) { p.MaxItems = new(maxJobIDs) }),
		ItemsOf("jobIds", func(p *jsonschema.Schema) { p.MinLength = new(1) }),
		Enum("minLevel", orchestrator.LevelWarn, orchestrator.LevelError),
		MinLen("contains", 1),
		// The description quotes `raw` in backticks, which a struct tag cannot hold.
		describe("includeRawFields", "Attach each row's backend-specific structured fields as `raw` — see the tool "+
			"description. Off by default: UiPath's parsed RawMessage is ~3x the response size."),
		OnProp("tail", func(p *jsonschema.Schema) { positiveInt()(p); p.Maximum = new(500.0) }),
	)

	addTool(s, &mcp.Tool{
		Name:        "get_job",
		Title:       "Get a single job's state by id",
		Annotations: readOnlyHints(true),
		Description: "Fetch one job by its id — the light poll target after start_job, and the job-first " +
			"diagnostic when you have an id but no orderUid (list_jobs is an unkeyed scan; " +
			"find_order_queue_items is order-correlated). Pass jobIds (up to 25) instead of jobId to " +
			"diagnose several jobs — e.g. every job list_jobs just returned — in ONE call instead of " +
			"looping get_job per id. READ-ONLY. `job` is the normalized job (state = dashboard label, " +
			"rawState = the backend's own status, terminal, process, queueItem, timings, attempt, " +
			"retryOfJobId, fault, link, details — UiPath details carry the parsed output with " +
			"token/callbackContext stripped). With includeLogDigest=true it also returns the log digest " +
			"(failure-focused condensed logs + stalls) and a structured fault (headline error, stable " +
			"signature, class); UiPath adds transactionOutcome — the queue transaction's OWN " +
			"processingExceptionType/Reason and timing from the Transaction-Ended log line, more " +
			"reliable for exception classification than the free-text-derived class, null when the job " +
			"never reached one; Apex adds run {exitCode, outcome, errorClass, timedOut, cancelled}. " +
			"When a job has no readable log, logsUnavailable {reason: running | never-started | " +
			"artifact-policy, detail} says why. If the digest is not enough, call get_job_logs (also " +
			"batchable via jobIds) for the complete logs. A jobIds batch never fails as a whole on one " +
			"id's error — that entry carries error instead, the rest still return; a single jobId " +
			"fails the call normally, like any other error. includeLogDigest is always best-effort: a " +
			"failure fetching it surfaces as logDigestError, never blocks the job data. Returns " +
			"{orchestrator, env, folder, id, found, job?, logDigest?, logsUnavailable?, logDigestError?, " +
			"error?} for a single jobId, or {orchestrator, env, folder, count, jobs:[{id,found,job?," +
			"logDigest?,logsUnavailable?,logDigestError?,error?}]} for jobIds.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in getJobArgs) (any, error) {
		ctx = memo.RefreshIf(ctx, in.Refresh)
		ids, err := orchestrator.ResolveJobIDs(in.JobID, in.JobIDs)
		if err != nil {
			return nil, err
		}
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		// With includeLogDigest a 25-id batch is 50 orchestrator calls — the backend counts
		// them off through stepProgress as each id finishes.
		res, err := b.GetJobs(ctx, orchestrator.GetJobsArgs{
			Env: in.Env, JobIDs: ids.IDs, Batch: ids.Batch, Folder: in.Folder, IncludeLogDigest: in.IncludeLogDigest,
		}, stepProgress(ctx, req))
		if err != nil {
			return nil, err
		}
		return orchestrator.JobEntriesEnvelope(in.Orchestrator, in.Env, res.Folder, ids.Batch, res.Entries), nil
	},
		envEnum(), orchestratorEnum(),
		MinLen("jobId", 8), Default("includeLogDigest", false), Default("refresh", false),
		MinItems("jobIds", 1), OnProp("jobIds", func(p *jsonschema.Schema) { p.MaxItems = new(maxJobIDs) }),
		ItemsOf("jobIds", func(p *jsonschema.Schema) { p.MinLength = new(8) }),
	)

	addTool(s, &mcp.Tool{
		Name:        "get_job_artifacts",
		Title:       "List or download an Apex job's artifacts",
		Annotations: readOnlyHints(true),
		Description: "List an Apex job's artifacts ({filename, type, sizeBytes}: job.log, bot.log, debug.json, " +
			"result.json, screenshots, trace.zip, recording.mp4), or download chosen ones with `download` " +
			"(artifact filenames, or \"all\") to ~/.cache/copilot-mcp/artifacts/<jobId>/ and get back the " +
			"local paths + sizes. Apex-only (UiPath has no artifacts) — no `orchestrator` argument. " +
			"READ-ONLY on Apex; only local files are written. PHI WARNING: screenshots, trace.zip, " +
			"recordings and logs can contain member data — they are written OUTSIDE any repo with " +
			"owner-only permissions; never commit, attach or paste them. An unknown filename fails with " +
			"the list of available names. For parsed, filterable logs use get_job_logs instead; use this " +
			"for trace.zip, screenshots, recordings and raw files. Returns {env,folder,artifacts[]} " +
			"when listing, {env,dir,files:[{filename,path,sizeBytes}]} when downloading.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in getJobArtifactsArgs) (any, error) {
		ctx = memo.RefreshIf(ctx, in.Refresh)
		if in.Download == nil {
			return withHeartbeat(ctx, req, "Apex", heartbeatEvery, func() (artifactListing, error) {
				folder, artifacts, err := apex.ListJobArtifacts(ctx, in.Env, in.JobID, in.Folder)
				return artifactListing{in.Env, folder, artifacts}, err
			})
		}
		names, all, err := downloadRequest(in.Download)
		if err != nil {
			return nil, err
		}
		return withHeartbeat(ctx, req, "Apex", heartbeatEvery, func() (artifactDownload, error) {
			dir, files, err := apex.DownloadJobArtifacts(ctx, in.Env, in.JobID, names, all, "", in.Folder)
			return artifactDownload{in.Env, dir, files}, err
		})
	},
		envEnum(), MinLen("jobId", 8), Default("refresh", false),
		OnProp("download", func(p *jsonschema.Schema) {
			p.AnyOf = []*jsonschema.Schema{
				{Type: "string", Const: new(any("all"))},
				{Type: "array", Items: &jsonschema.Schema{Type: "string", MinLength: new(1)}, MinItems: new(1), MaxItems: new(maxDownloads)},
			}
		}),
	)
}
