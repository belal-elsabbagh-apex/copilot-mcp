package apex

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

func logRow(level, msg string) LogLine {
	return LogLine{JobLog: orchestrator.JobLog{Level: level, Message: msg, TimeStamp: "t"}, Kind: KindNDJSON}
}

func queueNamed(id, name string, active bool) Queue {
	q := queueFixture()
	q.ID, q.Name, q.IsActive = id, name, active
	return q
}

func pickedID(queues []Queue, input string, kind orchestrator.QueueKind) string {
	if q := pickQueue(queues, input, kind); q != nil {
		return q.ID
	}
	return ""
}

func TestPickQueue(t *testing.T) {
	queues := []Queue{
		queueNamed("q-pmg", "PMG auth submit queue", true),
		queueNamed("q-pmg-sync", "PMG auth sync queue", true),
		queueNamed("q-spmg", "Spmg auth submit queue", true),
		queueNamed("q-test", "CenCal shared-adapter test queue", false),
		queueNamed("q-unrouted", "Unrouted", true),
	}
	for _, tc := range []struct {
		name, input string
		kind        orchestrator.QueueKind
		want        string
	}{
		{"portal key is case-insensitive and picks submit by kind", "pmg", orchestrator.QueueSubmit, "q-pmg"},
		{"portal key picks sync by kind", "PMG", orchestrator.QueueSync, "q-pmg-sync"},
		{"a portal alias resolves to the key's queue", "sdpmg", orchestrator.QueueSubmit, "q-spmg"},
		{"a queue id matches directly", "q-unrouted", orchestrator.QueueSubmit, "q-unrouted"},
		{"a full queue name matches directly", "cencal shared-adapter test queue", orchestrator.QueueSubmit, "q-test"},
		{"an unknown portal is nil", "nope", orchestrator.QueueSubmit, ""},
		{"a kind with no queue is nil", "spmg", orchestrator.QueueSync, ""},
	} {
		if got := pickedID(queues, tc.input, tc.kind); got != tc.want {
			t.Errorf("%s: pickQueue(%q, %s) = %q, want %q", tc.name, tc.input, tc.kind, got, tc.want)
		}
	}
	dup := []Queue{queueNamed("old", "PMG auth submit queue", false), queueNamed("new", "Pmg auth submit queue", true)}
	if got := pickedID(dup, "PMG", orchestrator.QueueSubmit); got != "new" {
		t.Errorf("pickQueue over a repeated name = %q, want the active queue", got)
	}
}

func messages(rows []LogLine) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Message
	}
	return out
}

func levelsOf(rows []LogLine) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Level
	}
	return out
}

var filterRows = []LogLine{
	logRow("Info", "login ok"), logRow("Warn", "slow page"), logRow("Error", "row not found"),
	logRow("Debug", "dom dump"), logRow("Info", "request failed with 500"), logRow("Fatal", "kaput"),
}

func TestFilterLogRowsMinLevel(t *testing.T) {
	warn, _ := filterLogRows(filterRows, orchestrator.LogFilter{MinLevel: orchestrator.LevelWarn})
	if got := levelsOf(warn); !slices.Equal(got, []string{"Warn", "Error", "Fatal"}) {
		t.Errorf("warn levels = %v", got)
	}
	errs, _ := filterLogRows(filterRows, orchestrator.LogFilter{MinLevel: orchestrator.LevelError})
	if got := levelsOf(errs); !slices.Equal(got, []string{"Error", "Fatal"}) {
		t.Errorf("error levels = %v", got)
	}
}

func TestFilterLogRowsContainsIsCaseSensitive(t *testing.T) {
	if kept, _ := filterLogRows(filterRows, orchestrator.LogFilter{Contains: "page"}); len(kept) != 1 {
		t.Errorf("contains page kept %d, want 1", len(kept))
	}
	if kept, _ := filterLogRows(filterRows, orchestrator.LogFilter{Contains: "PAGE"}); len(kept) != 0 {
		t.Errorf("contains PAGE kept %d, want 0", len(kept))
	}
}

func TestFilterLogRowsOnlyFailuresCatchesFailureWordingAtInfoLevel(t *testing.T) {
	kept, _ := filterLogRows(filterRows, orchestrator.LogFilter{OnlyFailures: true})
	if got := messages(kept); !slices.Equal(got, []string{"row not found", "request failed with 500", "kaput"}) {
		t.Errorf("failures = %v", got)
	}
}

func TestFilterLogRowsTailKeepsTheLastNButTotalMatchingCountsAll(t *testing.T) {
	kept, total := filterLogRows(filterRows, orchestrator.LogFilter{MinLevel: orchestrator.LevelWarn, Tail: new(2)})
	if got := messages(kept); !slices.Equal(got, []string{"row not found", "kaput"}) || total != 3 {
		t.Errorf("tail = %v total = %d, want the last two of 3", got, total)
	}
}

func TestFilterLogRowsWindowIsTheFirst500MatchesAndTailIsCapped(t *testing.T) {
	var many []LogLine
	for range 600 {
		many = append(many, logRow("Info", "m"))
	}
	kept, total := filterLogRows(many, orchestrator.LogFilter{})
	if len(kept) != maxLogRows || total != 600 {
		t.Errorf("window kept %d of %d, want %d of 600", len(kept), total, maxLogRows)
	}
	kept, _ = filterLogRows(many, orchestrator.LogFilter{Tail: new(900)})
	if len(kept) != maxLogRows {
		t.Errorf("tail=900 kept %d, want the %d cap", len(kept), maxLogRows)
	}
}

