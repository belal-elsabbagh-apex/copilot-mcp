package apex

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func get(t *testing.T, env config.Env, path string, opts reqOpts) ([]byte, error) {
	t.Helper()
	return apexRequest(t.Context(), env, http.MethodGet, path, opts)
}

func apiErr(t *testing.T, err error) *APIError {
	t.Helper()
	e, ok := errors.AsType[*APIError](err)
	if !ok {
		t.Fatalf("error = %v, want *APIError", err)
	}
	return e
}

func bearer(c apexCall) string { return c.Header.Get("Authorization") }

func TestLoginOnceWithConfiguredAccountAndCookieJWTAsBearer(t *testing.T) {
	f := newFakeApex(t, nil)
	jwt := longLived()
	f.route("POST", "/auth/login", loginReturning(jwt))
	f.on("GET", "/jobs/", jsonReply(map[string]any{"id": "j1"}))
	for _, p := range []string{"/jobs/j1", "/jobs/j2"} {
		if _, err := get(t, config.Prod, p, reqOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	logins := f.callsTo("POST", "/auth/login")
	if len(logins) != 1 {
		t.Fatalf("logins = %d, want 1", len(logins))
	}
	if b, _ := logins[0].Body.(map[string]any); b["username"] != "tester" || b["password"] != "pw" {
		t.Errorf("login body = %v, want the configured account", logins[0].Body)
	}
	for _, c := range f.callsTo("GET", "/jobs/") {
		if bearer(c) != "Bearer "+jwt {
			t.Errorf("Authorization = %q, want Bearer <cookie jwt>", bearer(c))
		}
	}
}

func TestLoginWithoutAccessTokenCookieIsAnAPIErrorNotAnUnauthenticatedCall(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("POST", "/auth/login", jsonReply(map[string]any{"user": map[string]any{}}).with("Set-Cookie", "other=1"))
	_, err := get(t, config.Prod, "/jobs/j1", reqOpts{})
	e := apiErr(t, err)
	if e.Path != "/auth/login" || !strings.Contains(e.Error(), "no access_token cookie") {
		t.Errorf("error = %v, want the missing-cookie login error", e)
	}
	if n := len(f.callsTo("GET", "/jobs/")); n != 0 {
		t.Errorf("%d data calls made, want 0", n)
	}
}

func TestRejectedLoginSurfacesTheServersDetail(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("POST", "/auth/login", jsonReply(map[string]any{"detail": "Invalid credentials"}, 401))
	_, err := get(t, config.Prod, "/jobs/j1", reqOpts{})
	if e := apiErr(t, err); e.Status != 401 || !strings.Contains(e.Error(), "Invalid credentials") {
		t.Errorf("error = %v, want 401 Invalid credentials", e)
	}
	if n := len(f.callsTo("POST", "/auth/login")); n != 1 {
		t.Errorf("logins = %d, want 1", n)
	}
}

func TestConcurrentFirstCallsShareOneLoginAndOneFolderLookup(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/", jsonReply(map[string]any{}))
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() { _, _ = get(t, config.Prod, "/jobs/j", reqOpts{}) })
	}
	wg.Wait()
	if n := len(f.callsTo("POST", "/auth/login")); n != 1 {
		t.Errorf("logins = %d, want 1", n)
	}
	if n := len(f.callsTo("GET", "/folders")); n != 1 {
		t.Errorf("folder lookups = %d, want 1", n)
	}
}

