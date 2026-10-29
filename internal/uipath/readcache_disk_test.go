package uipath

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

// diskCacheOn flips the loaded test config to sessionCache:true and points the cache dir at a
// temp dir, returning that dir.
func diskCacheOn(t *testing.T) string {
	t.Helper()
	path := os.Getenv("COPILOT_MCP_CONFIG")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["copilot"]["sessionCache"] = true
	if raw, err = json.Marshal(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "cm")
	t.Setenv("COPILOT_MCP_CACHE", filepath.Join(dir, "sessions.json"))
	config.ResetForTest()
	return dir
}

// newProcess drops everything a restart would: memory caches and the in-memory token.
func newProcess() {
	logCache.Flush()
	jobCache.Flush()
	invalidateBearerToken("")
}

func detail(t *testing.T, refresh bool) {
	t.Helper()
	ctx := t.Context()
	if refresh {
		ctx = memo.WithRefresh(ctx)
	}
	if _, err := Backend.GetJobs(ctx, orchestrator.GetJobsArgs{Env: config.Prod, JobIDs: []string{testKey}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestASecondProcessReadsSettledLogsAndJobDetailFromDisk(t *testing.T) {
	f := settledServe(t, nil)
	diskCacheOn(t)
	getLogs(t, orchestrator.LogFilter{})
	detail(t, false)
	newProcess()
	before := len(f.calls())
	second := getLogs(t, orchestrator.LogFilter{})
	detail(t, false)
	if n := len(f.calls()) - before; n != 0 {
		t.Errorf("second process made %d upstream calls, want 0", n)
	}
	if second.Returned != 2 {
		t.Errorf("disk-served logs returned %d rows, want 2", second.Returned)
	}
}

func TestJobDetailIsNotPersistedUntilSettled(t *testing.T) {
	f := settledServe(t, map[string]any{"EndTime": endedAgo(time.Minute)})
	dir := diskCacheOn(t)
	detail(t, false)
	detail(t, false)
	if n := fullJobReads(f); n != 2 {
		t.Errorf("job detail reads = %d, want 2 (an unsettled job is read live every time)", n)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "cache", "uipath-jobs", "*")); len(files) != 0 {
		t.Errorf("unsettled job persisted: %v", files)
	}
}

func TestRefreshRefetchesLogsAndDetailOnceAndReplacesTheEntries(t *testing.T) {
	f := settledServe(t, nil)
	diskCacheOn(t)
	getLogs(t, orchestrator.LogFilter{})
	detail(t, false)
	ctx := memo.WithRefresh(t.Context())
	a := logsBase
	a.JobIDs = []string{testKey}
	if _, err := Backend.GetJobLogs(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	detail(t, true)
	if logCalls(f) != 2 || fullJobReads(f) != 2 {
		t.Errorf("after refresh: log calls = %d, job reads = %d, want 2 each", logCalls(f), fullJobReads(f))
	}
	newProcess()
	getLogs(t, orchestrator.LogFilter{})
	detail(t, false)
	if logCalls(f) != 2 || fullJobReads(f) != 2 {
		t.Errorf("a post-refresh read from disk went upstream: log calls = %d, job reads = %d", logCalls(f), fullJobReads(f))
	}
}

func TestWritePathStillFetchesTheJobAfterTheDetailWasCached(t *testing.T) {
	f := settledServe(t, map[string]any{"Id": 5, "Key": "job-key-1"})
	diskCacheOn(t)
	detail(t, false)
	detail(t, false)
	before := fullJobReads(f)
	if _, err := stopJob(t.Context(), config.PreProd, testKey, orchestrator.StopSoft); err == nil {
		t.Fatal("want a terminal-state refusal")
	}
	if got := fullJobReads(f) - before; got != 1 {
		t.Errorf("stop_job fetched the job %d times, want 1 live read", got)
	}
}

func TestAPersistedOAuthTokenSurvivesARestartAndIsDroppedOnA401(t *testing.T) {
	f := newFake(t, tokenHandler("tok-1", "tok-2"))
	f.useConfig(t, oauthCfg(f, nil))
	dir := diskCacheOn(t)
	if err := listOnce(t); err != nil {
		t.Fatal(err)
	}
	newProcess()
	if err := listOnce(t); err != nil {
		t.Fatal(err)
	}
	if n := len(f.callsTo("/token")); n != 1 {
		t.Fatalf("token calls = %d, want 1 (the second process reuses the persisted token)", n)
	}
	cfg, err := config.Uipath()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := memo.LoadToken(oauthTokenKey(cfg)); !ok {
		t.Fatal("token not on disk")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "tokens", "*.json")); len(files) != 1 {
		t.Fatalf("token files = %v", files)
	}
	invalidateBearerToken("tok-1") // a 401 for the token in use
	if _, _, ok := memo.LoadToken(oauthTokenKey(cfg)); ok {
		t.Error("a rejected token is still on disk")
	}
}

func TestSessionCacheOffPersistsNoTokenNoLogsNoDetail(t *testing.T) {
	settledServe(t, nil) // sessionCache:false
	dir := filepath.Join(t.TempDir(), "cm")
	t.Setenv("COPILOT_MCP_CACHE", filepath.Join(dir, "sessions.json"))
	getLogs(t, orchestrator.LogFilter{})
	detail(t, false)
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("cache dir %s exists with sessionCache:false", dir)
	}
}
