package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type noArgs struct{}

// callWithToken calls tool name with a progress token, like a client that opted in to progress.
func callWithToken(t *testing.T, cs *mcp.ClientSession, name string) {
	t.Helper()
	params := &mcp.CallToolParams{Name: name}
	params.SetProgressToken("tok")
	if _, err := cs.CallTool(t.Context(), params); err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
}

// progressTool registers a tool that runs body with the request, for notify tests.
func progressTool(s *Server, name string, body func(context.Context, *mcp.CallToolRequest)) {
	addTool(s, &mcp.Tool{Name: name, Description: "test tool"}, func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (any, error) {
		body(ctx, req)
		return "done", nil
	})
}

func TestProgressIsOnlyEmittedWhenTheClientSuppliedAToken(t *testing.T) {
	s := newTestServer(t)
	progressTool(s, "p", func(ctx context.Context, req *mcp.CallToolRequest) {
		reportProgress(ctx, req, 2, 5, "halfway")
	})
	cs, ev := connect(t, s)

	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "p"}); err != nil {
		t.Fatal(err)
	}
	noEvent(t, ev.progress)

	callWithToken(t, cs, "p")
	got := recv(t, ev.progress)
	if got.ProgressToken != "tok" || got.Progress != 2 || got.Total != 5 || got.Message != "halfway" {
		t.Errorf("progress = %+v, want token tok, 2 of 5, halfway", got)
	}
}

func TestStepProgressForwardsToTheRequestAndIsNilWithoutAToken(t *testing.T) {
	s := newTestServer(t)
	progressTool(s, "p", func(ctx context.Context, req *mcp.CallToolRequest) {
		stepProgress(ctx, req).Step(3, 4, "page 3")
	})
	cs, ev := connect(t, s)
	callWithToken(t, cs, "p")
	if got := recv(t, ev.progress); got.Progress != 3 || got.Total != 4 || got.Message != "page 3" {
		t.Errorf("progress = %+v, want 3 of 4 page 3", got)
	}

	if stepProgress(t.Context(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}) != nil {
		t.Error("stepProgress() without a token is non-nil, want nil so domain code skips the work")
	}
}

func TestHeartbeatTicksWhileWorkIsInFlightAndStopsOnceItSettles(t *testing.T) {
	s := newTestServer(t)
	progressTool(s, "p", func(ctx context.Context, req *mcp.CallToolRequest) {
		_, _ = withHeartbeat(ctx, req, "Orchestrator", 30*time.Millisecond, func() (string, error) {
			time.Sleep(140 * time.Millisecond)
			return "done", nil
		})
	})
	cs, ev := connect(t, s)
	callWithToken(t, cs, "p")

	first := recv(t, ev.progress)
	if !strings.HasPrefix(first.Message, "Orchestrator: waiting (") || first.Progress != 1 {
		t.Fatalf("first tick = %+v, want progress 1 labelled Orchestrator", first)
	}
	if second := recv(t, ev.progress); second.Progress != 2 {
		t.Errorf("second tick progress = %v, want 2 (monotonic, as the spec requires)", second.Progress)
	}
	time.Sleep(100 * time.Millisecond)
	for len(ev.progress) > 0 { // drain ticks that fired before the work settled
		<-ev.progress
	}
	noEvent(t, ev.progress)
}

func TestHeartbeatNeverStartsATimerWithoutAToken(t *testing.T) {
	calls := 0
	got, err := withHeartbeat(t.Context(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}, "x", time.Nanosecond, func() (int, error) {
		calls++
		return 7, nil
	})
	if got != 7 || err != nil || calls != 1 {
		t.Errorf("withHeartbeat() = %d, %v after %d calls, want 7, nil after 1", got, err, calls)
	}
}

func TestHeartbeatPropagatesTheWorkError(t *testing.T) {
	s := newTestServer(t)
	want := errors.New("nope")
	var got error
	progressTool(s, "p", func(ctx context.Context, req *mcp.CallToolRequest) {
		_, got = withHeartbeat(ctx, req, "BE", 20*time.Millisecond, func() (int, error) { return 0, want })
	})
	cs, _ := connect(t, s)
	callWithToken(t, cs, "p")
	if !errors.Is(got, want) {
		t.Errorf("withHeartbeat() error = %v, want %v", got, want)
	}
}
