package apex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
)

// diskCacheOn flips the (already loaded) test config to sessionCache:true and points the cache
// dir at a temp dir, returning that dir.
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
	ResetClient()
	return dir
}

func logins(f *fakeApex) int { return len(f.callsTo("POST", "/auth/login")) }

func TestASecondProcessReadsASettledJobFromDiskAndReusesTheToken(t *testing.T) {
	f := newFakeApex(t, nil)
	diskCacheOn(t)
	routeJob(f, failedDetailFixture(endedAgo(time.Hour)), []Artifact{logArtifact()}, backendLog)
	first := readLogs(t)
	ResetClient() // what a new server process starts from: no memory, same disk
	second := readLogs(t)
	if logDownloads(f) != 1 || listings(f) != 1 {
		t.Errorf("log downloads = %d, listings = %d, want 1 each across both processes", logDownloads(f), listings(f))
	}
	if logins(f) != 1 {
		t.Errorf("logins = %d, want 1 (the second process reuses the persisted JWT)", logins(f))
	}
	if second.Returned != first.Returned || second.Returned != 7 {
		t.Errorf("disk-served read returned %d rows, first read %d", second.Returned, first.Returned)
	}
}

func TestNothingIsPersistedWhileTheJobIsWithinTheSettleWindow(t *testing.T) {
	f := newFakeApex(t, nil)
	dir := diskCacheOn(t)
	routeJob(f, failedDetailFixture(endedAgo(time.Minute)), []Artifact{logArtifact()}, backendLog)
	readLogs(t)
	if files, _ := filepath.Glob(filepath.Join(dir, "cache", "apex", "*")); len(files) != 0 {
		t.Errorf("unsettled job persisted: %v", files)
	}
}

func TestRefreshRefetchesOnceAndReplacesTheCachedEntry(t *testing.T) {
	f := newFakeApex(t, nil)
	diskCacheOn(t)
	routeJob(f, failedDetailFixture(endedAgo(time.Hour)), []Artifact{logArtifact()}, backendLog)
	readLogs(t)
	if _, err := Backend.GetJobLogs(memo.WithRefresh(t.Context()), logsArgs(), nil); err != nil {
		t.Fatal(err)
	}
	if logDownloads(f) != 2 || listings(f) != 2 {
		t.Errorf("after refresh: downloads = %d, listings = %d, want 2 each", logDownloads(f), listings(f))
	}
	readLogs(t) // the refreshed entry is a normal hit again
	if logDownloads(f) != 2 {
		t.Errorf("downloads = %d after a post-refresh read, want still 2", logDownloads(f))
	}
}

func TestAPersistedTokenIsDroppedFromDiskWhenItIsRejected(t *testing.T) {
	f := newFakeApex(t, nil)
	diskCacheOn(t)
	cfg, err := config.ResolveApex()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := bearerToken(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := memo.LoadToken(tokenKey(cfg)); !ok {
		t.Fatal("login did not persist the token")
	}
	dropToken(cfg, tok)
	if _, _, ok := memo.LoadToken(tokenKey(cfg)); ok {
		t.Error("a rejected token is still on disk")
	}
	if logins(f) != 1 {
		t.Errorf("logins = %d", logins(f))
	}
}

func TestSessionCacheOffPersistsNoTokenAndNoJobLog(t *testing.T) {
	f := newFakeApex(t, nil) // sessionCache:false
	dir := filepath.Join(t.TempDir(), "cm")
	t.Setenv("COPILOT_MCP_CACHE", filepath.Join(dir, "sessions.json"))
	routeJob(f, failedDetailFixture(endedAgo(time.Hour)), []Artifact{logArtifact()}, backendLog)
	readLogs(t)
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("cache dir %s exists with sessionCache:false", dir)
	}
}

func TestDroppingARejectedTokenSurvivesAConfigReloadItTriggers(t *testing.T) {
	newFakeApex(t, nil)
	diskCacheOn(t)
	path := os.Getenv("COPILOT_MCP_CONFIG")
	cfg, err := config.ResolveApex()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := bearerToken(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The edit is picked up by the Load inside DropToken, firing ResetClient on the same goroutine.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		dropToken(cfg, tok)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dropToken deadlocked on the reload it triggered")
	}
}
