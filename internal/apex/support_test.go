package apex

// Shared fixtures for tests that run the REAL Apex client against an httptest server.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

const (
	testServerURL = "https://be.pre.example"
	testQueueURL  = "https://sqs.example/q"

	jobID      = "aaaaaaaa-0000-4000-8000-000000000001"
	workflowID = "bbbbbbbb-0000-4000-8000-000000000001"
	queueID    = "cccccccc-0000-4000-8000-000000000001"
	itemID     = "dddddddd-0000-4000-8000-000000000001"
)

var folderIDs = map[string]string{
	"Authorization":      "11111111-1111-1111-1111-111111111111",
	"Authorization Test": "22222222-2222-2222-2222-222222222222",
	"Eligibility Check":  "33333333-3333-3333-3333-333333333333",
}

var folderOrder = []string{"Authorization", "Authorization Test", "Eligibility Check"}

// apexCall is one recorded request.
type apexCall struct {
	Method string
	Path   string // after /api/v1, query string included
	Header http.Header
	Body   any // parsed JSON, nil when none
}

func (c apexCall) route() string { return c.Method + " " + strings.SplitN(c.Path, "?", 2)[0] }

// reply is what a route answers.
type reply struct {
	status  int
	body    any    // JSON-encoded unless raw is set
	raw     []byte // verbatim body
	headers map[string]string
	hangup  bool // drop the connection, like a network reset
}

func jsonReply(body any, status ...int) reply {
	r := reply{status: 200, body: body}
	if len(status) > 0 {
		r.status = status[0]
	}
	return r
}

func (r reply) with(k, v string) reply {
	r.headers = map[string]string{k: v}
	return r
}

type handler func(c apexCall) reply

type route struct {
	method, prefix string
	h              handler
}

// fakeApex is an Apex server: routes match by method + path prefix (after /api/v1); the LAST
// registered match wins, so a test overrides the default login/folders routes by registering its
// own. Unmatched requests answer 404.
type fakeApex struct {
	t      *testing.T
	URL    string
	mu     sync.Mutex
	calls  []apexCall
	routes []route
}

func b64(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

// fakeJWT is a structurally real (unsigned) JWT: the client only decodes the payload's `exp`.
func fakeJWT(exp time.Time) string {
	return b64(map[string]any{"alg": "none"}) + "." + b64(map[string]any{"sub": "u", "exp": exp.Unix()}) + ".sig"
}

func longLived() string { return fakeJWT(time.Now().Add(24 * time.Hour)) }

func loginReturning(jwts ...string) handler {
	var mu sync.Mutex
	n := 0
	return func(apexCall) reply {
		mu.Lock()
		defer mu.Unlock()
		jwt := jwts[min(n, len(jwts)-1)]
		n++
		return jsonReply(map[string]any{}).with("Set-Cookie", "access_token="+jwt+"; HttpOnly")
	}
}

func foldersReply() reply {
	items := make([]map[string]any, 0, len(folderOrder))
	for _, name := range folderOrder {
		items = append(items, map[string]any{"id": folderIDs[name], "name": name})
	}
	return jsonReply(map[string]any{"items": items, "total": 3, "page": 1, "page_size": 200, "total_pages": 1})
}

func pagedReply(items any, n int) reply {
	return jsonReply(map[string]any{"items": items, "total": n, "page": 1, "page_size": 200, "total_pages": 1})
}

func defaultApexSection(url string) map[string]any {
	return map[string]any{
		"apiUrl": url, "username": "tester", "password": "pw",
		"serverUrlByEnv": map[string]any{"pre_prod": testServerURL}, "queueUrl": testQueueURL,
	}
}

// newFakeApex starts the server and points the config at it. mutate edits the apex config
// section (nil section = no apex section at all); the apiUrl is already set.
func newFakeApex(t *testing.T, mutate func(section map[string]any) map[string]any) *fakeApex {
	t.Helper()
	f := &fakeApex{t: t}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	f.route("POST", "/auth/login", loginReturning(longLived()))
	f.route("GET", "/folders", func(apexCall) reply { return foldersReply() })

	section := defaultApexSection(srv.URL)
	if mutate != nil {
		section = mutate(section)
	}
	creds := func(n string) map[string]any {
		return map[string]any{"be": "https://be." + n + ".example.com", "email": n + "@x.com", "password": "pw"}
	}
	cfg := map[string]any{
		"copilot": map[string]any{"sessionCache": false, "prod": creds("prod"), "pre_prod": creds("preprod")},
		"uipath":  map[string]any{"orchestratorUrl": "https://cloud.uipath.com/o/t/orchestrator_", "bearer": "b"},
	}
	if section != nil {
		cfg["apex"] = section
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_MCP_CONFIG", path)
	for _, k := range []string{"APEX_API_URL", "APEX_USER_NAME", "APEX_PASSWORD"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	config.ResetForTest()
	ResetClient()
	t.Cleanup(func() { config.ResetForTest(); ResetClient() })
	return f
}

func (f *fakeApex) route(method, prefix string, h handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = append(f.routes, route{method, prefix, h})
}

func (f *fakeApex) on(method, prefix string, r reply) {
	f.route(method, prefix, func(apexCall) reply { return r })
}

func (f *fakeApex) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c := apexCall{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), apiPrefix), Header: r.Header}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &c.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	var h handler
	for _, rt := range f.routes {
		if rt.method == c.Method && strings.HasPrefix(c.Path, rt.prefix) {
			h = rt.h
		}
	}
	f.mu.Unlock()
	rep := jsonReply(map[string]any{"detail": fmt.Sprintf("no stub for %s %s", c.Method, c.Path)}, 404)
	if h != nil {
		rep = h(c)
	}
	if rep.hangup {
		panic(http.ErrAbortHandler)
	}
	for k, v := range rep.headers {
		w.Header().Set(k, v)
	}
	payload := rep.raw
	if payload == nil && rep.body != nil {
		w.Header().Set("Content-Type", "application/json")
		payload, _ = json.Marshal(rep.body)
	}
	w.WriteHeader(rep.status)
	_, _ = w.Write(payload)
}

