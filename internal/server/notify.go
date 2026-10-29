package server

// MCP progress notifications (notifications/progress). A side-channel: it must never fail a tool
// handler, and never writes to stdout (that's the JSON-RPC channel). Client-visible logging
// (notifications/message) lives in logging.go.

import (
	"context"
	"strconv"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// heartbeatEvery is the cadence for withHeartbeat callers that have no reason to pick their own.
//
//nolint:unused // the tool sets pass it to withHeartbeat
const heartbeatEvery = 5 * time.Second

// progressToken is the token the client put in the request _meta, or nil when it didn't opt in.
func progressToken(req *mcp.CallToolRequest) any {
	if req == nil || req.Params == nil {
		return nil
	}
	return req.Params.GetProgressToken()
}

// reportProgress emits a progress notification for the in-flight request, but only when the
// client opted in by supplying a progressToken. total <= 0 and an empty message are omitted.
func reportProgress(ctx context.Context, req *mcp.CallToolRequest, progress, total float64, message string) {
	token := progressToken(req)
	if token == nil || req.Session == nil {
		return
	}
	_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
		ProgressToken: token, Progress: progress, Total: max(total, 0), Message: message,
	})
}

// stepProgress hands a domain function a StepProgress that forwards to this request's progress
// channel, or nil (a valid StepProgress) when the client didn't ask for progress.
func stepProgress(ctx context.Context, req *mcp.CallToolRequest) shared.StepProgress {
	if progressToken(req) == nil {
		return nil
	}
	return func(done, total int, label string) {
		reportProgress(ctx, req, float64(done), float64(total), label)
	}
}

// withHeartbeat emits an elapsed-time heartbeat while work is in flight, then stops. For tools
// that are slow because of ONE long round-trip (Orchestrator queue/job reads, a BE login) — there
// is no step sequence to count, so this reports time, not steps: progress is the tick number
// (monotonic, as the spec requires) and the message carries the elapsed seconds.
//
// No goroutine is ever started when the client didn't ask for progress. The ticker goroutine is
// always joined before return, so no tick can outlive the request.
func withHeartbeat[T any](ctx context.Context, req *mcp.CallToolRequest, label string, every time.Duration, work func() (T, error)) (T, error) {
	if progressToken(req) == nil {
		return work()
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for tick := 1; ; tick++ {
			select {
			case <-stop:
				return
			case <-ticker.C:
				secs := strconv.FormatFloat(float64(tick)*every.Seconds(), 'f', -1, 64)
				reportProgress(ctx, req, float64(tick), 0, label+": waiting ("+secs+"s)")
			}
		}
	}()
	defer func() { close(stop); <-done }()
	return work()
}