func TestJWTExpiringInsideTheSkewIsNotReusedButALongLivedOneIs(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/", jsonReply(map[string]any{}))
	f.route("POST", "/auth/login", loginReturning(fakeJWT(time.Now().Add(30*time.Second))))
	for _, p := range []string{"/jobs/a", "/jobs/b"} {
		if _, err := get(t, config.Prod, p, reqOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	// never reused: every authenticated request (folders lookup + 2 reads) logged in afresh
	if n := len(f.callsTo("POST", "/auth/login")); n != 3 {
		t.Fatalf("logins = %d, want 3 (token inside the 60s skew is never reused)", n)
	}

	ResetClient()
	before := len(f.callsTo("POST", "/auth/login"))
	f.route("POST", "/auth/login", loginReturning(fakeJWT(time.Now().Add(time.Hour))))
	for _, p := range []string{"/jobs/a", "/jobs/b"} {
		if _, err := get(t, config.Prod, p, reqOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.callsTo("POST", "/auth/login")) - before; n != 1 {
		t.Errorf("logins for a long-lived JWT = %d, want 1", n)
	}
}

func TestA401ReLogsInExactlyOnceAndRetriesWithTheFreshToken(t *testing.T) {
	f := newFakeApex(t, nil)
	old, fresh := longLived(), fakeJWT(time.Now().Add(25*time.Hour))
	f.route("POST", "/auth/login", loginReturning(old, fresh))
	f.route("GET", "/jobs/", func(c apexCall) reply {
		if bearer(c) == "Bearer "+fresh {
			return jsonReply(map[string]any{"id": "j1"})
		}
		return jsonReply(map[string]any{"detail": "expired"}, 401)
	})
	if _, err := get(t, config.Prod, "/jobs/j1", reqOpts{}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.callsTo("POST", "/auth/login")); n != 2 {
		t.Errorf("logins = %d, want 2", n)
	}
	if n := len(f.callsTo("GET", "/jobs/")); n != 2 {
		t.Errorf("reads = %d, want 2", n)
	}
}

func TestASecondConsecutive401IsReportedNotRetriedForever(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/", jsonReply(map[string]any{"detail": "nope"}, 401))
	_, err := get(t, config.Prod, "/jobs/j1", reqOpts{})
	if e := apiErr(t, err); e.Status != 401 {
		t.Errorf("status = %d, want 401", e.Status)
	}
	if n := len(f.callsTo("POST", "/auth/login")); n != 2 {
		t.Errorf("logins = %d, want 2", n)
	}
	if n := len(f.callsTo("GET", "/jobs/")); n != 2 {
		t.Errorf("reads = %d, want 2", n)
	}
}

func TestEveryRequestCarriesTheEnvsFolderID(t *testing.T) {
	f := newFakeApex(t, nil)
	ok := jsonReply(map[string]any{})
	f.on("GET", "/jobs/", ok)
	f.on("POST", "/queues/", ok)
	ctx := t.Context()
	_, _ = apexRequest(ctx, config.Prod, http.MethodGet, "/jobs/a", reqOpts{})
	_, _ = apexRequest(ctx, config.PreProd, http.MethodGet, "/jobs/a", reqOpts{})
	_, _ = apexRequest(ctx, config.PreProd, http.MethodPost, "/queues/q/items", reqOpts{Body: map[string]any{"reference": "r"}})
	var got []string
	for _, c := range append(f.callsTo("GET", "/jobs/"), f.callsTo("POST", "/queues/")...) {
		got = append(got, c.Header.Get("X-Folder-Id"))
	}
	want := []string{folderIDs["Authorization"], folderIDs["Authorization Test"], folderIDs["Authorization Test"]}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("X-Folder-Id = %v, want %v", got, want)
	}
}

func TestReadSideFolderOverrideWinsOverTheEnvDefault(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/", jsonReply(map[string]any{}))
	if _, err := get(t, config.Prod, "/jobs/a", reqOpts{Folder: "Eligibility Check"}); err != nil {
		t.Fatal(err)
	}
	if got := f.callsTo("GET", "/jobs/")[0].Header.Get("X-Folder-Id"); got != folderIDs["Eligibility Check"] {
		t.Errorf("X-Folder-Id = %q, want the Eligibility Check folder", got)
	}
}

func TestUnknownFolderListsTheAvailableOnesAndSendsNothing(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/", jsonReply(map[string]any{}))
	_, err := get(t, config.Prod, "/jobs/a", reqOpts{Folder: "Nope"})
	if !shared.IsExpected(err) || !strings.Contains(err.Error(), "'Nope' not found") ||
		!strings.Contains(err.Error(), "Authorization, Authorization Test, Eligibility Check") {
		t.Errorf("error = %v, want an expected 'not found' listing the folders", err)
	}
	if n := len(f.callsTo("GET", "/jobs/")); n != 0 {
		t.Errorf("%d data calls made, want 0", n)
	}
}

func TestFolderListingIsFetchedOnceAndAFailedFetchIsNotCached(t *testing.T) {
	f := newFakeApex(t, nil)
	var mu sync.Mutex
	fail := true
	f.route("GET", "/folders", func(apexCall) reply {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return jsonReply(map[string]any{"detail": "boom"}, 500)
		}
		return jsonReply(map[string]any{"items": []any{map[string]any{"id": folderIDs["Authorization"], "name": "Authorization"}}})
	})
	if _, err := ResolveFolderID(t.Context(), config.Prod, ""); err == nil {
		t.Fatal("ResolveFolderID succeeded against a failing listing")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	for range 2 {
		got, err := ResolveFolderID(t.Context(), config.Prod, "")
		if err != nil || got != (Folder{ID: folderIDs["Authorization"], Name: "Authorization"}) {
			t.Fatalf("ResolveFolderID = %v, %v", got, err)
		}
	}
	if n := len(f.callsTo("GET", "/folders")); n != 2 {
		t.Errorf("folder listings = %d, want 2 (one failed, one cached)", n)
	}
}

func TestWritesOnProdAreRefusedBeforeAnyRequest(t *testing.T) {
	for _, m := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		t.Run(m, func(t *testing.T) {
			f := newFakeApex(t, nil)
			_, err := apexRequest(t.Context(), config.Prod, m, "/queues/q/items", reqOpts{Body: map[string]any{}})
			if !shared.IsExpected(err) || !strings.Contains(err.Error(), "pre_prod-only") {
				t.Errorf("error = %v, want an expected pre_prod-only refusal", err)
			}
			if n := f.count(); n != 0 {
				t.Errorf("%d requests made, want 0", n)
			}
		})
	}
}

func TestWriteWithAFolderOverrideIsRefusedBeforeAnyRequest(t *testing.T) {
	f := newFakeApex(t, nil)
	_, err := apexRequest(t.Context(), config.PreProd, http.MethodPost, "/jobs", reqOpts{Body: map[string]any{}, Folder: "Authorization"})
	if err == nil || !strings.Contains(err.Error(), "never take a folder override") {
		t.Errorf("error = %v, want the folder-override refusal", err)
	}
	if n := f.count(); n != 0 {
		t.Errorf("%d requests made, want 0", n)
	}
}

func TestPreProdWritePostsTheJSONBodyToTheTestFolder(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("POST", "/queues/", jsonReply(map[string]any{"id": "item-1"}, 201))
	raw, err := apexRequest(t.Context(), config.PreProd, http.MethodPost, "/queues/q/items", reqOpts{Body: map[string]any{"reference": "r"}})
	if err != nil || !strings.Contains(string(raw), "item-1") {
		t.Fatalf("apexRequest = %s, %v", raw, err)
	}
	post := f.callsTo("POST", "/queues/")[0]
	if b, _ := post.Body.(map[string]any); b["reference"] != "r" {
		t.Errorf("body = %v, want reference r", post.Body)
	}
	if post.Header.Get("Content-Type") != "application/json" || post.Header.Get("X-Folder-Id") != folderIDs["Authorization Test"] {
		t.Errorf("headers = %v, want JSON content type and the test folder", post.Header)
	}
}

func TestEmptyTwoXXBodyIsNil(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("POST", "/jobs/", reply{status: 204})
	raw, err := apexRequest(t.Context(), config.PreProd, http.MethodPost, "/jobs/j1/cancel", reqOpts{Body: map[string]any{}})
	if err != nil || len(raw) != 0 {
		t.Errorf("apexRequest = %q, %v, want an empty body", raw, err)
	}
}

func TestPreProdWriteIsRefusedWhenBothEnvsResolveToOneFolder(t *testing.T) {
	f := newFakeApex(t, func(s map[string]any) map[string]any {
		s["folderByEnv"] = map[string]any{"pre_prod": "Authorization"}
		return s
	})
	f.on("POST", "/queues/", jsonReply(map[string]any{"id": "item-1"}, 201))
	f.on("GET", "/jobs/", jsonReply(map[string]any{}))
	_, err := apexRequest(t.Context(), config.PreProd, http.MethodPost, "/queues/q/items", reqOpts{Body: map[string]any{}})
	if !shared.IsExpected(err) || !strings.Contains(err.Error(), "same folder") {
		t.Errorf("error = %v, want the same-folder refusal", err)
	}
	if n := len(f.callsTo("POST", "/queues/")); n != 0 {
		t.Errorf("%d POSTs sent, want 0", n)
	}
	if _, err := get(t, config.PreProd, "/jobs/a", reqOpts{}); err != nil {
		t.Errorf("read on the shared folder failed: %v", err)
	}
}

func TestGETSurvivesTwoConnectionResetsButNotThree(t *testing.T) {
	f := newFakeApex(t, nil)
	var mu sync.Mutex
	n := 0
	f.route("GET", "/jobs/", func(apexCall) reply {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n < 3 {
			return reply{hangup: true}
		}
		return jsonReply(map[string]any{"id": "j1"})
	})
	if _, err := get(t, config.Prod, "/jobs/j1", reqOpts{}); err != nil || n != 3 {
		t.Fatalf("GET after 2 resets: err=%v attempts=%d, want success on attempt 3", err, n)
	}

	f.on("GET", "/jobs/", reply{hangup: true})
	before := len(f.callsTo("GET", "/jobs/"))
	if _, err := get(t, config.Prod, "/jobs/j1", reqOpts{}); err == nil {
		t.Fatal("GET succeeded though every attempt was reset")
	}
	// net/http itself replays an idempotent request once when a reused connection is reset, so
	// the server may see more than our 3 attempts — never fewer.
	if got := len(f.callsTo("GET", "/jobs/")) - before; got < 3 {
		t.Errorf("attempts = %d, want at least 3", got)
	}
}

func TestPOSTIsNeverRetriedOnAReset(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("POST", "/queues/", reply{hangup: true})
	if _, err := apexRequest(t.Context(), config.PreProd, http.MethodPost, "/queues/q/items", reqOpts{Body: map[string]any{}}); err == nil {
		t.Fatal("POST succeeded though the connection was reset")
	}
	if n := len(f.callsTo("POST", "/queues/")); n != 1 {
		t.Errorf("POST attempts = %d, want 1", n)
	}
}

func TestGET429WaitsRetryAfterAndGivesUpAfterThreeRetries(t *testing.T) {
	f := newFakeApex(t, nil)
	slow := jsonReply(map[string]any{"detail": "slow"}, 429).with("Retry-After", "0")
	var mu sync.Mutex
	n := 0
	f.route("GET", "/jobs/", func(apexCall) reply {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return slow
		}
		return jsonReply(map[string]any{})
	})
	if _, err := get(t, config.Prod, "/jobs/j1", reqOpts{}); err != nil || n != 2 {
		t.Fatalf("GET after one 429: err=%v attempts=%d, want 2", err, n)
	}

	f.on("GET", "/jobs/", slow)
	_, err := get(t, config.Prod, "/jobs/j1", reqOpts{})
	if e := apiErr(t, err); e.Status != 429 {
		t.Errorf("status = %d, want 429", e.Status)
	}
	if got := len(f.callsTo("GET", "/jobs/")); got != 2+4 {
		t.Errorf("GETs = %d, want 6 (2 earlier + 1 try + 3 retries)", got)
	}
}

