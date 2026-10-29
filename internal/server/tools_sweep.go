package server

import (
	"context"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/sweep"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerSweepTools) }

type findStuckOrdersArgs struct {
	Env            config.Env                `json:"env" jsonschema:"Which env to scan (required)"`
	Profile        string                    `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	ScanPages      int                       `json:"scanPages,omitempty" jsonschema:"Pages of 50 recent orders to scan"`
	Statuses       []string                  `json:"statuses,omitempty" jsonschema:"Override the 'stuck' status set (default inProgress/incomplete/pending)"`
	OlderThanHours float64                   `json:"olderThanHours,omitempty" jsonschema:"Only flag orders at least this many hours old (needs a parseable date on the row)"`
	CrossCheck     bool                      `json:"crossCheck,omitempty" jsonschema:"Correlate each stuck order to its queue item(s)/job(s) for a verdict"`
	Orchestrator   orchestrator.Orchestrator `json:"orchestrator,omitempty" jsonschema:"With crossCheck: check only this orchestrator (uipath | apex); omit to check every orchestrator"`
	// Since's description has backticks, which a struct tag cannot hold: see the patch below.
	Since string `json:"since,omitempty"`
	Top   int    `json:"top,omitempty" jsonschema:"Max queue items to consider per order, per orchestrator, in the cross-check"`
}

func findStuckOrders(ctx context.Context, req *mcp.CallToolRequest, in findStuckOrdersArgs) (any, error) {
	return sweep.FindStuckOrders(ctx, sweep.Args{
		Env: in.Env, Profile: in.Profile, ScanPages: in.ScanPages, Statuses: in.Statuses,
		OlderThanHours: in.OlderThanHours, CrossCheck: in.CrossCheck, Orchestrator: in.Orchestrator,
		Since: in.Since, Top: in.Top, OnProgress: stepProgress(ctx, req),
	})
}

func registerSweepTools(s *Server) {
	addTool(s, &mcp.Tool{
		Name:  "find_stuck_orders",
		Title: "Find stuck Copilot orders",
		Description: "Scan recent orders in an env and flag the ones sitting in a non-terminal ('stuck') status " +
			"(default inProgress/incomplete/pending). Optionally (`crossCheck`) correlate each to its queue " +
			"item(s) on the orchestrator(s) — omit `orchestrator` to check every orchestrator, since where " +
			"the item actually sits is the truth — for a coarse verdict: no-job / job-never-started / " +
			"job-faulted / job-running / queued-not-picked-up / job-successful-order-stuck / job-found " +
			"(or 'error: …' when the lookup failed, which is NOT 'no-job'). Verdicts come from the newest " +
			"queue item's status and latest attempt (PHI-safe — member data / tokens are never surfaced). " +
			"job-never-started = the latest attempt was reaped before any worker ran it (e.g. a missing " +
			"credential on Apex): a platform/config problem, not order data. READ-ONLY. Returns {env, " +
			"scanned, statuses, found, stuck:[{orderUid,status,ageHours,orchestrator?:{backend," +
			"verdict,queueItemCount,queueItemStatus?,exceptionType?}}]} where `backend` is the " +
			"orchestrator holding the newest queue item (null when none / lookup failed).",
		Annotations: readOnlyHints(true),
	}, findStuckOrders,
		Enum("env", config.Envs...),
		MinLen("profile", 1),
		Range("scanPages", 1, 20), Default("scanPages", 8),
		OnProp("olderThanHours", func(s *jsonschema.Schema) { s.Minimum = new(0.0) }),
		Default("crossCheck", false),
		Enum("orchestrator", orchestrator.Orchestrators...),
		OnProp("since", func(s *jsonschema.Schema) {
			s.Description = "Explicit ISO floor for the queue-item search. Each order's own creationDate " +
				"already tightens its search to that date (queue items can't predate the order), so " +
				"this is only needed to push the floor even later, or when crossCheck is used " +
				"without a BE-sourced creationDate"
		}),
		Range("top", 1, 500), Default("top", 50),
	)
}
