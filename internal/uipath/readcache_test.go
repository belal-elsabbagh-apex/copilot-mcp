package uipath

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

func logCalls(f *fake) int { return len(f.callsTo("/odata/RobotLogs")) }

// fullJobReads are the detail reads (FetchJobByKey asks for InputArguments; the verdict read does not).
func fullJobReads(f *fake) int {
	n := 0
	for _, c := range f.callsTo("/odata/Jobs") {
		if strings.Contains(c.query("$select"), "InputArguments") {
			n++
		}
	}
	return n
}

func endedAgo(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }

func getLogs(t *testing.T, filter orchestrator.LogFilter) orchestrator.JobLogsEntry {
	t.Helper()
	a := logsBase
	a.JobIDs, a.Filter = []string{testKey}, filter
	res, err := Backend.GetJobLogs(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Entries[0]
}

func settledServe(t *testing.T, over map[string]any) *fake {
	t.Helper()
	return serve(t, map[string]any{testKey: jobRow(testKey, over)}, map[string]any{testKey: []any{logJSON("Info", "one", nil), logJSON("Error", "two", nil)}})
}

func TestSettledTerminalJobsLogsAreFetchedFromRobotLogsOnce(t *testing.T) {
	f := settledServe(t, nil)
	first := getLogs(t, orchestrator.LogFilter{})
	second := getLogs(t, orchestrator.LogFilter{})
	if n := logCalls(f); n != 1 {
		t.Errorf("RobotLogs calls = %d, want 1", n)
	}
	if first.Returned != 2 || second.Returned != 2 {
		t.Errorf("returned %d then %d rows, want 2 both times", first.Returned, second.Returned)
	}
}

func TestACacheHitMakesNoUpstreamCallAtAll(t *testing.T) {
	f := settledServe(t, nil)
	getLogs(t, orchestrator.LogFilter{})
	before := len(f.calls())
	getLogs(t, orchestrator.LogFilter{})
	if n := len(f.calls()) - before; n != 0 {
		t.Errorf("a cache hit made %d upstream calls, want 0", n)
	}
}

func TestCacheKeyIsTheExactQueryAndNeverRefilteredLocally(t *testing.T) {
	f := settledServe(t, nil)
	getLogs(t, orchestrator.LogFilter{})
	getLogs(t, orchestrator.LogFilter{Contains: "two"})
	getLogs(t, orchestrator.LogFilter{Contains: "two"})
	getLogs(t, orchestrator.LogFilter{OnlyFailures: true})
	tail := 1
	getLogs(t, orchestrator.LogFilter{Tail: &tail})
	getLogs(t, orchestrator.LogFilter{Tail: &tail})
	if n := logCalls(f); n != 4 {
		t.Errorf("RobotLogs calls = %d, want 4 (plain, contains, onlyFailures, tail; repeats are hits)", n)
	}
}

func TestDigestAndFaultIssueShareTheCachedRawFieldsFetch(t *testing.T) {
	f := settledServe(t, nil)
	digest := func() {
		if _, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.Prod, JobIDs: []string{testKey}, IncludeLogDigest: true}, nil); err != nil {
			t.Fatal(err)
		}
	}
	digest()
	digest()
	if _, err := BuildFaultedJobIssue(t.Context(), config.Prod, testKey, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := logCalls(f); n != 1 {
		t.Errorf("RobotLogs calls = %d, want 1 across two digests and a fault issue", n)
	}
	if n := fullJobReads(f); n != 1 {
		t.Errorf("job detail reads = %d, want 1 (a settled terminal job's detail is cached too)", n)
	}
}

func TestJobsNotYetSettledOrNotTerminalAreNeverCached(t *testing.T) {
	cases := map[string]map[string]any{
		"faulted 1 minute ago": {"EndTime": endedAgo(time.Minute)},
		"running":              {"State": "Running", "EndTime": nil},
		"pending":              {"State": "Pending", "EndTime": nil},
		"no EndTime":           {"EndTime": nil},
	}
	for name, over := range cases {
		t.Run(name, func(t *testing.T) {
			f := settledServe(t, over)
			getLogs(t, orchestrator.LogFilter{})
			getLogs(t, orchestrator.LogFilter{})
			if n := logCalls(f); n != 2 {
				t.Errorf("RobotLogs calls = %d, want 2 (nothing cached)", n)
			}
		})
	}
}