func TestWrite429IsNotRetried(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("POST", "/queues/", jsonReply(map[string]any{"detail": "slow"}, 429).with("Retry-After", "0"))
	_, err := apexRequest(t.Context(), config.PreProd, http.MethodPost, "/queues/q/items", reqOpts{Body: map[string]any{}})
	if e := apiErr(t, err); e.Status != 429 {
		t.Errorf("status = %d, want 429", e.Status)
	}
	if n := len(f.callsTo("POST", "/queues/")); n != 1 {
		t.Errorf("POST attempts = %d, want 1", n)
	}
}

func TestErrorMessages(t *testing.T) {
	failWith := func(t *testing.T, status int, body reply) *APIError {
		t.Helper()
		f := newFakeApex(t, nil)
		body.status = status
		f.on("GET", "/jobs/", body)
		_, err := get(t, config.Prod, "/jobs/j1", reqOpts{})
		return apiErr(t, err)
	}
	t.Run("a string detail becomes the message with status method and path on the error", func(t *testing.T) {
		e := failWith(t, 404, jsonReply(map[string]any{"detail": "Job not found"}))
		if e.Status != 404 || e.Method != "GET" || e.Path != "/jobs/j1" || !shared.IsExpected(e) {
			t.Errorf("error fields = %+v, want expected 404 GET /jobs/j1", e)
		}
		if e.Error() != "Apex GET /jobs/j1 -> 404: Job not found" {
			t.Errorf("message = %q", e.Error())
		}
	})
	t.Run("a pydantic detail list is flattened to loc: msg pairs", func(t *testing.T) {
		e := failWith(t, 422, jsonReply(map[string]any{"detail": []any{
			map[string]any{"loc": []any{"query", "page_size"}, "msg": "ensure this value is less than or equal to 200", "type": "x"},
			map[string]any{"loc": []any{"body", "reference"}, "msg": "field required", "type": "y"},
		}}))
		want := "query.page_size: ensure this value is less than or equal to 200; body.reference: field required"
		if !strings.Contains(e.Error(), want) {
			t.Errorf("message = %q, want it to contain %q", e.Error(), want)
		}
	})
	t.Run("a long detail is capped at 300 characters", func(t *testing.T) {
		e := failWith(t, 500, jsonReply(map[string]any{"detail": strings.Repeat("x", 1000)}))
		if want := "Apex GET /jobs/j1 -> 500: " + strings.Repeat("x", 300) + "…"; e.Error() != want {
			t.Errorf("message = %q, want the 300-char cap", e.Error())
		}
	})
	t.Run("a 403 adds the permission hint and a non-JSON error body is still reported", func(t *testing.T) {
		if e := failWith(t, 403, jsonReply(map[string]any{"detail": "Forbidden"})); !strings.Contains(e.Error(), "Forbidden — the Apex account lacks the role") {
			t.Errorf("403 message = %q, want the permission hint", e.Error())
		}
		if e := failWith(t, 502, reply{raw: []byte("bad gateway")}); !strings.Contains(e.Error(), "bad gateway") {
			t.Errorf("502 message = %q, want the raw body", e.Error())
		}
	})
	t.Run("a bodyless error still surfaces with its status, and a bodyless 403 keeps the hint", func(t *testing.T) {
		if e := failWith(t, 502, reply{}); e.Status != 502 || !strings.Contains(e.Error(), "HTTP 502") {
			t.Errorf("bodyless 502 = %q", e.Error())
		}
		if e := failWith(t, 403, reply{}); !strings.Contains(e.Error(), "the Apex account lacks the role") {
			t.Errorf("bodyless 403 = %q, want the permission hint", e.Error())
		}
	})
}

