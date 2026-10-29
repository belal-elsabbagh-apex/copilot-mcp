package apex

// Apex read functions: one per orchestrator.Backend read method. Fetching lives here; shaping is
// in map.go and the pure selection logic in readsupport.go. Every call is folder-scoped by the
// client (the `folder` arg is a name override, "" = the env's folder).

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const (
	findConcurrency    = 6
	searchPerQueueMax  = 200             // one page of fuzzy `search` hits per queue
	attemptsPerItemMax = 50              // max_attempts is 1–10
	maxLogBytes        = 5 * 1024 * 1024 // a larger job.log is read from its tail
	partialLogNote     = " Only the last 5 MiB of job.log were read: earlier lines (and failures) are not reflected."
)

// Backend is the Apex orchestrator backend.
var Backend orchestrator.Backend = backend{}

type backend struct{}

func (backend) Name() orchestrator.Orchestrator { return orchestrator.Apex }

func jobLink(id string) string { return DashboardLink("/jobs/" + id) }

// ---- jobs ---------------------------------------------------------------------

// workflowFilter: processName is matched as a substring over the folder's workflows, then each
// match is listed by workflow_id (a verified filter) — one request per matching workflow. "" ids
// mean unfiltered.
func workflowFilter(ctx context.Context, env config.Env, folder, processName string) ([]string, error) {
	if processName == "" {
		return []string{""}, nil
	}
	workflows, err := listWorkflows(ctx, env, folder)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, w := range workflows {
		if containsFold(w.Name, processName) {
			ids = append(ids, w.ID)
		}
	}
	return ids, nil
}

func (backend) ListJobs(ctx context.Context, args orchestrator.ListJobsArgs) (orchestrator.ListJobsResult, error) {
	var (
		resolved Folder
		rerr     error
		wg       sync.WaitGroup
	)
	wg.Go(func() { resolved, rerr = ResolveFolderID(ctx, args.Env, args.Folder) })
	workflowIDs, err := workflowFilter(ctx, args.Env, args.Folder, args.ProcessName)
	wg.Wait()
	if err := cmpErr(rerr, err); err != nil {
		return orchestrator.ListJobsResult{}, err
	}
	lists, err := shared.MapOrdered(ctx, workflowIDs, len(workflowIDs), func(ctx context.Context, _ int, workflowID string) ([]JobRow, error) {
		return listJobs(ctx, args.Env, args.Folder, JobFilters{CreatedAfter: args.Since, WorkflowID: workflowID}, args.Top)
	})
	if err != nil {
		return orchestrator.ListJobsResult{}, err
	}
	rows := mergeNewestFirst(lists, args.Top)
	jobs := make([]orchestrator.JobCore, len(rows))
	for i, row := range rows {
		jobs[i] = toJobCore(row, nil, jobLink(row.ID))
	}
	return orchestrator.ListJobsResult{Folder: resolved.Name, Jobs: jobs}, nil
}

// cmpErr is the first non-nil error.
func cmpErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

type readLog struct {
	ParsedJobLog
	Partial bool // the job.log exceeded maxLogBytes, so only its tail was read

	rawBytes int // downloaded size, the cache's cost proxy
}

// readLogArtifact reads and parses one job.log artifact (its tail when it is huge).
func readLogArtifact(ctx context.Context, artifact Artifact) (readLog, error) {
	partial := artifact.SizeBytes > maxLogBytes
	rangeHeader := ""
	if partial {
		rangeHeader = fmt.Sprintf("bytes=-%d", maxLogBytes)
	}
	var buf bytes.Buffer
	if _, err := apexDownload(ctx, artifact.DownloadURL, rangeHeader, &buf); err != nil {
		return readLog{}, err
	}
	text := strings.ToValidUTF8(buf.String(), "\uFFFD")
	return readLog{ParsedJobLog: parseJobLog(text), Partial: partial, rawBytes: buf.Len()}, nil
}

// readJobLog is the job's parsed job.log, or nil when no job.log artifact is listed. Errors on any
// HTTP failure. The ONE job.log read of the read side; row is the live job read that gates the
// terminal-job cache.
func readJobLog(ctx context.Context, env config.Env, folder string, row JobRow) (*readLog, error) {
	artifacts, err := cachedArtifacts(ctx, env, folder, row)
	if err != nil {
		return nil, err
	}
	for _, a := range artifacts {
		if a.Filename == jobLogName {
			l, err := cachedLog(ctx, env, folder, row, a)
			return &l, err
		}
	}
	return nil, nil //nolint:nilnil // nil log = no job.log artifact, not a failure
}

func plainLogs(rows []LogLine) []orchestrator.JobLog {
	out := make([]orchestrator.JobLog, len(rows))
	for i, r := range rows {
		out[i] = r.JobLog
	}
	return out
}

// digestPart is best-effort, as UiPath's digest: a failure surfaces as logDigestError and never
// hides the job.
func digestPart(ctx context.Context, env config.Env, folder string, detail JobDetail, entry *orchestrator.JobEntry) {
	if u := logsUnavailableBeforeFetch(detail.JobRow); u != nil {
		entry.LogsUnavailable = u
		return
	}
	parsed, err := readJobLog(ctx, env, folder, detail.JobRow)
	switch {
	case err != nil:
		entry.LogDigestError = err.Error()
	case parsed == nil:
		entry.LogsUnavailable = &artifactPolicyUnavailable
	default:
		digest := LogDigest{JobLogDigest: orchestrator.DigestLogs(plainLogs(parsed.Rows)), Run: toRunSummary(detail, parsed.ParsedJobLog)}
		if parsed.Partial {
			digest.Note += partialLogNote
		}
		entry.LogDigest = digest
	}
}

