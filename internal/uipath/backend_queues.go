package uipath

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/reference"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ---- queues ----------------------------------------------------------------

func (backend) ListQueues(ctx context.Context, args orchestrator.ListQueuesArgs) ([]orchestrator.QueueCore, error) {
	scope, err := scopeFor(args.Env, args.Folder)
	if err != nil {
		return nil, err
	}
	defs, err := listQueueDefinitions(ctx, scope, args.NameContains, args.Top)
	if err != nil {
		return nil, err
	}
	return mapSlice(defs, toQueueCore), nil
}

func mapSlice[T, R any](in []T, f func(T) R) []R {
	out := make([]R, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

var digitsRE = regexp.MustCompile(`^\d+$`)

// resolveQueue: a numeric id is a QueueDefinitionId as-is; anything else is a portal key/alias
// whose submit/sync id comes from the static registry (prod ids only — pass the numeric id for
// dev-clone queues). name is the portal key when the registry knows the queue, else "".
func resolveQueue(queue string, kind orchestrator.QueueKind) (id int64, name string, err error) {
	if digitsRE.MatchString(queue) {
		id, err = strconv.ParseInt(queue, 10, 64)
		if err != nil {
			return 0, "", shared.Expectedf("invalid queue id '%s'", queue)
		}
		return id, reference.ResolvePortalByDefID(int(id)).Key, nil
	}
	portal := reference.ResolvePortal(queue)
	if !portal.Matched {
		known := make([]string, len(reference.Portals))
		for i, p := range reference.Portals {
			known[i] = p.Key
		}
		return 0, "", shared.Expectedf("unknown queue '%s'. Pass a numeric queue id or one of: %s", queue, strings.Join(known, ", "))
	}
	defID := portal.SubmitQueueDefID
	if kind == orchestrator.QueueSync {
		defID = portal.SyncQueueDefID
	}
	if defID == 0 {
		return 0, "", shared.Expectedf("portal '%s' has no known %s queue id — pass the numeric queue id", portal.Key, kind)
	}
	return int64(defID), portal.Key, nil
}

// ListQueueItems browses a queue, newest first (PHI-light: no specificContent in the projection).
func (backend) ListQueueItems(ctx context.Context, args orchestrator.ListQueueItemsArgs) (orchestrator.ListQueueItemsResult, error) {
	id, name, err := resolveQueue(args.Queue, args.QueueKind)
	if err != nil {
		return orchestrator.ListQueueItemsResult{}, err
	}
	scope, err := scopeFor(args.Env, "")
	if err != nil {
		return orchestrator.ListQueueItemsResult{}, err
	}
	rows, err := listQueueItems(ctx, id, scope, string(args.Status), args.Top)
	if err != nil {
		return orchestrator.ListQueueItemsResult{}, err
	}
	if name == "" {
		first := ""
		if len(rows) > 0 {
			first = rows[0].Name
		}
		name = reference.NormalizeQueueName(first)
	}
	return orchestrator.ListQueueItemsResult{
		QueueID:   fmt.Sprint(id),
		QueueName: name,
		Items:     mapSlice(rows, func(r QueueItem) orchestrator.QueueItemCore { return toQueueItemCoreFromItem(r, r.Name, nil) }),
	}, nil
}

// FindOrderQueueItems resolves orderUid -> the UiPath queue item(s) it flowed through. When a
// `since`-bounded search finds nothing, it retries once without the bound (the item may predate it).
func (backend) FindOrderQueueItems(ctx context.Context, args orchestrator.FindOrderArgs) (orchestrator.FindOrderResult, error) {
	scope, err := scopeFor(args.Env, args.Folder)
	if err != nil {
		return orchestrator.FindOrderResult{}, err
	}
	q, err := searchQueueItemsByOrderID(ctx, args.OrderUID, scope, args.Top, args.Since)
	if err != nil {
		return orchestrator.FindOrderResult{}, err
	}
	notes := slices.Clone(q.Notes)
	if args.Since != "" && len(q.Matches) == 0 {
		if q, err = searchQueueItemsByOrderID(ctx, args.OrderUID, scope, args.Top, ""); err != nil {
			return orchestrator.FindOrderResult{}, err
		}
		notes = append(notes, q.Notes...)
		notes = append(notes, fmt.Sprintf("no queue item matched within since=%s; retried without the time bound.", args.Since))
	}
	items := mapSlice(q.Matches, func(m QueueItemMatch) orchestrator.QueueItemCore {
		core := toQueueItemCoreFromMatch(m, reference.ResolvePortalByDefID(int(m.QueueDefinitionID)).Key)
		if !args.IncludeSpecificContent {
			core.SpecificContent = nil
		}
		return core
	})
	return orchestrator.FindOrderResult{
		Folder: scope.FolderPath, SearchMode: q.SearchMode, Scanned: q.Scanned,
		Items: items, Notes: notes, SearchError: q.SearchError,
	}, nil
}

type pullTarget struct {
	itemID int64
	env    config.Env
	scope  FolderScope
}

// resolvePull picks where to pull from: an Orchestrator URL (fid + item id parsed out; env derived
// from the fid) or an explicit numeric itemId scoped by env (folderId omitted = the env default).
func resolvePull(args orchestrator.PullQueueItemArgs) (pullTarget, error) {
	if args.URL != "" {
		parsed, err := parseQueueItemURL(args.URL)
		if err != nil {
			return pullTarget{}, err
		}
		return pullTarget{
			itemID: parsed.txnID,
			env:    reference.EnvForFolderID(parsed.folderID),
			// Query by the URL's actual fid (correct even for folders we don't map).
			scope: FolderScope{OrgUnitID: parsed.folderID, FolderPath: reference.FolderNameForID(parsed.folderID)},
		}, nil
	}
	if args.Env == "" {
		return pullTarget{}, shared.Expectedf("env is required when pulling by itemId (no url to derive it from)")
	}
	itemID, err := strconv.ParseInt(args.ItemID, 10, 64)
	if err != nil || itemID <= 0 {
		return pullTarget{}, shared.Expectedf("UiPath pull_queue_item needs a url or a positive numeric itemId")
	}
	if args.FolderID != "" {
		return pullTarget{itemID, args.Env, FolderScope{OrgUnitID: args.FolderID, FolderPath: reference.FolderNameForID(args.FolderID)}}, nil
	}
	scope, err := scopeFor(args.Env, "")
	return pullTarget{itemID, args.Env, scope}, err
}

// PullQueueItem fetches one queue item as a ready-to-run test payload.
// CRITICAL: IsApproved is forced false so a test run can never submit a real auth.
func (backend) PullQueueItem(ctx context.Context, args orchestrator.PullQueueItemArgs) (orchestrator.PullQueueItemResult, error) {
	target, err := resolvePull(args)
	if err != nil {
		return orchestrator.PullQueueItemResult{}, err
	}
	item, err := GetQueueItem(ctx, target.itemID, target.scope)
	if err != nil {
		return orchestrator.PullQueueItemResult{}, err
	}
	queueName := reference.NormalizeQueueName(item.Name)
	// Secondary "try harder" lookup — best-effort: a real failure here shouldn't fail the whole
	// pull when item.Name is already a usable fallback, but it must still surface.
	queueNameError := ""
	if (queueName == "" || !reference.ResolvePortal(queueName).Matched) && item.QueueDefinitionID != 0 {
		resolved, err := getQueueDefinitionName(ctx, item.QueueDefinitionID, target.scope)
		switch {
		case err != nil:
			queueNameError = err.Error()
		case resolved != "":
			queueName = reference.NormalizeQueueName(resolved)
		}
	}
	guarded, err := orchestrator.GuardQueueItemSafety(item.SpecificContent, orchestrator.SafetyForce, orchestrator.QueueSafetyLimits{})
	if err != nil {
		return orchestrator.PullQueueItemResult{}, err
	}
	sc := guarded.SpecificContent
	return orchestrator.PullQueueItemResult{
		Env:               target.env,
		Item:              toQueueItemCoreFromItem(item, queueName, sc),
		Portal:            orchestrator.ToPortalRef(reference.ResolvePortal(queueName)),
		IsApprovedForced:  true,
		SuggestedFilename: orchestrator.MemberFilename(sc),
		QueueNameError:    queueNameError,
	}, nil
}

// ---- processes / triggers --------------------------------------------------

func (backend) ListProcesses(ctx context.Context, args orchestrator.ListProcessesArgs) ([]orchestrator.ProcessCore, error) {
	scope, err := scopeFor(args.Env, args.Folder)
	if err != nil {
		return nil, err
	}
	releases, err := listReleases(ctx, scope, args.NameContains, args.Top)
	if err != nil {
		return nil, err
	}
	return mapSlice(releases, toProcessCore), nil
}

// ListTriggers: ProcessSchedules has no server-side queue filter — `top` bounds the fetch, then
// the queueId filter applies to what came back.
func (backend) ListTriggers(ctx context.Context, args orchestrator.ListTriggersArgs) ([]orchestrator.TriggerCore, error) {
	scope, err := scopeFor(args.Env, args.Folder)
	if err != nil {
		return nil, err
	}
	triggers, err := listTriggers(ctx, scope, args.Top)
	if err != nil {
		return nil, err
	}
	cores := mapSlice(triggers, toTriggerCore)
	if args.QueueID == "" {
		return cores, nil
	}
	return slices.DeleteFunc(cores, func(t orchestrator.TriggerCore) bool { return t.QueueID == nil || *t.QueueID != args.QueueID }), nil
}
