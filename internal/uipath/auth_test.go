package uipath

import (
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

// tokenHandler serves /token with the given tokens in order (the last repeats) and an empty
// Jobs page everywhere else.
func tokenHandler(tokens ...string) http.HandlerFunc {
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			i := min(int(n.Add(1))-1, len(tokens)-1)
			respond(w, http.StatusOK, map[string]any{"access_token": tokens[i], "expires_in": 3600})
			return
		}
		okEmpty(w, r)
	}
}

func oauthCfg(f *fake, extra map[string]any) map[string]any {
	o := map[string]any{"clientId": "id", "clientSecret": "secret"}
	for k, v := range extra {
		o[k] = v
	}
	return map[string]any{"bearer": nil, "oauth": o}
}

func listOnce(t *testing.T) error {
	t.Helper()
	_, err := ListRecentJobs(t.Context(), "", 1, "Authorization", "")
	return err
}

func TestOAuthTokenIsFetchedAndCarriedOnTheOrchestratorCall(t *testing.T) {
	f := newFake(t, tokenHandler("tok-1"))
	f.useConfig(t, oauthCfg(f, map[string]any{"scope": "OR.Jobs OR.Queues"}))
	if err := listOnce(t); err != nil {
		t.Fatal(err)
	}
	tokenCalls, jobCalls := f.callsTo("/token"), f.callsTo("/odata/Jobs")
	if len(tokenCalls) != 1 || len(jobCalls) != 1 {
		t.Fatalf("calls = %d token, %d jobs", len(tokenCalls), len(jobCalls))
	}
	if got := tokenCalls[0].Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Errorf("token content-type = %q", got)
	}
	if want := "grant_type=client_credentials&client_id=id&client_secret=secret&scope=OR.Jobs+OR.Queues"; tokenCalls[0].Body != want {
		t.Errorf("token body = %q", tokenCalls[0].Body)
	}
	if got := jobCalls[0].Header.Get("Authorization"); got != "Bearer tok-1" {
		t.Errorf("authorization = %q", got)
	}
}

func TestOAuthTokenIsCachedAcrossCalls(t *testing.T) {
	f := newFake(t, tokenHandler("tok-1"))
	f.useConfig(t, oauthCfg(f, nil))
	for range 2 {
		if err := listOnce(t); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.callsTo("/token")); n != 1 {
		t.Fatalf("token calls = %d, want 1", n)
	}
}

func TestConcurrentColdCallsShareOneTokenFetch(t *testing.T) {
	f := newFake(t, tokenHandler("tok-1"))
	f.useConfig(t, oauthCfg(f, nil))
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() { _ = listOnce(t) })
	}
	wg.Wait()
	if n := len(f.callsTo("/token")); n != 1 {
		t.Fatalf("token calls = %d, want 1 (single-flight)", n)
	}
}

func TestFailedOAuthFallsBackToTheBearer(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		okEmpty(w, r)
	})
	cfg := oauthCfg(f, nil)
	cfg["bearer"] = "fallback-bearer"
	f.useConfig(t, cfg)
	if err := listOnce(t); err != nil {
		t.Fatal(err)
	}
	if got := f.callsTo("/odata/Jobs")[0].Header.Get("Authorization"); got != "Bearer fallback-bearer" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestFailedOAuthWithoutBearerPropagatesTheTokenError(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	})
	f.useConfig(t, oauthCfg(f, nil))
	err := listOnce(t)
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 500 || apiErr.Method != "POST" || apiErr.Body != "boom" {
		t.Fatalf("err = %#v", err)
	}
	if n := len(f.calls()); n != 1 {
		t.Fatalf("calls = %d, want only the token request", n)
	}
}

