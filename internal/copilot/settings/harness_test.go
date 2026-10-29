package settings

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
)

// testBE is one tenant's BE behind a real HTTP server, so the exported operations run through the
// real client, login and config.
type testBE struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []recorded
	handle func(method, path string, body any) (int, any)
}

func newBE(t *testing.T, routes map[string]any) *testBE {
	t.Helper()
	b := &testBE{}
	b.handle = func(method, path string, _ any) (int, any) {
		if data, ok := routes[method+" "+path]; ok {
			return 200, data
		}
		return 404, map[string]any{"error": "no route " + method + " " + path}
	}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.HasSuffix(r.URL.Path, "physician/login") {
			claims, _ := json.Marshal(map[string]any{"id": 1, "timestamp": time.Now().Add(time.Hour).UnixMilli()})
			fmt.Fprintf(w, `{"token":"h.%s.s"}`, base64.StdEncoding.EncodeToString(claims))
			return
		}
		b.mu.Lock()
		b.reqs = append(b.reqs, recorded{r.Method, r.URL.Path, body})
		h := b.handle
		b.mu.Unlock()
		status, data := h(r.Method, r.URL.Path, body)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(data)
	}))
	t.Cleanup(b.Close)
	return b
}

func (b *testBE) requests() []recorded {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]recorded(nil), b.reqs...)
}

func (b *testBE) writes() []recorded {
	var out []recorded
	for _, r := range b.requests() {
		if r.Method != "GET" {
			out = append(out, r)
		}
	}
	return out
}

// useBEs points the config at the two tenants as profile "ossm" (direct login, no disk cache).
func useBEs(t *testing.T, prod, pre *testBE) {
	t.Helper()
	creds := func(b *testBE, email string) map[string]any {
		return map[string]any{"be": b.URL, "email": email, "password": "pw"}
	}
	cfg, _ := json.Marshal(map[string]any{
		"copilot": map[string]any{
			"sessionCache": false,
			"profiles":     map[string]any{"ossm": map[string]any{"prod": creds(prod, "p@example.com"), "pre_prod": creds(pre, "q@example.com")}},
		},
		"uipath": map[string]any{"orchestratorUrl": "https://cloud.uipath.com/o/t/orchestrator_", "bearer": "x"},
	})
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_MCP_CONFIG", path)
	t.Setenv("COPILOT_MCP_CACHE", filepath.Join(t.TempDir(), "sessions.json"))
	config.ResetForTest()
	copilot.ResetSessionCacheForTest()
	t.Cleanup(func() { config.ResetForTest(); copilot.ResetSessionCacheForTest() })
}