func (f *fakeApex) all() []apexCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apexCall(nil), f.calls...)
}

func (f *fakeApex) callsTo(method, prefix string) []apexCall {
	var out []apexCall
	for _, c := range f.all() {
		if c.Method == method && strings.HasPrefix(c.Path, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// actionCalls is everything except login + folder resolution: what the operation itself did.
func (f *fakeApex) actionCalls() []apexCall {
	var out []apexCall
	for _, c := range f.all() {
		if !strings.HasPrefix(c.Path, "/auth/") && !strings.HasPrefix(c.Path, "/folders") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeApex) writes() []apexCall {
	var out []apexCall
	for _, c := range f.actionCalls() {
		if c.Method != http.MethodGet {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeApex) sequence() []string {
	var out []string
	for _, c := range f.actionCalls() {
		out = append(out, c.route())
	}
	return out
}

func (f *fakeApex) count() int { return len(f.all()) }

// ---- synthetic, PHI-free row builders (shapes mirror the live keys in api.go) ----------------

func sp(s string) *string { return &s }

func jobRowFixture() JobRow {
	fp := 263.400669
	return JobRow{
		ID: jobID, Status: "completed", WorkflowID: workflowID, FolderID: "22222222-2222-4222-8222-222222222222",
		Source: sp("sqs"), QueueItemID: sp(itemID), QueueID: sp(queueID), QueueItemRef: sp("1000000001"),
		WorkflowName: "demo-auth-submit", QueueName: sp("DEMO auth submit queue"), CredentialName: sp("demo-cred"),
		AttemptNumber: 1, WorkerID: sp("eeeeeeee-0000-4000-8000-000000000001"), WorkflowVersion: sp("1.0.2"),
		CallbackStatus: sp("sent"), StartedAt: sp("2026-10-08T00:39:51.248748Z"), CompletedAt: sp("2026-10-08T00:44:14.649417Z"),
		CreatedAt: "2026-10-08T00:39:49.044176Z", DurationSeconds: &fp,
	}
}

func jobDetailFixture() JobDetail {
	zero, timeout := 0, 900
	return JobDetail{
		JobRow: jobRowFixture(),
		InputPayload: map[string]any{
			"orderUid": "order-1", "token": "synthetic-token", "callbackContext": map[string]any{"orderUid": "order-1"}, "retryCount": 0,
		},
		ResultPayload: json.RawMessage(`{"result":"approved"}`), ExitCode: &zero, EffectiveTimeoutSeconds: &timeout,
	}
}

func queueFixture() Queue {
	return Queue{
		ID: queueID, Name: "DEMO auth submit queue", WorkflowID: sp(workflowID), IsActive: true, AutoDrain: true,
		MaxConcurrentJobs: new(1),
	}
}

func itemFixture() QueueItem {
	return QueueItem{
		ID: itemID, QueueID: queueID, Reference: "1000000001",
		SpecificContent: map[string]any{
			"orderUid": "order-1", "token": "synthetic-token", "IsApproved": true,
			"MemberFirstName": "Jane", "MemberLastName": "Doe",
		},
		MatchValue: sp("auto-1"), Priority: new(20), Status: "failed", ExceptionType: sp("business"), JobID: sp(jobID),
		CreatedAt: "2026-10-08T00:29:26.164862Z", EndedAt: sp("2026-10-08T00:44:14.655419Z"),
	}
}

func workflowFixture() Workflow {
	return Workflow{
		ID: workflowID, Name: "demo-auth-submit", PackageName: sp("demo-auth-submit"), IsActive: true,
		TimeoutSeconds: new(900), MaxAttempts: new(3), BackoffStrategy: "exponential", ArtifactPolicy: "on_failure", CurrentVersion: sp("1.0.2"),
	}
}

func artifactFixture(filename, kind string, size int64) Artifact {
	id := "99999999-" + strings.ReplaceAll(filename, ".", "-")
	return Artifact{
		ID: id, Filename: filename, ArtifactType: kind, SizeBytes: size,
		DownloadURL: apiPrefix + "/jobs/" + jobID + "/artifacts/" + id,
	}
}
