package uipath

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// The UiPath implementation of orchestrator.Backend: the handler logic returning the
// backend-neutral cores. Reads live here and in backend_queues.go; writes translate onto
// actions.go in backend_writes.go.

// Backend is the UiPath orchestrator.Backend; internal/backends registers it.
var Backend orchestrator.Backend = backend{}

type backend struct{}

func (backend) Name() orchestrator.Orchestrator { return orchestrator.UiPath }

// ---- jobs ------------------------------------------------------------------

func (backend) ListJobs(ctx context.Context, args orchestrator.ListJobsArgs) (orchestrator.ListJobsResult, error) {
	folder, err := ResolveFolder(args.Env, args.Folder)
	if err != nil {
		return orchestrator.ListJobsResult{}, err
	}
	jobs, err := ListRecentJobs(ctx, args.Since, args.Top, folder, args.ProcessName)
	if err != nil {
		return orchestrator.ListJobsResult{}, err
	}
	cores := make([]orchestrator.JobCore, len(jobs))
	for i, j := range jobs {
		cores[i] = toJobCore(j, jobDeepLink(j.Key), nil)
	}
	return orchestrator.ListJobsResult{Folder: folder, Jobs: cores}, nil
}

// parseJobOutput is the job's parsed OutputArguments with token/callbackContext stripped
// (normalizeOutputFields); unparseable output degrades to its first 500 characters; none at all
// is nil.
func parseJobOutput(job Job) any {
	if job.OutputArguments == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(job.OutputArguments), &parsed); err != nil || parsed == nil {
		return shared.SliceUTF16(job.OutputArguments, 500)
	}
	if obj, ok := parsed.(map[string]any); ok {
		return normalizeOutputFields(obj)
	}
	return orchestrator.Fields{}
}

// jobEntry builds one id's entry. A single explicit id fails like any other tool error; a batch
// keeps going and attaches the failure to just this entry. With IncludeLogDigest the logs are a
// best-effort attachment: a fetch failure lands on LogDigestError and leaves Fault nil (a fault
// synthesized from zero logs would be a state-only signature that mis-dedupes).
func jobEntry(ctx context.Context, id string, lookup JobLookup, args orchestrator.GetJobsArgs, folder string) (orchestrator.JobEntry, error) {
	if lookup.Err != nil && !args.Batch {
		return orchestrator.JobEntry{}, lookup.Err
	}
	if lookup.Err != nil {
		return orchestrator.JobEntry{ID: id, Error: lookup.Err.Error()}, nil
	}
	if lookup.Job == nil {
		return orchestrator.JobEntry{ID: id}, nil
	}
	job := *lookup.Job
	output := parseJobOutput(job)
	core := toJobCore(job, jobDeepLink(id), &output)
	if !args.IncludeLogDigest {
		return orchestrator.JobEntry{ID: id, Found: true, Job: &core}, nil
	}
	// The job was read live just above, so it is the verdict gating the logs cache.
	res, err := cachedJobLogs(ctx, id, folder, LogFilter{IncludeRawFields: true}, func(context.Context) (*Job, error) { return &job, nil })
	if err != nil {
		return orchestrator.JobEntry{ID: id, Found: true, Job: &core, LogDigestError: err.Error()}, nil
	}
	if job.State != "Successful" {
		core.Fault = uipathFault(job, jobLogs(res.Logs))
	}
	return orchestrator.JobEntry{ID: id, Found: true, Job: &core, LogDigest: toLogDigest(res.Logs)}, nil
}

type jobEntryResult struct {
	entry orchestrator.JobEntry
	err   error
}

func (backend) GetJobs(ctx context.Context, args orchestrator.GetJobsArgs, onProgress shared.StepProgress) (orchestrator.GetJobsResult, error) {
	folder, err := ResolveFolder(args.Env, args.Folder)
	if err != nil {
		return orchestrator.GetJobsResult{}, err
	}
	lookups := fetchJobsForKeys(ctx, args.JobIDs, folder)
	// With IncludeLogDigest a 25-id batch is 50 Orchestrator calls — count them off as each id
	// finishes rather than going quiet for the whole batch.
	var mu sync.Mutex
	done := 0
	idx := make([]int, len(args.JobIDs))
	for i := range idx {
		idx[i] = i
	}
	results := shared.MapIsolated(ctx, idx, maxConcurrency, func(ctx context.Context, i int) jobEntryResult {
		entry, err := jobEntry(ctx, args.JobIDs[i], lookups[i], args, folder)
		mu.Lock()
		done++
		onProgress.Step(done, len(args.JobIDs), fmt.Sprintf("diagnosed %d/%d job(s)", done, len(args.JobIDs)))
		mu.Unlock()
		return jobEntryResult{entry, err}
	})
	entries := make([]orchestrator.JobEntry, len(results))
	for i, r := range results {
		if r.err != nil {
			return orchestrator.GetJobsResult{}, r.err
		}
		entries[i] = r.entry
	}
	return orchestrator.GetJobsResult{Folder: folder, Entries: entries}, nil
}