func jobEntry(ctx context.Context, args orchestrator.GetJobsArgs, id string) (orchestrator.JobEntry, error) {
	detail, err := getJob(ctx, args.Env, args.Folder, id)
	if err != nil {
		switch {
		case notFound(err):
			return orchestrator.JobEntry{ID: id}, nil
		case args.Batch:
			return orchestrator.JobEntry{ID: id, Error: err.Error()}, nil
		}
		return orchestrator.JobEntry{}, err
	}
	job := toJobCore(detail.JobRow, &detail, jobLink(id))
	entry := orchestrator.JobEntry{ID: id, Found: true, Job: &job}
	if args.IncludeLogDigest {
		digestPart(ctx, args.Env, args.Folder, detail, &entry)
	}
	return entry, nil
}

func (backend) GetJobs(ctx context.Context, args orchestrator.GetJobsArgs, onProgress shared.StepProgress) (orchestrator.GetJobsResult, error) {
	resolved, err := ResolveFolderID(ctx, args.Env, args.Folder)
	if err != nil {
		return orchestrator.GetJobsResult{}, err
	}
	var done atomic.Int64
	total := len(args.JobIDs)
	entries, err := shared.MapOrdered(ctx, args.JobIDs, total, func(ctx context.Context, _ int, id string) (orchestrator.JobEntry, error) {
		entry, err := jobEntry(ctx, args, id)
		n := int(done.Add(1))
		onProgress.Step(n, total, fmt.Sprintf("diagnosed %d/%d job(s)", n, total))
		return entry, err
	})
	if err != nil {
		return orchestrator.GetJobsResult{}, err
	}
	return orchestrator.GetJobsResult{Folder: resolved.Name, Entries: entries}, nil
}

func jobLogsEntry(ctx context.Context, args orchestrator.GetJobLogsArgs, jobID string) (orchestrator.JobLogsEntry, error) {
	detail, err := getJob(ctx, args.Env, args.Folder, jobID)
	if err != nil {
		return orchestrator.JobLogsEntry{}, err
	}
	entry := orchestrator.JobLogsEntry{JobID: jobID, Logs: []orchestrator.LogRow{}}
	if u := logsUnavailableBeforeFetch(detail.JobRow); u != nil {
		entry.LogsUnavailable = u
		if args.IncludeVideo {
			entry.VideoError = u.Detail
		}
		return entry, nil
	}
	artifacts, err := cachedArtifacts(ctx, args.Env, args.Folder, detail.JobRow)
	if err != nil {
		return orchestrator.JobLogsEntry{}, err
	}
	if args.IncludeVideo {
		entry.Video, entry.VideoError = videoPart(artifacts)
	}
	var logArtifact *Artifact
	for i := range artifacts {
		if artifacts[i].Filename == jobLogName {
			logArtifact = &artifacts[i]
			break
		}
	}
	if logArtifact == nil {
		entry.LogsUnavailable = &artifactPolicyUnavailable
		return entry, nil
	}
	log, err := cachedLog(ctx, args.Env, args.Folder, detail.JobRow, *logArtifact)
	if err != nil {
		return orchestrator.JobLogsEntry{}, err
	}
	kept, totalMatching := filterLogRows(log.Rows, args.Filter)
	entry.Logs = make([]orchestrator.LogRow, len(kept))
	for i, r := range kept {
		entry.Logs[i] = toLogRow(r, args.FullMessages, args.IncludeRawFields)
	}
	entry.Returned = len(kept)
	entry.TotalMatching = &totalMatching // of the rows read; truncated = the 500-row window or a tail-only read hid rows
	entry.Truncated = totalMatching > maxLogRows || log.Partial
	return entry, nil
}

func (backend) GetJobLogs(ctx context.Context, args orchestrator.GetJobLogsArgs, onProgress shared.StepProgress) (orchestrator.GetJobLogsResult, error) {
	resolved, err := ResolveFolderID(ctx, args.Env, args.Folder)
	if err != nil {
		return orchestrator.GetJobLogsResult{}, err
	}
	var done atomic.Int64
	total := len(args.JobIDs)
	entries, err := shared.MapOrdered(ctx, args.JobIDs, total, func(ctx context.Context, _ int, jobID string) (orchestrator.JobLogsEntry, error) {
		entry, err := jobLogsEntry(ctx, args, jobID)
		if err != nil && args.Batch {
			entry, err = orchestrator.JobLogsEntry{JobID: jobID, Logs: []orchestrator.LogRow{}, Error: err.Error()}, nil
		}
		n := int(done.Add(1))
		onProgress.Step(n, total, fmt.Sprintf("fetched logs for %d/%d job(s)", n, total))
		return entry, err
	})
	if err != nil {
		return orchestrator.GetJobLogsResult{}, err
	}
	return orchestrator.GetJobLogsResult{Folder: resolved.Name, Entries: entries}, nil
}
func containsFold(haystack, needle string) bool {
	return needle == "" || strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