func TestToLogRowTruncatesUnlessFullMessagesAndRawOnlyWhenRequested(t *testing.T) {
	r := logRow("Info", strings.Repeat("x", 500))
	r.Fields = map[string]any{"ms": 1}
	if got := utf8.RuneCountInString(toLogRow(r, false, false).Message); got != 401 {
		t.Errorf("truncated message = %d chars, want 401", got)
	}
	if got := len(toLogRow(r, true, false).Message); got != 500 {
		t.Errorf("full message = %d chars, want 500", got)
	}
	if raw := toLogRow(r, false, false).Raw; raw != nil {
		t.Errorf("raw = %s without includeRaw, want omitted", raw)
	}
	if got := string(toLogRow(r, false, true).Raw); got != `{"ms":1}` {
		t.Errorf("raw = %s, want the NDJSON fields", got)
	}
	if got := string(toLogRow(logRow("Info", "m"), false, true).Raw); got != "null" {
		t.Errorf("raw for a row without fields = %s, want null", got)
	}
}

func TestLogsUnavailableBeforeFetch(t *testing.T) {
	for _, status := range []string{"queued", "claimed", "running", "cancelling", "suspended", "resumed"} {
		j := jobRowFixture()
		j.Status, j.StartedAt = status, nil
		if u := logsUnavailableBeforeFetch(j); u == nil || u.Reason != orchestrator.LogsRunning {
			t.Errorf("%s: unavailable = %+v, want running", status, u)
		}
	}
	reaped := jobRowFixture()
	reaped.Status, reaped.StartedAt, reaped.ErrorClass = "failed", nil, sp("missing_credential")
	if u := logsUnavailableBeforeFetch(reaped); u == nil || u.Reason != orchestrator.LogsNeverStarted || !strings.Contains(u.Detail, "missing_credential") {
		t.Errorf("reaped job: unavailable = %+v, want never-started naming the error class", u)
	}
	for _, status := range []string{"failed", "timed_out"} {
		j := jobRowFixture()
		j.Status = status
		if u := logsUnavailableBeforeFetch(j); u != nil {
			t.Errorf("%s started job: unavailable = %+v, want nothing known up front", status, u)
		}
	}
}

func TestVideoPartPointsAtTheVideosArtifactByFilenameOrExplainsItsAbsence(t *testing.T) {
	v, errText := videoPart([]Artifact{artifactFixture("job.log", "logs", 1), artifactFixture("recording.mp4", "videos", 9)})
	if errText != "" || v.Kind != "artifact" || v.Artifact != "recording.mp4" || *v.SizeBytes != 9 || v.FetchWith != "get_job_artifacts" {
		t.Errorf("video = %+v, %q", v, errText)
	}
	if v, errText := videoPart([]Artifact{artifactFixture("job.log", "logs", 1)}); v != nil || !strings.Contains(errText, "no video") {
		t.Errorf("without a video: %v, %q", v, errText)
	}
}

func TestMergeNewestFirstCutsToTop(t *testing.T) {
	mk := func(id, at string) JobRow {
		r := jobRowFixture()
		r.ID, r.CreatedAt = id, at
		return r
	}
	a, b, c := mk("a", "2026-10-08T00:00:03Z"), mk("b", "2026-10-08T00:00:05Z"), mk("c", "2026-10-08T00:00:04Z")
	var ids []string
	for _, r := range mergeNewestFirst([][]JobRow{{a}, {b, c}}, 2) {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []string{"b", "c"}) {
		t.Errorf("merged = %v, want [b c]", ids)
	}
}

type stamped struct{ createdAt string }

func at(s stamped) string { return s.createdAt }

func TestWithinSince(t *testing.T) {
	rows := []stamped{{"2026-10-08T00:00:00Z"}, {"2026-10-01T00:00:00Z"}}
	kept, note, err := withinSince(rows, at, "2026-10-05T00:00:00Z")
	if err != nil || len(kept) != 1 || kept[0] != rows[0] || note != "" {
		t.Errorf("window = %v %q %v, want the newer row only", kept, note, err)
	}
	kept, note, err = withinSince(rows, at, "2026-11-01T00:00:00Z")
	if err != nil || len(kept) != 2 || !strings.Contains(note, "without the time bound") {
		t.Errorf("empty window = %v %q %v, want the unbounded rows with a note", kept, note, err)
	}
	if kept, note, err = withinSince(nil, at, "2026-11-01T00:00:00Z"); err != nil || len(kept) != 0 || note != "" {
		t.Errorf("no rows = %v %q %v", kept, note, err)
	}
	if kept, _, _ = withinSince(rows, at, ""); len(kept) != 2 {
		t.Errorf("no since kept %d, want all", len(kept))
	}
	if _, _, err = withinSince(rows, at, "yesterday"); err == nil || !strings.Contains(err.Error(), "not a valid ISO date") {
		t.Errorf("bad date error = %v", err)
	}
}