type logsEntryResult struct {
	entry orchestrator.JobLogsEntry
	err   error
}

func (backend) GetJobLogs(ctx context.Context, args orchestrator.GetJobLogsArgs, onProgress shared.StepProgress) (orchestrator.GetJobLogsResult, error) {
	folder, err := ResolveFolder(args.Env, args.Folder)
	if err != nil {
		return orchestrator.GetJobLogsResult{}, err
	}
	filter := LogFilter{
		MinLevel:         args.Filter.MinLevel,
		Contains:         args.Filter.Contains,
		OnlyFailures:     args.Filter.OnlyFailures,
		Tail:             args.Filter.Tail,
		IncludeRawFields: args.IncludeRawFields,
	}
	byID := fetchJobLogsForKeys(ctx, args.JobIDs, folder, filter, onProgress)
	idx := make([]int, len(args.JobIDs))
	for i := range idx {
		idx[i] = i
	}
	results := shared.MapIsolated(ctx, idx, maxConcurrency, func(ctx context.Context, i int) logsEntryResult {
		entry, err := logsEntry(ctx, args.JobIDs[i], byID[i], args, folder)
		return logsEntryResult{entry, err}
	})
	entries := make([]orchestrator.JobLogsEntry, len(results))
	for i, r := range results {
		if r.err != nil {
			return orchestrator.GetJobLogsResult{}, r.err
		}
		entries[i] = r.entry
	}
	return orchestrator.GetJobLogsResult{Folder: folder, Entries: entries}, nil
}

func logsEntry(ctx context.Context, id string, result LogResult, args orchestrator.GetJobLogsArgs, folder string) (orchestrator.JobLogsEntry, error) {
	if result.Err != nil && !args.Batch {
		return orchestrator.JobLogsEntry{}, result.Err
	}
	// RawMessage (the raw JSON text) never leaks into the output — only the parsed `raw` object
	// does, and only when includeRawFields was requested.
	rows := make([]orchestrator.LogRow, len(result.Logs))
	for i, l := range result.Logs {
		row := orchestrator.LogRow{Level: l.Level, Message: l.Message, TimeStamp: l.TimeStamp}
		if !args.FullMessages {
			row.Message = orchestrator.Truncate(l.Message)
		}
		if args.IncludeRawFields {
			raw, err := shared.MarshalJSON(parseRawMessage(l))
			if err != nil {
				return orchestrator.JobLogsEntry{}, err
			}
			row.Raw = json.RawMessage(raw)
		}
		rows[i] = row
	}
	entry := orchestrator.JobLogsEntry{
		JobID:         id,
		Logs:          rows,
		Returned:      len(rows),
		TotalMatching: result.TotalMatching,
		// truncated = the 500-row fetch window didn't cover every matching row (a requested
		// `tail` shorter than the total is not truncation).
		Truncated: result.TotalMatching != nil && *result.TotalMatching > logWindow,
	}
	if result.Err != nil {
		entry.Error = result.Err.Error()
	}
	// Video is a secondary attachment, always best-effort — never fails the primary logs result,
	// single id or batch alike. No recording at all means no `video`.
	if args.IncludeVideo {
		url, err := fetchJobVideoURL(ctx, id, folder)
		switch {
		case err != nil:
			entry.VideoError = err.Error()
		case url != "":
			entry.Video = orchestrator.URLVideo(url)
		}
	}
	return entry, nil
}

// ---- faulted-job issue -----------------------------------------------------

func (backend) BuildFaultedJobIssue(ctx context.Context, args orchestrator.FaultIssueArgs) (orchestrator.FaultedJobIssueResult, error) {
	return BuildFaultedJobIssue(ctx, args.Env, args.JobID, BuildOptions{Folder: args.Folder, Repo: args.Repo, Labels: args.Labels})
}

func (backend) BuildQueueItemPayload(ctx context.Context, args orchestrator.QueueItemPayloadArgs) (orchestrator.QueueItemPayload, error) {
	return buildQueueItemPayload(ctx, args)
}
