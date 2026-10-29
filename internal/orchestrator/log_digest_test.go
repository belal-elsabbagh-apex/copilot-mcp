package orchestrator

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func logRow(level, msg, ts string) JobLog { return JobLog{Level: level, Message: msg, TimeStamp: ts} }

func TestTruncateLeavesShortStringsAndCutsLongOnesWithEllipsis(t *testing.T) {
	if got := Truncate("short message"); got != "short message" {
		t.Errorf("got %q", got)
	}
	got := Truncate(strings.Repeat("x", MessageCap+50))
	if want := strings.Repeat("x", MessageCap) + "…"; got != want {
		t.Errorf("got length %d, want %d", len(got), len(want))
	}
}

func TestCollapseLogsMergesRetriesDifferingOnlyInIDsAndTimestamps(t *testing.T) {
	logs := []JobLog{
		logRow("Error", "Timeout reaching order 7bfea00c-1111-2222-3333-444444444444 at 2026-07-01T10:00:01Z", "2026-07-01T10:00:01Z"),
		logRow("Error", "Timeout reaching order 23ba359f-5555-6666-7777-888888888888 at 2026-07-01T10:00:31Z", "2026-07-01T10:00:31Z"),
		logRow("Error", "Timeout reaching order 508debf6-9999-aaaa-bbbb-cccccccccccc at 2026-07-01T10:01:01Z", "2026-07-01T10:01:01Z"),
	}
	got := CollapseLogs(logs)
	if len(got) != 1 {
		t.Fatalf("collapsed into %d entries, want 1", len(got))
	}
	if got[0].Count != 3 || got[0].FirstAt != "2026-07-01T10:00:01Z" || got[0].LastAt != "2026-07-01T10:01:01Z" {
		t.Errorf("unexpected entry %+v", got[0])
	}
	// the first raw occurrence is kept, not the normalized form
	if !strings.Contains(got[0].Message, "7bfea00c") {
		t.Errorf("message %q lost the first raw occurrence", got[0].Message)
	}
}

func TestCollapseLogsKeepsDistinctMessagesLevelChangesAndNonConsecutiveRepeatsApart(t *testing.T) {
	tests := map[string][]JobLog{
		"distinct messages and level changes": {
			logRow("Info", "Opening portal", "2026-07-01T10:00:00Z"),
			logRow("Error", "Opening portal", "2026-07-01T10:00:01Z"),
			logRow("Error", "Submit failed", "2026-07-01T10:00:02Z"),
		},
		"non-consecutive repeats": {
			logRow("Error", "Submit failed", "2026-07-01T10:00:00Z"),
			logRow("Info", "Retrying", "2026-07-01T10:00:01Z"),
			logRow("Error", "Submit failed", "2026-07-01T10:00:02Z"),
		},
	}
	for name, logs := range tests {
		t.Run(name, func(t *testing.T) {
			if got := CollapseLogs(logs); len(got) != 3 {
				t.Errorf("collapsed into %d entries, want 3", len(got))
			}
		})
	}
}

func TestFindLogStallsReportsLargestGapsAboveThresholdBiggestFirst(t *testing.T) {
	logs := []JobLog{
		logRow("Info", "step 1", "2026-07-01T10:00:00Z"),
		logRow("Info", "step 2", "2026-07-01T10:00:05Z"), // 5s — under threshold
		logRow("Info", "step 3", "2026-07-01T10:01:05Z"), // 60s stall after "step 2"
		logRow("Info", "step 4", "2026-07-01T10:01:25Z"), // 20s stall after "step 3"
	}
	got := FindLogStalls(logs)
	if len(got) != 2 {
		t.Fatalf("got %d stalls, want 2", len(got))
	}
	if got[0].GapMs != 60_000 || got[0].BeforeMessage != "step 2" || got[1].GapMs != 20_000 {
		t.Errorf("unexpected stalls %+v", got)
	}
}

func TestFindLogStallsIsEmptyNotNilForATightStream(t *testing.T) {
	got := FindLogStalls([]JobLog{
		logRow("Info", "a", "2026-07-01T10:00:00Z"),
		logRow("Info", "b", "2026-07-01T10:00:01Z"),
	})
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want empty non-nil (marshals as [])", got)
	}
}

func TestDigestLogsCountsByLevelKeepsOnlyFailuresAndPointsAtGetJobLogs(t *testing.T) {
	logs := []JobLog{
		logRow("Info", "Opening portal", "2026-07-01T10:00:00Z"),
		logRow("Info", "Unable to find submit button", "2026-07-01T10:00:01Z"), // failure worded at Info
		logRow("Warn", "Slow response", "2026-07-01T10:00:02Z"),
		logRow("Error", "Submit failed", "2026-07-01T10:00:03Z"),
	}
	d := DigestLogs(logs)
	if d.Fetched != 4 || d.DroppedFailures != 0 {
		t.Errorf("fetched=%d dropped=%d", d.Fetched, d.DroppedFailures)
	}
	if !slices.Equal(d.ByLevel.Levels(), []string{"Info", "Warn", "Error"}) || d.ByLevel.Get("Info") != 2 {
		t.Errorf("byLevel = %+v", d.ByLevel)
	}
	var msgs []string
	for _, f := range d.Failures {
		msgs = append(msgs, f.Message)
	}
	if !slices.Equal(msgs, []string{"Unable to find submit button", "Submit failed"}) {
		t.Errorf("failures = %v", msgs)
	}
	if !strings.Contains(d.Note, "get_job_logs") {
		t.Errorf("note %q does not point at get_job_logs", d.Note)
	}
}

func TestDigestLogsFailureCapKeepsFirstAndTailAndCountsDropped(t *testing.T) {
	logs := make([]JobLog, 30)
	for i := range logs {
		logs[i] = logRow("Error", fmt.Sprintf("failure %d", i), "2026-07-01T10:00:00Z")
	}
	d := DigestLogs(logs)
	if len(d.Failures) != 25 || d.Failures[0].Message != "failure 0" || d.Failures[24].Message != "failure 29" || d.DroppedFailures != 5 {
		t.Errorf("failures=%d first=%q last=%q dropped=%d", len(d.Failures), d.Failures[0].Message, d.Failures[24].Message, d.DroppedFailures)
	}
}

// The digest is a tool result: byLevel keeps first-seen key order and empty lists are [] not null.
func TestDigestLogsMarshalsLikeTheTSDigest(t *testing.T) {
	d := DigestLogs([]JobLog{logRow("Warn", "x", "2026-07-01T10:00:00Z"), logRow("Info", "y", "2026-07-01T10:00:01Z")})
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"byLevel":{"Warn":1,"Info":1},"failures":[],"droppedFailures":0,"stalls":[]`) {
		t.Errorf("unexpected JSON: %s", b)
	}
}
