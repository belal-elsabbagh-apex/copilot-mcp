package uipath

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Log is one RobotLogs row. RawMessage is only populated when LogFilter.IncludeRawFields is set —
// see rawmessage.go for what it contains and why it's opt-in.
type Log struct {
	orchestrator.JobLog
	RawMessage string
}

// LogFilter narrows a job-log fetch. Every field defaults off — the zero filter returns the full
// (capped 500) log set, oldest first.
type LogFilter struct {
	MinLevel     orchestrator.LogLevel // at-or-above; pushed into the OData $filter
	Contains     string                // server-side substring match on Message (case-sensitive)
	OnlyFailures bool                  // semantic post-filter — see orchestrator.IsFailureLog
	Tail         *int                  // keep only the last N logs after all other filters
	// IncludeRawFields also selects RawMessage — the structured transaction/exception/custom-field
	// data hiding behind the free-text Message. Off by default: benchmarked live at ~4-6x the byte
	// size of every row fetched (RobotLogs response, real jobs, 2026-09-07) — a bigger cost than a
	// mere doubling.
	IncludeRawFields bool
}

var levelsAtLeast = map[orchestrator.LogLevel][]string{
	orchestrator.LevelWarn:  {"Warn", "Error", "Fatal"},
	orchestrator.LevelError: {"Error", "Fatal"},
}

// jobLogQueryParams is the OData query for a job's RobotLogs under f. Level/contains narrow
// server-side so the 500-row cap spends its budget on matching rows. Tail flips the fetch to
// newest-first so the window covers the END of a >500-row job; refineJobLogs restores
// oldest-first. With OnlyFailures the tail cut happens client-side (after the semantic filter),
// so $top stays at 500.
func jobLogQueryParams(jobKey string, f LogFilter) map[string]string {
	clauses := []string{"JobKey eq " + jobKey}
	if f.MinLevel != "" {
		ors := make([]string, 0, 3)
		for _, l := range levelsAtLeast[f.MinLevel] {
			ors = append(ors, fmt.Sprintf("Level eq '%s'", l))
		}
		clauses = append(clauses, "("+strings.Join(ors, " or ")+")")
	}
	if f.Contains != "" {
		clauses = append(clauses, fmt.Sprintf("contains(Message, '%s')", odataQuote(f.Contains)))
	}
	order, top := "asc", logWindow
	if f.Tail != nil {
		order = "desc"
		if !f.OnlyFailures {
			top = min(*f.Tail, logWindow)
		}
	}
	sel := "Level,Message,TimeStamp"
	if f.IncludeRawFields {
		sel += ",RawMessage"
	}
	return map[string]string{
		"$filter":  strings.Join(clauses, " and "),
		"$orderby": "TimeStamp " + order,
		"$top":     fmt.Sprint(top),
		"$select":  sel,
		"$count":   "true",
	}
}

// refineJobLogs is the client-side refinement matching jobLogQueryParams: restore oldest-first
// order (a tail fetch comes back newest-first), apply the semantic failure filter, then keep the
// last Tail rows. Without a filter it returns logs untouched.
func refineJobLogs(logs []Log, f LogFilter) []Log {
	out := logs
	if f.Tail != nil {
		out = slices.Clone(logs)
		slices.Reverse(out)
	}
	if f.OnlyFailures {
		out = slices.DeleteFunc(slices.Clone(out), func(l Log) bool { return !orchestrator.IsFailureLog(l.JobLog) })
	}
	// A tail of 0 keeps everything, as JS slice(-0) did.
	if f.Tail != nil && *f.Tail > 0 && len(out) > *f.Tail {
		out = out[len(out)-*f.Tail:]
	}
	return out
}

// LogResult is one job's logs.
type LogResult struct {
	Logs []Log
	// TotalMatching is @odata.count: rows matching the server-side filters, before the 500 cap and
	// before OnlyFailures/Tail. nil when Orchestrator omits the count.
	TotalMatching *int
	// Err is set by fetchJobLogsForKeys when the fetch itself failed; Logs stays empty.
	Err error
}

// FetchJobLogs is the raw single-job RobotLogs read: robot execution logs for one job, oldest
// first. RobotLogs is keyed by the job's GUID Key (not its numeric Id). A $filter query, like
// FetchJobByKey: no matching rows is a normal empty result, so any error here is a real failure
// and propagates.
func FetchJobLogs(ctx context.Context, jobKey, folder string, f LogFilter) (LogResult, error) {
	if jobKey == "" {
		return LogResult{Logs: []Log{}}, nil
	}
	data, err := get(ctx, "/odata/RobotLogs", jobLogQueryParams(jobKey, f), folder, "")
	if err != nil {
		return LogResult{}, err
	}
	rows := odataValues(data)
	logs := make([]Log, len(rows))
	for i, r := range rows {
		logs[i] = Log{
			JobLog:     orchestrator.JobLog{Level: shared.Str(r, "Level"), Message: shared.Str(r, "Message"), TimeStamp: shared.Str(r, "TimeStamp")},
			RawMessage: shared.Str(r, "RawMessage"),
		}
	}
	res := LogResult{Logs: refineJobLogs(logs, f)}
	if n, ok := shared.Prop(data, "@odata.count").(float64); ok {
		count := int(n)
		res.TotalMatching = &count
	}
	return res, nil
}

// fetchJobLogsForKeys fetches logs for several jobs, the same filter applied to each, one result
// per key in input order. RobotLogs has NO way to combine multiple JobKeys server-side —
// confirmed live that both `JobKey in (...)` and `JobKey eq X or JobKey eq Y` silently return
// wrong/empty results rather than erroring, so one HTTP call per key is a hard platform limit.
// What this collapses is MCP round-trips, not HTTP calls. One key's failure lands on its own Err
// instead of losing every other key's logs.
func fetchJobLogsForKeys(ctx context.Context, keys []string, folder string, f LogFilter, onProgress shared.StepProgress) []LogResult {
	var mu sync.Mutex
	fetched := 0
	return shared.MapIsolated(ctx, keys, maxConcurrency, func(ctx context.Context, key string) LogResult {
		res, err := cachedJobLogs(ctx, key, folder, f, func(ctx context.Context) (*Job, error) { return fetchJobState(ctx, key, folder) })
		if err != nil {
			res = LogResult{Logs: []Log{}, Err: err}
		}
		mu.Lock()
		fetched++
		onProgress.Step(fetched, len(keys), fmt.Sprintf("fetched logs for %d job(s)", fetched))
		mu.Unlock()
		return res
	})
}

// fetchJobVideoURL returns the recording uri for a job (the entry whose uri points at
// recording.webm), "" when there is none.
func fetchJobVideoURL(ctx context.Context, jobKey, folder string) (string, error) {
	if jobKey == "" {
		return "", nil
	}
	data, err := get(ctx, "/api/VideoRecording/jobs/"+jobKey+"/read", nil, folder, "")
	if err != nil {
		if IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	entries, _ := data.([]any)
	for _, e := range entries {
		if uri := shared.Str(e, "uri"); strings.Contains(uri, "recording.webm") {
			return uri, nil
		}
	}
	return "", nil
}