type idRow struct {
	ID string `json:"id"`
}

func pageOf(n, totalPages, size, rows int) reply {
	items := make([]map[string]string, rows)
	for i := range items {
		items[i] = map[string]string{"id": "p" + string(rune('0'+n)) + "-" + string(rune('a'+i%26))}
	}
	return jsonReply(map[string]any{"items": items, "total": totalPages * size, "page": n, "page_size": size, "total_pages": totalPages})
}

func param(t *testing.T, c apexCall, key string) string {
	t.Helper()
	u, err := url.Parse("https://x" + c.Path)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(key)
}

func TestApexPagesClampsPageSizeAndWalksToTotalPages(t *testing.T) {
	f := newFakeApex(t, nil)
	f.route("GET", "/jobs", func(c apexCall) reply {
		n := 1
		if p := param(t, c, "page"); p != "" {
			n = int(p[0] - '0')
		}
		return pageOf(n, 3, 200, 200)
	})
	rows, err := apexPages[idRow](t.Context(), config.Prod, "/jobs", map[string]string{"page_size": "500", "status": "failed"}, 10_000, "")
	if err != nil || len(rows) != 600 {
		t.Fatalf("apexPages = %d rows, %v, want 600", len(rows), err)
	}
	calls := f.callsTo("GET", "/jobs")
	if len(calls) != 3 {
		t.Fatalf("requests = %d, want 3 pages", len(calls))
	}
	// Page 1 carries total_pages so it goes first; pages 2..N are fetched concurrently (any order).
	for i, c := range calls {
		if i == 0 && param(t, c, "page") != "1" {
			t.Errorf("first request = %s, want page 1", c.Path)
		}
		if param(t, c, "page_size") != "200" || param(t, c, "status") != "failed" {
			t.Errorf("request %d = %s, want page_size 200, status failed", i, c.Path)
		}
	}
}

