package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
)

// j parses a JSON literal into the untyped form the BE client hands back.
func j(t testing.TB, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad JSON fixture: %v\n%s", err, s)
	}
	return v
}

func respond(status int, data any) copilot.Response {
	text, _ := json.Marshal(data)
	return copilot.Response{Status: status, Data: data, Text: string(text)}
}

type recorded struct {
	Method, Path string
	Body         any
}

// fakeBE is a goroutine-safe copilot.Client answering from a handler. It records every call and
// the peak number of requests in flight at once.
type fakeBE struct {
	env    config.Env
	handle func(method, path string, body any) copilot.Response
	delay  time.Duration
	reqErr error // when set, Req fails with it (a transport failure)

	mu          sync.Mutex
	calls       []recorded
	inflight    atomic.Int32
	maxInflight atomic.Int32
}

func (f *fakeBE) Base() string    { return "https://be." + string(f.env) + ".example.com" }
func (f *fakeBE) Env() config.Env { return f.env }

func (f *fakeBE) Req(_ context.Context, method, path string, body copilot.ReqBody) (copilot.Response, error) {
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxInflight.Load()
		if n <= m || f.maxInflight.CompareAndSwap(m, n) {
			break
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, recorded{method, path, body.JSON})
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.reqErr != nil {
		return copilot.Response{}, f.reqErr
	}
	return f.handle(method, path, body.JSON), nil
}

// count is the number of calls to exactly method+path (a query string counts as part of the path
// only when given).
func (f *fakeBE) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method && (c.Path == path || strings.HasPrefix(c.Path, path+"?")) {
			n++
		}
	}
	return n
}

func (f *fakeBE) writes() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, c := range f.calls {
		if c.Method != "GET" {
			out = append(out, c)
		}
	}
	return out
}

// routes builds a handler from an exact-path table; unknown paths are a 404 so a stray request
// fails loudly.
func routes(table map[string]any) func(string, string, any) copilot.Response {
	return func(method, path string, _ any) copilot.Response {
		if data, ok := table[method+" "+path]; ok {
			return respond(200, data)
		}
		return respond(404, map[string]any{"error": fmt.Sprintf("no route %s %s", method, path)})
	}
}