func TestA401RefreshesTheTokenAndRetriesOnce(t *testing.T) {
	var jobs atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			tok := "tok-1"
			if len(r.Header) > 0 && jobs.Load() > 0 {
				tok = "tok-2"
			}
			respond(w, http.StatusOK, map[string]any{"access_token": tok, "expires_in": 3600})
		case jobs.Add(1) == 1:
			http.Error(w, "expired", http.StatusUnauthorized)
		default:
			respond(w, http.StatusOK, page(map[string]any{"Id": 1}))
		}
	})
	f.useConfig(t, oauthCfg(f, nil))
	jobsOut, err := ListRecentJobs(t.Context(), "", 1, "Authorization", "")
	if err != nil || len(jobsOut) != 1 {
		t.Fatalf("jobs = %v, err = %v", jobsOut, err)
	}
	calls := f.callsTo("/odata/Jobs")
	if len(f.callsTo("/token")) != 2 || len(calls) != 2 {
		t.Fatalf("token calls = %d, job calls = %d", len(f.callsTo("/token")), len(calls))
	}
	if calls[0].Header.Get("Authorization") != "Bearer tok-1" || calls[1].Header.Get("Authorization") != "Bearer tok-2" {
		t.Fatalf("authorizations = %q, %q", calls[0].Header.Get("Authorization"), calls[1].Header.Get("Authorization"))
	}
}

func TestA401OnTheRetryIsNotRetriedAgain(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			respond(w, http.StatusOK, map[string]any{"access_token": "tok", "expires_in": 3600})
			return
		}
		http.Error(w, "still expired", http.StatusUnauthorized)
	})
	f.useConfig(t, oauthCfg(f, nil))
	err := listOnce(t)
	if apiErr, ok := err.(*APIError); !ok || apiErr.Status != 401 {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.callsTo("/odata/Jobs")); n != 2 {
		t.Fatalf("job calls = %d, want exactly one retry", n)
	}
}

func TestStale401OnlyInvalidatesTheTokenThatWasRejected(t *testing.T) {
	tokens.mu.Lock()
	tokens.tok, tokens.expires = "fresh", time.Now().Add(time.Hour)
	tokens.mu.Unlock()
	invalidateBearerToken("old") // a late 401 for a token that was already replaced
	tokens.mu.Lock()
	got := tokens.tok
	tokens.mu.Unlock()
	if got != "fresh" {
		t.Fatalf("cached token = %q, want the fresh one kept", got)
	}
	invalidateBearerToken("fresh")
	tokens.mu.Lock()
	got = tokens.tok
	tokens.mu.Unlock()
	if got != "" {
		t.Fatalf("cached token = %q, want cleared", got)
	}
}

func TestTokenURLIsDerivedFromTheOrchestratorURL(t *testing.T) {
	got, err := defaultTokenURL("https://cloud.uipath.com/myorg/mytenant/orchestrator_")
	if err != nil || got != "https://cloud.uipath.com/myorg/identity_/connect/token" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := defaultTokenURL("https://uipath.internal/x/y/orchestrator_"); err == nil {
		t.Fatal("want an error for a non-cloud URL")
	}
}

func TestBearerOnlyConfigNeverCallsTheTokenEndpoint(t *testing.T) {
	f := newFake(t, okEmpty)
	f.useConfig(t, map[string]any{"bearer": "plain-bearer"})
	if err := listOnce(t); err != nil {
		t.Fatal(err)
	}
	calls := f.calls()
	if len(calls) != 1 || calls[0].Header.Get("Authorization") != "Bearer plain-bearer" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestConfigReloadClearsTheCachedToken(t *testing.T) {
	f := newFake(t, tokenHandler("tok-1", "tok-2"))
	path := f.useConfig(t, oauthCfg(f, map[string]any{"clientId": "id-1"}))
	if err := listOnce(t); err != nil {
		t.Fatal(err)
	}
	// Live-edit (no ResetForTest) so the reload listeners fire.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	next := *cfg.Uipath.OAuth
	next.ClientID = "id-2"
	writeConfigFile(t, path, map[string]any{
		"copilot": map[string]any{"sessionCache": false, "prod": map[string]any{"be": "https://be.p.example.com", "email": "p@example.com", "password": "pw"}},
		"uipath": map[string]any{
			"orchestratorUrl": f.orchestratorURL(),
			"oauth":           map[string]any{"clientId": next.ClientID, "clientSecret": next.ClientSecret},
		},
	})
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if err := listOnce(t); err != nil {
		t.Fatal(err)
	}
	if n := len(f.callsTo("/token")); n != 2 {
		t.Fatalf("token calls = %d, want a refetch after the reload", n)
	}
}

func TestA429IsRetriedAfterRetryAfterThenSucceeds(t *testing.T) {
	var n atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		respond(w, http.StatusOK, page(map[string]any{"Id": 1}))
	})
	f.useConfig(t, nil)
	jobs, err := ListRecentJobs(t.Context(), "", 1, "Authorization", "")
	if err != nil || len(jobs) != 1 || len(f.callsTo("/odata/Jobs")) != 2 {
		t.Fatalf("jobs = %v, err = %v, calls = %d", jobs, err, len(f.callsTo("/odata/Jobs")))
	}
}