func TestApexPagesStopsAtMaxMidPageAskingForNoMoreThanNeeded(t *testing.T) {
	f := newFakeApex(t, nil)
	f.route("GET", "/jobs", func(apexCall) reply { return pageOf(1, 9, 7, 7) })
	rows, err := apexPages[idRow](t.Context(), config.Prod, "/jobs", nil, 7, "")
	if err != nil || len(rows) != 7 {
		t.Fatalf("apexPages = %d rows, %v, want 7", len(rows), err)
	}
	calls := f.callsTo("GET", "/jobs")
	if len(calls) != 1 || param(t, calls[0], "page_size") != "7" {
		t.Errorf("requests = %v, want one with page_size=7", calls)
	}
}

func TestApexPagesReturnsABareArrayRouteTruncatedToMax(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/j1/artifacts", jsonReply([]any{map[string]any{"id": "a"}, map[string]any{"id": "b"}, map[string]any{"id": "c"}}))
	rows, err := apexPages[idRow](t.Context(), config.Prod, "/jobs/j1/artifacts", nil, 2, "")
	if err != nil || len(rows) != 2 || rows[0].ID != "a" || rows[1].ID != "b" {
		t.Fatalf("apexPages = %v, %v, want [a b]", rows, err)
	}
	if n := len(f.callsTo("GET", "/jobs/j1/artifacts")); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestApexPagesEmptyPageEndsTheWalkEvenIfTotalPagesSaysMore(t *testing.T) {
	f := newFakeApex(t, nil)
	f.route("GET", "/jobs", func(apexCall) reply { return pageOf(1, 5, 200, 0) })
	rows, err := apexPages[idRow](t.Context(), config.Prod, "/jobs", nil, 1000, "")
	if err != nil || len(rows) != 0 {
		t.Fatalf("apexPages = %v, %v, want no rows", rows, err)
	}
	if n := len(f.callsTo("GET", "/jobs")); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

const downloadPath = "/api/v1/jobs/j1/artifacts/a1"

func TestDownloadForwardsRangeSendsBearerButNoFolderHeader(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/j1/artifacts/a1", reply{status: 206, raw: []byte{1, 2, 3}, headers: map[string]string{"Content-Range": "bytes 0-2/10"}})
	var buf strings.Builder
	d, err := apexDownload(t.Context(), downloadPath, "bytes=0-2", &buf)
	if err != nil || d.Status != 206 || d.ContentRange != "bytes 0-2/10" || d.Bytes != 3 || buf.String() != "\x01\x02\x03" {
		t.Fatalf("apexDownload = %+v, %v, body %q", d, err, buf.String())
	}
	c := f.callsTo("GET", "/jobs/j1/artifacts/a1")[0]
	if c.Header.Get("Range") != "bytes=0-2" || !strings.HasPrefix(bearer(c), "Bearer ") || c.Header.Get("X-Folder-Id") != "" {
		t.Errorf("headers = %v, want Range + Bearer and no folder header", c.Header)
	}
	if n := len(f.callsTo("GET", "/folders")); n != 0 {
		t.Errorf("folder lookups = %d, want 0", n)
	}
}

func TestDownloadFullBodyHasNoContentRangeAndNon2xxIsAnAPIErrorWithARelativePath(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/j1/artifacts/a1", reply{status: 200, raw: []byte("hello")})
	var buf strings.Builder
	if d, err := apexDownload(t.Context(), downloadPath, "", &buf); err != nil || d.ContentRange != "" {
		t.Fatalf("apexDownload = %+v, %v", d, err)
	}
	f.on("GET", "/jobs/j1/artifacts/a1", jsonReply(map[string]any{"detail": "gone"}, 404))
	_, err := apexDownload(t.Context(), downloadPath, "", &buf)
	if e := apiErr(t, err); e.Path != "/jobs/j1/artifacts/a1" || e.Status != 404 {
		t.Errorf("error = %+v, want 404 on the relative path", e)
	}
}

func TestDownloadRefusesNonAPIPathsWithoutSendingTheBearerAnywhere(t *testing.T) {
	for _, u := range []string{"https://evil.example.com/api/v1/x", "/jobs/j1/artifacts/a1", "/api/v1/../admin"} {
		t.Run(u, func(t *testing.T) {
			f := newFakeApex(t, nil)
			var buf strings.Builder
			if _, err := apexDownload(t.Context(), u, "", &buf); !shared.IsExpected(err) {
				t.Errorf("error = %v, want an expected refusal", err)
			}
			if n := f.count(); n != 0 {
				t.Errorf("%d requests made, want 0", n)
			}
		})
	}
}

func TestDashboardLinkJoinsTheBaseURLAndMakesNoRequest(t *testing.T) {
	f := newFakeApex(t, nil)
	if got := DashboardLink("/jobs/j1"); got != f.URL+"/jobs/j1" {
		t.Errorf("DashboardLink = %q, want base URL + path", got)
	}
	if n := f.count(); n != 0 {
		t.Errorf("%d requests made, want 0", n)
	}
}

func TestDashboardLinkIsEmptyWhenApexIsUnconfigured(t *testing.T) {
	newFakeApex(t, func(map[string]any) map[string]any { return nil })
	if got := DashboardLink("/jobs/j1"); got != "" {
		t.Errorf("DashboardLink = %q, want empty", got)
	}
}

func TestWritesGetUndiciSDefaultHeadersTimeout(t *testing.T) {
	if got := apexTransport().ResponseHeaderTimeout; got != 300*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 300s so a slow POST /jobs that started is not cut off", got)
	}
}
