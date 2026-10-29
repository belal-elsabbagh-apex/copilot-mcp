package apex

// Pure helpers for the Apex read functions in backend*.go: queue resolution, log row
// filtering/shaping, "why are there no logs" classification and window selection.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/reference"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const (
	jobLogName = "job.log"
	maxLogRows = 500 // UiPath's robot-log fetch window
)

// pickQueue matches case-insensitively over the queue name: `<KEY> auth <kind> queue` for the
// portal key and each of its aliases, or the input as a whole name. A queue id wins outright.
func pickQueue(queues []Queue, input string, kind orchestrator.QueueKind) *Queue {
	if i := slices.IndexFunc(queues, func(q Queue) bool { return q.ID == input }); i >= 0 {
		return &queues[i]
	}
	keys := []string{reference.NormalizeQueueName(input)}
	if p := reference.ResolvePortal(input); p.Matched {
		keys = append([]string{p.Key}, p.Aliases...)
	}
	wanted := map[string]bool{strings.ToLower(input): true}
	for _, k := range keys {
		wanted[strings.ToLower(fmt.Sprintf("%s auth %s queue", k, kind))] = true
	}
	var first *Queue
	for i, q := range queues {
		if !wanted[strings.ToLower(q.Name)] {
			continue
		}
		if q.IsActive {
			return &queues[i]
		}
		if first == nil {
			first = &queues[i]
		}
	}
	return first
}

var minLevels = map[orchestrator.LogLevel]map[string]bool{
	orchestrator.LevelWarn:  {"Warn": true, "Error": true, "Fatal": true, "Critical": true},
	orchestrator.LevelError: {"Error": true, "Fatal": true, "Critical": true},
}

// filterLogRows narrows by minLevel, contains (case-sensitive) and onlyFailures; the window then
// mirrors UiPath's 500-row fetch: the first 500 matches, or with `tail` the last min(tail, 500).
// totalMatching is the match count before the window cut.
func filterLogRows(rows []LogLine, f orchestrator.LogFilter) (kept []LogLine, totalMatching int) {
	levels := minLevels[f.MinLevel]
	matched := make([]LogLine, 0, len(rows))
	for _, r := range rows {
		if (levels == nil || levels[r.Level]) &&
			(f.Contains == "" || strings.Contains(r.Message, f.Contains)) &&
			(!f.OnlyFailures || orchestrator.IsFailureLog(r.JobLog)) {
			matched = append(matched, r)
		}
	}
	if f.Tail != nil && *f.Tail > 0 {
		return matched[max(0, len(matched)-min(*f.Tail, maxLogRows)):], len(matched)
	}
	return matched[:min(len(matched), maxLogRows)], len(matched)
}

// toLogRow: Raw carries the NDJSON `fields` object (null for rows that had none).
func toLogRow(r LogLine, fullMessages, includeRaw bool) orchestrator.LogRow {
	row := orchestrator.LogRow{Level: r.Level, Message: r.Message, TimeStamp: r.TimeStamp}
	if !fullMessages {
		row.Message = orchestrator.Truncate(r.Message)
	}
	if includeRaw {
		row.Raw = json.RawMessage("null")
		if r.Fields != nil {
			if b, err := shared.MarshalJSON(r.Fields); err == nil {
				row.Raw = b
			}
		}
	}
	return row
}

var artifactPolicyUnavailable = orchestrator.LogsUnavailable{
	Reason: orchestrator.LogsArtifactPolicy,
	Detail: "the job ended but no job.log artifact was kept (the Process's artifact_policy is on_failure or never)",
}

// logsUnavailableBeforeFetch is known without any artifact call: a job still in flight has no log
// yet (the live stream is dashboard-only SSE), and a job that never started has no artifacts at all.
func logsUnavailableBeforeFetch(job JobRow) *orchestrator.LogsUnavailable {
	if !IsTerminal(job.Status) {
		return &orchestrator.LogsUnavailable{
			Reason: orchestrator.LogsRunning,
			Detail: fmt.Sprintf("job is %s; Apex writes job.log only once the run ends", job.Status),
		}
	}
	if job.StartedAt == nil {
		class := "none"
		if job.ErrorClass != nil {
			class = *job.ErrorClass
		}
		return &orchestrator.LogsUnavailable{
			Reason: orchestrator.LogsNeverStarted,
			Detail: fmt.Sprintf("job ended without ever starting (error_class=%s); it has no artifacts", class),
		}
	}
	return nil
}

// videoPart reports the job's recording as a video ref, or the reason there is none.
func videoPart(artifacts []Artifact) (video *orchestrator.VideoRef, videoError string) {
	i := slices.IndexFunc(artifacts, func(a Artifact) bool { return a.ArtifactType == "videos" })
	if i < 0 {
		return nil, "no video recording artifact on this job"
	}
	return orchestrator.ArtifactVideo(artifacts[i].Filename, artifacts[i].SizeBytes), ""
}

// mergeNewestFirst merges per-workflow job lists into one newest-first list cut to top.
func mergeNewestFirst(lists [][]JobRow, top int) []JobRow {
	merged := slices.Concat(lists...)
	slices.SortStableFunc(merged, func(a, b JobRow) int { return strings.Compare(b.CreatedAt, a.CreatedAt) })
	return merged[:min(len(merged), top)]
}

func parseDate(s string) (time.Time, bool) {
	if t, ok := shared.ParseTime(s); ok {
		return t, true
	}
	t, err := time.Parse("2006-01-02", s)
	return t, err == nil
}

// withinSince bounds created_at client-side (the items route has no verified date filter). When
// nothing falls inside the window the unbounded rows are returned with a note, as UiPath does.
func withinSince[T any](rows []T, createdAt func(T) string, since string) (kept []T, note string, err error) {
	if since == "" {
		return rows, "", nil
	}
	bound, ok := parseDate(since)
	if !ok {
		return nil, "", shared.Expectedf("since '%s' is not a valid ISO date", since)
	}
	for _, r := range rows {
		if t, ok := parseDate(createdAt(r)); ok && !t.Before(bound) {
			kept = append(kept, r)
		}
	}
	if len(kept) > 0 || len(rows) == 0 {
		return kept, "", nil
	}
	return rows, fmt.Sprintf("no queue item matched within since=%s; returned matches without the time bound.", since), nil
}