func TestRateLimitGivesUpAfterMaxRetries(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	})
	f.useConfig(t, nil)
	err := listOnce(t)
	if apiErr, ok := err.(*APIError); !ok || apiErr.Status != 429 {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.calls()); n != 6 {
		t.Fatalf("calls = %d, want the attempt plus exactly 5 retries", n)
	}
}

func TestA429PausesEveryConcurrentRequestNotJustTheOneThatSawIt(t *testing.T) {
	f := newFake(t, okEmpty)
	f.useConfig(t, nil)
	const pause = 300 * time.Millisecond
	gate.pause(pause) // what the first request to see a 429 does
	start := time.Now()
	var wg sync.WaitGroup
	elapsed := make([]time.Duration, 4)
	for i := range elapsed {
		wg.Go(func() {
			_ = listOnce(t)
			elapsed[i] = time.Since(start)
		})
	}
	wg.Wait()
	for i, d := range elapsed {
		if d < pause-20*time.Millisecond {
			t.Errorf("request %d finished after %v, before the pause ended", i, d)
		}
	}
}

func TestAWriteIsRetriedAfterA429ThenSucceeds(t *testing.T) {
	var n atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		respond(w, http.StatusOK, map[string]any{"ok": true})
	})
	f.useConfig(t, nil)
	if _, err := post(t.Context(), "/odata/Queues/UiPathODataSvc.AddQueueItem", map[string]any{"a": 1}, FolderScope{OrgUnitID: "1", FolderPath: "F"}); err != nil {
		t.Fatal(err)
	}
	if got := len(f.calls()); got != 2 {
		t.Fatalf("calls = %d, want the 429 and one retry", got)
	}
}

func TestAWriteIsRetriedOnceWithAFreshTokenAfterA401(t *testing.T) {
	var jobs atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			tok := "tok-1"
			if jobs.Load() > 0 {
				tok = "tok-2"
			}
			respond(w, http.StatusOK, map[string]any{"access_token": tok, "expires_in": 3600})
			return
		}
		if jobs.Add(1) == 1 {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		respond(w, http.StatusOK, map[string]any{"ok": true})
	})
	f.useConfig(t, oauthCfg(f, nil))
	if _, err := post(t.Context(), "/odata/Queues/UiPathODataSvc.AddQueueItem", map[string]any{"a": 1}, FolderScope{OrgUnitID: "1", FolderPath: "F"}); err != nil {
		t.Fatal(err)
	}
	posts := f.callsTo("AddQueueItem")
	if len(posts) != 2 || posts[1].Header.Get("Authorization") != "Bearer tok-2" {
		t.Fatalf("posts = %d, want a retry carrying the refreshed token", len(posts))
	}
}

func TestInvalidatingTheLiveTokenSurvivesAConfigReloadItTriggers(t *testing.T) {
	f := newFake(t, tokenHandler("tok-1", "tok-2"))
	path := f.useConfig(t, oauthCfg(f, nil))
	if err := listOnce(t); err != nil {
		t.Fatal(err)
	}
	tokens.mu.Lock()
	live := tokens.tok
	tokens.mu.Unlock()
	// The config edit is picked up by the Load inside DropToken, firing the reload listener
	// (invalidateBearerToken("")) on the same goroutine.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		invalidateBearerToken(live)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("invalidateBearerToken deadlocked on the reload it triggered")
	}
}
