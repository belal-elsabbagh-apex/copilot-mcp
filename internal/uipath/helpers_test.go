package uipath

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

const (
	testKey  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testKey2 = "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee"
	testKey3 = "cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee"
)

// call is one recorded request.
type call struct {
	Method, Path, RawQuery string
	Header                 http.Header
	Body                   string
}

func (c call) query(k string) string {
	return mustQuery(c.RawQuery).Get(k)
}

// fake is an httptest Orchestrator: every request is recorded, then answered by handle.
type fake struct {
	srv    *httptest.Server
	mu     sync.Mutex
	recs   []call
	handle http.HandlerFunc
}

func newFake(t *testing.T, h http.HandlerFunc) *fake {
	t.Helper()
	f := &fake{handle: h}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.recs = append(f.recs, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(body)})
		f.mu.Unlock()
		f.handle(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) calls() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.recs...)
}

// callsTo are the recorded calls whose path ends with suffix.
func (f *fake) callsTo(suffix string) []call {
	var out []call
	for _, c := range f.calls() {
		if len(c.Path) >= len(suffix) && c.Path[len(c.Path)-len(suffix):] == suffix {
			out = append(out, c)
		}
	}
	return out
}

// testOrchestratorURL is what the config validator accepts; the fake answers it via redirect.
const testOrchestratorURL = "https://cloud.uipath.com/myorg/mytenant/orchestrator_"

func (f *fake) orchestratorURL() string { return testOrchestratorURL }

// redirect sends every request the client makes to the fake, whatever host it names.
type redirect struct{ to *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// useConfig points the config at the fake, with uipath overrides merged over the bearer default,
// and clears every piece of process-level UiPath state.
func (f *fake) useConfig(t *testing.T, uipath map[string]any) string {
	t.Helper()
	creds := func(n string) map[string]any {
		return map[string]any{"be": "https://be." + n + ".example.com", "email": n + "@example.com", "password": "pw"}
	}
	u := map[string]any{"orchestratorUrl": f.orchestratorURL(), "bearer": "test-bearer"}
	for k, v := range uipath {
		if v == nil {
			delete(u, k)
			continue
		}
		u[k] = v
	}
	target, _ := url.Parse(f.srv.URL)
	prev := httpClient
	httpClient = &http.Client{Transport: redirect{target}}
	t.Cleanup(func() { httpClient = prev })
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, path, map[string]any{
		"copilot": map[string]any{"sessionCache": false, "prod": creds("prod"), "pre_prod": creds("preprod")},
		"uipath":  u,
	})
	t.Setenv("COPILOT_MCP_CONFIG", path)
	resetState()
	t.Cleanup(resetState)
	return path
}

func resetState() {
	config.ResetForTest()
	invalidateBearerToken("")
	logCache.Flush()
	jobCache.Flush()
	gate.mu.Lock()
	gate.resumeAt = time.Time{}
	gate.mu.Unlock()
}

func writeConfigFile(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// okEmpty answers every request with an empty OData page.
func okEmpty(w http.ResponseWriter, _ *http.Request) {
	respond(w, http.StatusOK, map[string]any{"value": []any{}})
}

func page(rows ...any) map[string]any { return map[string]any{"value": rows} }

func mustQuery(raw string) url.Values {
	v, _ := url.ParseQuery(raw)
	return v
}

func configEnv(s string) config.Env { return config.Env(s) }
