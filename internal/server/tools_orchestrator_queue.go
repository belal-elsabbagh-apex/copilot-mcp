package server

import (
	"context"
	"maps"
	"net/url"
	"slices"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/backends"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/queueitem"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerOrchestratorQueueTools) }

type buildQueueItemArgs struct {
	OrderUID     string                    `json:"orderUid" jsonschema:"Order UID to fetch (must exist in the chosen env)"`
	Env          config.Env                `json:"env" jsonschema:"Which env the order lives in; drives fetch + token + serverURL (required)"`
	Profile      string                    `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	Orchestrator orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	AutomationID string                    `json:"automationId,omitempty"`
}

type buildQueueItemOut struct {
	Payload any            `json:"payload"`
	Notes   []string       `json:"notes"`
	Meta    map[string]any `json:"meta"`
}

type findOrderArgs struct {
	OrderUID               string                    `json:"orderUid" jsonschema:"Copilot orderUid to correlate"`
	Env                    config.Env                `json:"env" jsonschema:"Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; Apex prod='Authorization', pre_prod='Authorization Test' (required)"`
	Orchestrator           orchestrator.Orchestrator `json:"orchestrator,omitempty" jsonschema:"Search only this orchestrator (uipath | apex); omit to search every orchestrator"`
	Folder                 string                    `json:"folder,omitempty"`
	Since                  string                    `json:"since,omitempty" jsonschema:"ISO lower bound on the queue item's creation time — pass the order's creationDate from get_order; each orchestrator retries without the bound if nothing matches"`
	Top                    int                       `json:"top,omitempty" jsonschema:"Max queue items per orchestrator"`
	IncludeSpecificContent bool                      `json:"includeSpecificContent,omitempty"`
}

type findOrderOut struct {
	OrderUID string     `json:"orderUid"`
	Env      config.Env `json:"env"`
	backends.AcrossResult
}

type pullQueueItemArgs struct {
	Orchestrator orchestrator.Orchestrator `json:"orchestrator" jsonschema:"Which orchestrator: uipath | apex (required)"`
	Env          config.Env                `json:"env,omitempty" jsonschema:"Folder/env; UiPath derives it from the URL's fid when a url is given, otherwise required"`
	URL          string                    `json:"url,omitempty" jsonschema:"UiPath only: Orchestrator queue-item URL (copy from the UI; fid + item id are parsed out)"`
	QueueID      string                    `json:"queueId,omitempty" jsonschema:"Apex: the queue's id (from list_queues); UiPath derives the queue from the item"`
	ItemID       string                    `json:"itemId,omitempty" jsonschema:"Queue item id (UiPath numeric id as a string | Apex uuid); use instead of url"`
	FolderID     string                    `json:"folderId,omitempty" jsonschema:"UiPath: numeric folder id (OrganizationUnitId) override when no url is given"`
}

type pullQueueItemOut struct {
	Orchestrator orchestrator.Orchestrator `json:"orchestrator"`
	orchestrator.PullQueueItemResult
}

// validateURL is zod's .url() (which the SDK's JSON-schema `format` does not enforce): a
// parseable absolute URL.
func validateURL(raw string) error {
	if u, err := url.Parse(raw); err != nil || u.Scheme == "" {
		return shared.Expectedf("Invalid arguments for tool pull_queue_item: url: Invalid url")
	}
	return nil
}

func registerOrchestratorQueueTools(s *Server) {
	addTool(s, &mcp.Tool{
		Name:        "build_queue_item",
		Title:       "Build an orchestrator queue-item request from an order",
		Annotations: readOnlyHints(true), // BUILD ONLY — fetches an order, never POSTs
		Description: "Fetch an EHR Copilot order (prod or pre-prod) and build the queue-item request payload for " +
			"the chosen orchestrator from its details. BUILD ONLY — never POSTs to the orchestrator; " +
			"submit it yourself (for UiPath, to meta.postUrl with your own Orchestrator bearer token — see " +
			"notes; or hand payload.itemData's Name/Reference/SpecificContent to add_queue_item). " +
			"IsApproved is always false. The payload's wire format is the orchestrator's own: UiPath " +
			"carries the array fields and callbackContext as JSON strings, Apex keeps native types. " +
			"Returns {payload, notes, meta}.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in buildQueueItemArgs) (any, error) {
		built, err := withHeartbeat(ctx, req, "building queue item", heartbeatEvery, func() (queueitem.BuiltQueueItem, error) {
			return queueitem.Build(ctx, in.OrderUID, queueitem.Opts{Profile: in.Profile, Env: in.Env, AutomationID: in.AutomationID})
		})
		if err != nil {
			return nil, err
		}
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		p, err := b.BuildQueueItemPayload(ctx, orchestrator.QueueItemPayloadArgs{
			Env: in.Env, QueueName: built.QueueName, OrderUID: in.OrderUID, Content: built.Content, BeBase: built.BeBase,
		})
		if err != nil {
			return nil, err
		}
		meta := maps.Clone(built.Meta)
		if meta == nil {
			meta = map[string]any{}
		}
		maps.Copy(meta, p.Meta)
		return buildQueueItemOut{p.Payload, slices.Concat(built.Notes, p.Notes), meta}, nil
	},
		MinLen("orderUid", 8), envEnum(), MinLen("profile", 1), orchestratorEnum(), MinLen("automationId", 1),
		// The descriptions quote `profile` / `orchestrator` in backticks, which a struct tag cannot hold.
		describe("automationId", "The account's real automation id (from its orchestrator asset). Usually unnecessary — "+
			"when omitted, it's resolved from `profile` against the known account table; only "+
			"needed to override that or for an account not yet in the table (falls back to a "+
			"placeholder random UUID + warning note in that case)."),
	)

	addTool(s, &mcp.Tool{
		Name:        "find_order_queue_items",
		Title:       "Find the queue item(s) a Copilot order flowed through",
		Annotations: readOnlyHints(true),
		Description: "Given a Copilot orderUid, find the orchestrator queue item(s) it flowed through and the " +
			"job ids that ran them — every order flows through a queue item carrying the orderUid and " +
			"its job(s), so this reaches faulted/still-running consumer jobs an OutputArguments scan " +
			"can't. Omit `orchestrator` to search EVERY orchestrator (uipath | apex) in parallel: routing " +
			"is decided per portal outside this repo, so where the item actually sits is the truth. Each " +
			"orchestrator fails independently into `searchErrors` without losing the others' items. " +
			"`folder` is accepted only together with an explicit `orchestrator`. READ-ONLY. The item's " +
			"full specificContent is returned deliberately, in full (member/clinical fields visible) — " +
			"the point is divergence analysis: comparing what Copilot actually sent against the order " +
			"record from get_order. Only the JWT `token` is redacted (a live credential, opaque, useless " +
			"for divergence). Feed `jobIds[<orchestrator>]` into get_job's jobIds with THAT orchestrator " +
			"and includeLogDigest=true to diagnose every run and retry in one call per orchestrator " +
			"(Apex lists every attempt of a retried item). `count === 0` with no `searchErrors` means the " +
			"order never reached any searched orchestrator; with `searchErrors` it is inconclusive for " +
			"the failed ones. `jobIds[o]: []` with items present means queued but not yet picked up by a " +
			"worker. Env picks each orchestrator's folder (see copilot://reference/orchestrators). " +
			"`since` narrows the queue-item search (each orchestrator retries unbounded if nothing " +
			"matches) — pass the order's creationDate from get_order. Returns {orderUid, env, count, " +
			"jobIds:{<orchestrator>:string[]}, items:QueueItemCore[] (newest first), searched, " +
			"backends:{<orchestrator>:{folder,searchMode,scanned}}, notes? (prefixed '<orchestrator>: '), " +
			"searchErrors?:{<orchestrator>:string}}.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in findOrderArgs) (any, error) {
		bs, err := backends.SelectBackends(in.Orchestrator, in.Folder)
		if err != nil {
			return nil, err
		}
		found, err := withHeartbeat(ctx, req, "Orchestrator", heartbeatEvery, func() (backends.AcrossResult, error) {
			return backends.FindOrderAcross(ctx, orchestrator.FindOrderArgs{
				Env: in.Env, OrderUID: in.OrderUID, Folder: in.Folder, Since: in.Since, Top: in.Top,
				IncludeSpecificContent: in.IncludeSpecificContent,
			}, bs), nil
		})
		if err != nil {
			return nil, err
		}
		return findOrderOut{in.OrderUID, in.Env, found}, nil
	},
		MinLen("orderUid", 8), envEnum(), orchestratorEnum(), Range("top", 1, 500), Default("top", 50),
		Default("includeSpecificContent", true),
		describe("folder", "Explicit folder override for the one orchestrator named by `orchestrator` (wins "+
			"over env); refused without `orchestrator`"),
	)

	addTool(s, &mcp.Tool{
		Name:        "pull_queue_item",
		Title:       "Pull a queue item as a test payload",
		Annotations: readOnlyHints(true),
		Description: "Fetch a single orchestrator queue item and return its specificContent as a ready-to-run " +
			"test payload, mapped to its portal. READ-ONLY. IsApproved is ALWAYS forced false so a test " +
			"run can never submit a real auth. UiPath: pass `url` (an Orchestrator queue-item URL — env " +
			"is derived from its fid) or `itemId` + `env` (+ optional `folderId`). Apex: pass `env` + " +
			"`queueId` + `itemId`. Returns {orchestrator, env, item:QueueItemCore (specificContent " +
			"present), portal:{matched,key,portalDir,buildArtifact,family}, isApprovedForced, " +
			"suggestedFilename, queueNameError?}.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in pullQueueItemArgs) (any, error) {
		if in.URL != "" {
			if err := validateURL(in.URL); err != nil {
				return nil, err
			}
		}
		b, err := backendOf(in.Orchestrator)
		if err != nil {
			return nil, err
		}
		res, err := withHeartbeat(ctx, req, "Orchestrator", heartbeatEvery, func() (orchestrator.PullQueueItemResult, error) {
			return b.PullQueueItem(ctx, orchestrator.PullQueueItemArgs{
				Env: in.Env, URL: in.URL, QueueID: in.QueueID, ItemID: in.ItemID, FolderID: in.FolderID,
			})
		})
		if err != nil {
			return nil, err
		}
		return pullQueueItemOut{in.Orchestrator, res}, nil
	},
		orchestratorEnum(), envEnum(), MinLen("queueId", 1), MinLen("itemId", 1),
		// The SDK does not enforce `format`; the handler validates the URL (validateURL).
		OnProp("url", func(p *jsonschema.Schema) { p.Format = "uri" }),
	)
}
