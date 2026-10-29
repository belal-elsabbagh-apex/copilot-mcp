package copilot

import (
	"context"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

type call struct {
	Method, Path string
	Body         ReqBody
}

// fakeClient answers from a handler and records every call. Not goroutine-safe — tests that use
// it are sequential.
type fakeClient struct {
	env    config.Env
	handle func(c call) Response
	calls  []call
}

func (f *fakeClient) Base() string    { return "http://stub.invalid" }
func (f *fakeClient) Env() config.Env { return f.env }
func (f *fakeClient) Req(_ context.Context, method, path string, body ReqBody) (Response, error) {
	c := call{method, path, body}
	f.calls = append(f.calls, c)
	return f.handle(c), nil
}

// queued answers from a FIFO of responses (then an empty 200).
func queued(env config.Env, rs ...Response) *fakeClient {
	return &fakeClient{env: env, handle: func(call) Response {
		if len(rs) == 0 {
			return Response{Status: 200, Data: map[string]any{}, Text: "{}"}
		}
		r := rs[0]
		rs = rs[1:]
		return r
	}}
}

func (f *fakeClient) paths() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.Method + " " + c.Path
	}
	return out
}

// noWaits removes the real pauses (submit settle, /process retry) for the test's duration.
func noWaits(t *testing.T) {
	t.Helper()
	oldS, oldP := submitSettle, processRetryDelay
	submitSettle, processRetryDelay = 0, time.Millisecond
	t.Cleanup(func() { submitSettle, processRetryDelay = oldS, oldP })
}
