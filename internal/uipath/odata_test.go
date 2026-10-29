package uipath

import (
	"slices"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

func logRow(level, msg string) Log {
	return Log{JobLog: orchestrator.JobLog{Level: level, Message: msg, TimeStamp: "2026-07-01T10:00:00Z"}}
}

func TestJobLogQueryParams(t *testing.T) {
	tail := func(n int) *int { return &n }
	cases := []struct {
		name   string
		filter LogFilter
		want   map[string]string
	}{
		{"no filter: oldest first, top 500", LogFilter{}, map[string]string{
			"$filter": "JobKey eq " + testKey, "$orderby": "TimeStamp asc", "$top": "500", "$select": "Level,Message,TimeStamp"}},
		{"includeRawFields adds RawMessage", LogFilter{IncludeRawFields: true}, map[string]string{
			"$select": "Level,Message,TimeStamp,RawMessage"}},
		{"minLevel warn is a Warn/Error/Fatal or-chain", LogFilter{MinLevel: orchestrator.LevelWarn}, map[string]string{
			"$filter": "JobKey eq " + testKey + " and (Level eq 'Warn' or Level eq 'Error' or Level eq 'Fatal')"}},
		{"minLevel error narrows to Error/Fatal", LogFilter{MinLevel: orchestrator.LevelError}, map[string]string{
			"$filter": "JobKey eq " + testKey + " and (Level eq 'Error' or Level eq 'Fatal')"}},
		{"contains escapes OData quotes", LogFilter{Contains: "can't login"}, map[string]string{
			"$filter": "JobKey eq " + testKey + " and contains(Message, 'can''t login')"}},
		{"tail flips newest-first and caps $top", LogFilter{Tail: tail(50)}, map[string]string{
			"$orderby": "TimeStamp desc", "$top": "50"}},
		{"tail with onlyFailures keeps $top at 500", LogFilter{Tail: tail(50), OnlyFailures: true}, map[string]string{
			"$orderby": "TimeStamp desc", "$top": "500"}},
	}
	for _, c := range cases {
		got := jobLogQueryParams(testKey, c.filter)
		for k, want := range c.want {
			if got[k] != want {
				t.Errorf("%s: %s = %q, want %q", c.name, k, got[k], want)
			}
		}
	}
}

func TestRefineJobLogs(t *testing.T) {
	info := logRow("Info", "Processing order 42")
	sneaky := logRow("Info", "Unable to find submit button, retrying") // failure worded at Info
	warn := logRow("Warn", "Slow response from portal")
	boom := logRow("Error", "System.Exception: boom")
	two := 2

	t.Run("no filter returns the logs untouched", func(t *testing.T) {
		logs := []Log{info, warn, boom}
		if got := refineJobLogs(logs, LogFilter{}); &got[0] != &logs[0] {
			t.Fatal("want the same slice back")
		}
	})
	t.Run("onlyFailures keeps error level and failure wording at benign levels", func(t *testing.T) {
		got := refineJobLogs([]Log{info, sneaky, warn, boom}, LogFilter{OnlyFailures: true})
		if !slices.Equal(got, []Log{sneaky, boom}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("tail restores oldest-first from a newest-first fetch", func(t *testing.T) {
		older, newer := logRow("Info", "first"), logRow("Info", "second")
		if got := refineJobLogs([]Log{newer, older}, LogFilter{Tail: &two}); !slices.Equal(got, []Log{older, newer}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("tail with onlyFailures keeps the last N failures", func(t *testing.T) {
		f1, f2, f3 := logRow("Error", "boom 1"), logRow("Error", "boom 2"), logRow("Error", "boom 3")
		got := refineJobLogs([]Log{f3, info, f2, f1}, LogFilter{Tail: &two, OnlyFailures: true})
		if !slices.Equal(got, []Log{f2, f3}) {
			t.Fatalf("got %v", got)
		}
	})
}

func TestToJobFlattensTheExpandedNavObjects(t *testing.T) {
	job := toJob(map[string]any{
		"Id": 1.0, "Key": "k1", "State": "Successful", "ReleaseName": "SPMG Sync",
		"Robot": map[string]any{"Name": "ec2-bot-1"}, "Release": map[string]any{"ProcessVersion": "1.0.28"},
	})
	if job.ID != "1" || job.RobotName != "ec2-bot-1" || job.ProcessVersion != "1.0.28" || job.ReleaseName != "SPMG Sync" {
		t.Fatalf("job = %+v", job)
	}
}

func TestToJobOmitsNavFieldsWhenTheyAreNull(t *testing.T) {
	job := toJob(map[string]any{"Id": "2", "Key": "k2", "State": "Faulted", "Robot": nil, "Release": nil})
	if job.RobotName != "" || job.ProcessVersion != "" || job.ID != "2" {
		t.Fatalf("job = %+v", job)
	}
}

func TestToQueueItemName(t *testing.T) {
	base := func(over map[string]any) map[string]any {
		m := map[string]any{"Id": 5.0, "Status": "Successful", "Reference": "ref-1", "QueueDefinitionId": 79926.0, "SpecificContent": map[string]any{"MemberID": "m1"}}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	if got := toQueueItem(base(map[string]any{"Name": "stale", "QueueDefinition": map[string]any{"Name": "Live"}})).Name; got != "Live" {
		t.Errorf("live queue-definition name not preferred: %q", got)
	}
	if got := toQueueItem(base(map[string]any{"Name": "own", "QueueDefinition": nil})).Name; got != "own" {
		t.Errorf("fallback to the item's own name failed: %q", got)
	}
	if got := toQueueItem(base(map[string]any{"Robot": map[string]any{"Name": "ec2-bot-1"}})).RobotName; got != "ec2-bot-1" {
		t.Errorf("robot name = %q", got)
	}
	if got := toQueueItem(base(map[string]any{"Robot": nil})).RobotName; got != "" {
		t.Errorf("unassigned robot name = %q", got)
	}
}

func TestConfirmQueueItemMatch(t *testing.T) {
	raw := func(over map[string]any) map[string]any {
		m := map[string]any{
			"Id": 42.0, "Reference": "ref-1", "Status": "Failed", "ExecutorJobKey": "e6f6d5e4-da99-4bf5-bef9-10b529ab430e",
			"ProcessingExceptionType": "BusinessException", "RetryNumber": 1.0, "CreationTime": "2026-07-01T00:00:00Z",
			"SpecificContent": map[string]any{"orderUid": "ae03574b-1234", "MemberID": "m1", "token": "jwt.secret"},
		}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	m, ok := confirmQueueItemMatch(raw(nil), "ae03574b-1234")
	if !ok || m.ID != 42 || m.Status != "Failed" || m.ExecutorJobKey == "" || m.ProcessingExceptionType != "BusinessException" || m.RetryNumber != 1 {
		t.Fatalf("match = %+v ok=%v", m, ok)
	}
	if m.SpecificContent["token"] != "[redacted]" || m.SpecificContent["MemberID"] != "m1" {
		t.Fatalf("specificContent = %v", m.SpecificContent)
	}
	if _, ok := confirmQueueItemMatch(raw(nil), "different-uid"); ok {
		t.Error("an incidental substring hit must be rejected")
	}
	if _, ok := confirmQueueItemMatch(raw(map[string]any{"SpecificContent": map[string]any{"MemberID": "m1"}}), "ae03574b-1234"); ok {
		t.Error("an item without orderUid must be rejected")
	}
	n, ok := confirmQueueItemMatch(raw(map[string]any{"Status": "New", "ExecutorJobKey": nil}), "ae03574b-1234")
	if !ok || n.Status != "New" || n.ExecutorJobKey != "" {
		t.Errorf("a New item confirms with an empty executorJobKey: %+v", n)
	}
	// the original content is not mutated by the redaction
	src := raw(nil)
	confirmQueueItemMatch(src, "ae03574b-1234")
	if src["SpecificContent"].(map[string]any)["token"] != "jwt.secret" {
		t.Error("redaction mutated its input")
	}
}

func TestJobDeepLink(t *testing.T) {
	f := newFake(t, okEmpty)
	f.useConfig(t, nil)
	want := "https://cloud.uipath.com/myorg/mytenant/orchestrator_/jobs(sidepanel:sidepanel/jobs/job-key-1/details)"
	if got := jobDeepLink("job-key-1"); got != want {
		t.Fatalf("link = %q", got)
	}
	if jobDeepLink("") != "" {
		t.Fatal("an empty key has no link")
	}
}

func TestResolveFolderPrecedence(t *testing.T) {
	f := newFake(t, okEmpty)
	f.useConfig(t, map[string]any{"folderPath": "Legacy", "folderPathByEnv": map[string]any{"prod": "Configured"}})
	cases := []struct{ env, folder, want string }{
		{"prod", "Explicit", "Explicit"},
		{"prod", "", "Configured"},
		{"pre_prod", "", "Authorization Dev Clone"},
		{"", "", "Legacy"},
	}
	for _, c := range cases {
		got, err := ResolveFolder(configEnv(c.env), c.folder)
		if err != nil || got != c.want {
			t.Errorf("ResolveFolder(%q,%q) = %q, %v; want %q", c.env, c.folder, got, err, c.want)
		}
	}
}