func TestFailedLogFetchesAreNeverCached(t *testing.T) {
	var mu sync.Mutex
	failing := true
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/odata/Jobs"):
			respond(w, http.StatusOK, page(jobRow(testKey, nil)))
		case strings.HasSuffix(r.URL.Path, "/odata/RobotLogs"):
			mu.Lock()
			defer mu.Unlock()
			if failing {
				respond(w, http.StatusInternalServerError, map[string]any{"message": "boom"})
				return
			}
			respond(w, http.StatusOK, page(logJSON("Info", "ok", nil)))
		}
	})
	f.useConfig(t, nil)
	a := logsBase
	a.JobIDs, a.Batch = []string{testKey}, true
	if res, _ := Backend.GetJobLogs(t.Context(), a, nil); res.Entries[0].Error == "" {
		t.Fatal("want the failure on the entry")
	}
	mu.Lock()
	failing = false
	mu.Unlock()
	getLogs(t, orchestrator.LogFilter{})
	getLogs(t, orchestrator.LogFilter{})
	if n := logCalls(f); n != 2 {
		t.Errorf("RobotLogs calls = %d, want 2 (error, success; then cached)", n)
	}
}

func TestEntriesAreScopedToTheFolder(t *testing.T) {
	f := settledServe(t, nil)
	getLogs(t, orchestrator.LogFilter{})
	a := logsBase
	a.JobIDs, a.Folder = []string{testKey}, "Some Other Folder"
	if _, err := Backend.GetJobLogs(t.Context(), a, nil); err != nil {
		t.Fatal(err)
	}
	if n := logCalls(f); n != 2 {
		t.Errorf("RobotLogs calls = %d, want 2: another folder must not hit the first folder's entry", n)
	}
}

func TestConfigReloadFlushesTheLogCache(t *testing.T) {
	f := settledServe(t, nil)
	path := os.Getenv("COPILOT_MCP_CONFIG")
	if _, err := config.Load(); err != nil { // the first load never notifies
		t.Fatal(err)
	}
	getLogs(t, orchestrator.LogFilter{})
	if logCache.Len() == 0 {
		t.Fatal("nothing was cached")
	}
	writeConfigFile(t, path, map[string]any{
		"copilot": map[string]any{"sessionCache": false,
			"prod":     map[string]any{"be": "https://be.prod.example.com", "email": "p@example.com", "password": "pw2"},
			"pre_prod": map[string]any{"be": "https://be.preprod.example.com", "email": "q@example.com", "password": "pw"}},
		"uipath": map[string]any{"orchestratorUrl": testOrchestratorURL, "bearer": "test-bearer"},
	})
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(); err != nil {
		t.Fatal(err)
	}
	if logCache.Len() != 0 {
		t.Fatal("a config reload left cached logs behind")
	}
	getLogs(t, orchestrator.LogFilter{})
	if n := logCalls(f); n != 2 {
		t.Errorf("RobotLogs calls = %d, want 2 after the flush", n)
	}
}

func TestWritePathStillFetchesTheJobAfterAReadCachedIt(t *testing.T) {
	f := settledServe(t, map[string]any{"Id": 5, "Key": "job-key-1"})
	// Cache the logs of testKey through the read side, repeatedly.
	getLogs(t, orchestrator.LogFilter{})
	getLogs(t, orchestrator.LogFilter{})
	before := fullJobReads(f)
	// stop_job's fetch-first guard reads the job raw; the job is terminal, so it refuses after the read.
	_, err := stopJob(t.Context(), config.PreProd, testKey, orchestrator.StopSoft)
	if err == nil || !strings.Contains(err.Error(), "state is already") {
		t.Fatalf("err = %v, want a terminal-state refusal", err)
	}
	if got := fullJobReads(f) - before; got != 1 {
		t.Errorf("stop_job fetched the job %d times, want 1 live fetch-first read", got)
	}
}

func TestFaultIssueFetchesTheJobAndItsLogsConcurrently(t *testing.T) {
	arrived := make(chan string, 2)
	release := make(chan struct{})
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/odata/Jobs"):
			arrived <- "job"
			<-release
			respond(w, http.StatusOK, page(jobRow(testKey, nil)))
		case strings.HasSuffix(r.URL.Path, "/odata/RobotLogs"):
			arrived <- "logs"
			<-release
			respond(w, http.StatusOK, page(logJSON("Error", "boom", nil)))
		}
	})
	f.useConfig(t, nil)
	done := make(chan error, 1)
	go func() {
		_, err := BuildFaultedJobIssue(t.Context(), config.Prod, testKey, BuildOptions{})
		done <- err
	}()
	got := map[string]bool{}
	for range 2 {
		select {
		case n := <-arrived:
			got[n] = true
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatalf("only %v arrived while the other was held: the reads are sequential", got)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFaultIssueStillFailsWhenTheJobReadFails(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/odata/Jobs") {
			respond(w, http.StatusInternalServerError, map[string]any{"message": "boom"})
			return
		}
		okEmpty(w, r)
	})
	f.useConfig(t, nil)
	if _, err := BuildFaultedJobIssue(t.Context(), config.Prod, testKey, BuildOptions{}); err == nil {
		t.Fatal("want the job read error")
	}
}
