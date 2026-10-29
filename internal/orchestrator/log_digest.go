package orchestrator

import (
	"cmp"
	"slices"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Condensed, failure-focused view of a job's robot logs: consecutive-duplicate collapsing (retry
// loops) and stall detection (largest timestamp gaps). Pure and backend-neutral (reads only
// Level/Message/TimeStamp) — get_job attaches the digest instead of the raw (up to 500-line) log
// dump; the complete logs stay one get_job_logs call away. Backends embed JobLogDigest in their
// own struct to add fields (UiPath: the queue transaction's outcome; Apex: the run summary).

// CollapsedLog is one run of consecutive near-identical log lines.
type CollapsedLog struct {
	Level   string `json:"level"`
	Message string `json:"message"` // first raw occurrence, truncated
	Count   int    `json:"count"`
	FirstAt string `json:"firstAt"`
	LastAt  string `json:"lastAt"`
}

// LogStall is a gap between two consecutive log timestamps.
type LogStall struct {
	GapMs         int64  `json:"gapMs"`
	BeforeAt      string `json:"beforeAt"`
	AfterAt       string `json:"afterAt"`
	BeforeMessage string `json:"beforeMessage"` // what the robot logged last before going quiet
}

// LevelCounts counts logs per level and marshals in first-seen order, like the TS object it
// replaces (a Go map would sort the keys).
type LevelCounts struct {
	order  []string
	counts map[string]int
}

// Add counts one log at level.
func (c *LevelCounts) Add(level string) {
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	if _, seen := c.counts[level]; !seen {
		c.order = append(c.order, level)
	}
	c.counts[level]++
}

// Get is the count for level (0 when none).
func (c LevelCounts) Get(level string) int { return c.counts[level] }

// Levels lists the levels in first-seen order.
func (c LevelCounts) Levels() []string { return slices.Clone(c.order) }

// MarshalJSON emits an object whose keys follow first-seen order.
func (c LevelCounts) MarshalJSON() ([]byte, error) {
	fields := make(Fields, len(c.order))
	for i, level := range c.order {
		fields[i] = Field{level, c.counts[level]}
	}
	return fields.MarshalJSON()
}

// JobLogDigest is the failure-focused digest of a job's logs.
type JobLogDigest struct {
	Fetched         int            `json:"fetched"` // log rows fetched (capped at 500 upstream)
	ByLevel         LevelCounts    `json:"byLevel"`
	Failures        []CollapsedLog `json:"failures"`        // collapsed failure lines (error/fatal level or failure wording)
	DroppedFailures int            `json:"droppedFailures"` // collapsed failure entries cut by the cap
	Stalls          []LogStall     `json:"stalls"`
	Note            string         `json:"note"` // escalation pointer for the agent
}

// MessageCap bounds a collapsed log message.
const MessageCap = 400

const (
	failureCap = 25
	stallMinMs = 15_000
	stallTop   = 3
	// stallMessageCap bounds the "last thing logged before the stall" message.
	stallMessageCap = 160
)

func truncateTo(s string, limit int) string {
	if utf16Len(s) <= limit {
		return s
	}
	return shared.SliceUTF16(s, limit) + "…"
}

// Truncate caps s at MessageCap characters, appending an ellipsis when it cut.
func Truncate(s string) string { return truncateTo(s, MessageCap) }

// CollapseLogs merges consecutive logs whose level + normalized message match — retry loops
// repeat near-identical lines that differ only in ids/timestamps, so the collapse key reuses
// NormalizeError (the fault-signature normalization).
func CollapseLogs(logs []JobLog) []CollapsedLog {
	out := []CollapsedLog{}
	key := ""
	for _, l := range logs {
		k := l.Level + "|" + NormalizeError(l.Message)
		if n := len(out); n > 0 && k == key {
			out[n-1].Count++
			out[n-1].LastAt = l.TimeStamp
			continue
		}
		key = k
		out = append(out, CollapsedLog{
			Level:   l.Level,
			Message: Truncate(l.Message),
			Count:   1,
			FirstAt: l.TimeStamp,
			LastAt:  l.TimeStamp,
		})
	}
	return out
}

// FindLogStalls returns the largest gaps between consecutive log timestamps — where the run
// stalled. Logs must be oldest-first (the order every backend returns them). Never nil.
func FindLogStalls(logs []JobLog) []LogStall {
	stalls := []LogStall{}
	for i := 1; i < len(logs); i++ {
		prev, cur := logs[i-1], logs[i]
		gap, ok := shared.MsBetween(prev.TimeStamp, cur.TimeStamp)
		if !ok || gap < stallMinMs {
			continue
		}
		stalls = append(stalls, LogStall{
			GapMs:         gap,
			BeforeAt:      prev.TimeStamp,
			AfterAt:       cur.TimeStamp,
			BeforeMessage: truncateTo(prev.Message, stallMessageCap),
		})
	}
	slices.SortStableFunc(stalls, func(a, b LogStall) int { return cmp.Compare(b.GapMs, a.GapMs) })
	return stalls[:min(len(stalls), stallTop)]
}

// DigestLogs builds the failure-focused digest of logs (oldest first).
func DigestLogs(logs []JobLog) JobLogDigest {
	var byLevel LevelCounts
	var failureLogs []JobLog
	for _, l := range logs {
		level := l.Level
		if level == "" {
			level = "Unknown"
		}
		byLevel.Add(level)
		if IsFailureLog(l) {
			failureLogs = append(failureLogs, l)
		}
	}
	collapsed := CollapseLogs(failureLogs)
	// The cap keeps the first failure (often the root cause) and the tail (the terminal fault +
	// retry churn nearest the end).
	failures := collapsed
	if len(collapsed) > failureCap {
		failures = append([]CollapsedLog{collapsed[0]}, collapsed[len(collapsed)-(failureCap-1):]...)
	}
	return JobLogDigest{
		Fetched:         len(logs),
		ByLevel:         byLevel,
		Failures:        failures,
		DroppedFailures: len(collapsed) - len(failures),
		Stalls:          FindLogStalls(logs),
		Note: "Failure-focused digest (messages truncated, benign lines omitted). " +
			"If this is not enough, call get_job_logs with this job's jobId for the complete raw logs " +
			"(filters: minLevel, contains, onlyFailures, tail).",
	}
}
