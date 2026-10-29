package apex

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/reference"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ---- queues, queue items ------------------------------------------------------

func (backend) ListQueues(ctx context.Context, args orchestrator.ListQueuesArgs) ([]orchestrator.QueueCore, error) {
	queues, err := listQueues(ctx, args.Env, args.Folder)
	if err != nil {
		return nil, err
	}
	out := []orchestrator.QueueCore{}
	for _, q := range queues {
		if len(out) < args.Top && containsFold(q.Name, args.NameContains) {
			out = append(out, toQueueCore(q))
		}
	}
	return out, nil
}

func resolveQueue(ctx context.Context, env config.Env, input string, kind orchestrator.QueueKind) (Queue, error) {
	queues, err := listQueues(ctx, env, "")
	if err != nil {
		return Queue{}, err
	}
	if hit := pickQueue(queues, input, kind); hit != nil {
		return *hit, nil
	}
	names := make([]string, len(queues))
	for i, q := range queues {
		names[i] = q.Name
	}
	return Queue{}, shared.Expectedf("no Apex %s queue matches '%s'. Queues in this folder: %s", kind, input, strings.Join(names, ", "))
}

func (backend) ListQueueItems(ctx context.Context, args orchestrator.ListQueueItemsArgs) (orchestrator.ListQueueItemsResult, error) {
	picked, err := resolveQueue(ctx, args.Env, args.Queue, args.QueueKind)
	if err != nil {
		return orchestrator.ListQueueItemsResult{}, err
	}
	var filters ItemFilters
	if args.Status != "" {
		filters.Status = toItemWireStatus(args.Status)
	}
	rows, err := listQueueItems(ctx, args.Env, "", picked.ID, filters, args.Top)
	if err != nil {
		return orchestrator.ListQueueItemsResult{}, err
	}
	items := make([]orchestrator.QueueItemCore, 0, min(len(rows), args.Top))
	for _, r := range rows[:min(len(rows), args.Top)] {
		items = append(items, toItemCore(r, nil, attemptsFromItem(r), false, picked.Name))
	}
	return orchestrator.ListQueueItemsResult{QueueID: picked.ID, QueueName: picked.Name, Items: items}, nil
}

type hit struct {
	queue Queue
	item  QueueItem
}

// foundItem: every attempt of an item is a job with the item's reference; filter by queue_item_id
// because references are not unique (enforce_unique_reference is off). The item's own latest job
// id is the fallback when the reference listing finds none.
func foundItem(ctx context.Context, env config.Env, folder string, h hit, includeContent bool) (orchestrator.QueueItemCore, error) {
	jobs, err := listJobs(ctx, env, folder, JobFilters{Reference: h.item.Reference}, attemptsPerItemMax)
	if err != nil {
		return orchestrator.QueueItemCore{}, err
	}
	attempts := []orchestrator.JobAttempt{}
	for _, j := range jobs {
		if j.QueueItemID != nil && *j.QueueItemID == h.item.ID {
			attempts = append(attempts, toJobAttempt(j))
		}
	}
	if len(attempts) == 0 {
		attempts = attemptsFromItem(h.item)
	}
	return toItemCore(h.item, nil, attempts, includeContent, h.queue.Name), nil
}

// searchAllQueues searches every queue (findConcurrency at a time) for the order's items.
func searchAllQueues(ctx context.Context, env config.Env, folder string, queues []Queue, needle string) (hits []hit, scanned int, err error) {
	found, err := shared.MapOrdered(ctx, queues, findConcurrency, func(ctx context.Context, _ int, q Queue) ([]QueueItem, error) {
		return listQueueItems(ctx, env, folder, q.ID, ItemFilters{Search: needle}, searchPerQueueMax)
	})
	if err != nil {
		return nil, 0, err
	}
	for i, rows := range found {
		scanned += len(rows)
		// `search` is fuzzy: only an exact specific_content.orderUid is the order's item.
		for _, item := range rows {
			if uid, _ := item.SpecificContent["orderUid"].(string); uid == needle {
				hits = append(hits, hit{queue: queues[i], item: item})
			}
		}
	}
	return hits, scanned, nil
}

func (backend) FindOrderQueueItems(ctx context.Context, args orchestrator.FindOrderArgs) (orchestrator.FindOrderResult, error) {
	needle := strings.TrimSpace(args.OrderUID)
	resolved, err := ResolveFolderID(ctx, args.Env, args.Folder)
	if err != nil {
		return orchestrator.FindOrderResult{}, err
	}
	queues, err := listQueuesForRead(ctx, args.Env, args.Folder) // every queue, including Unrouted and inactive ones
	if err != nil {
		return orchestrator.FindOrderResult{}, err
	}
	hits, scanned, err := searchAllQueues(ctx, args.Env, args.Folder, queues, needle)
	if err != nil {
		return orchestrator.FindOrderResult{}, err
	}
	windowed, note, err := withinSince(hits, func(h hit) string { return h.item.CreatedAt }, args.Since)
	if err != nil {
		return orchestrator.FindOrderResult{}, err
	}
	slices.SortStableFunc(windowed, func(a, b hit) int { return strings.Compare(b.item.CreatedAt, a.item.CreatedAt) })
	newest := windowed[:min(len(windowed), args.Top)]
	items, err := shared.MapOrdered(ctx, newest, findConcurrency, func(ctx context.Context, _ int, h hit) (orchestrator.QueueItemCore, error) {
		return foundItem(ctx, args.Env, args.Folder, h, args.IncludeSpecificContent)
	})
	if err != nil {
		return orchestrator.FindOrderResult{}, err
	}
	notes := []string{}
	if note != "" {
		notes = append(notes, note)
	}
	return orchestrator.FindOrderResult{Folder: resolved.Name, SearchMode: "search", Scanned: scanned, Items: items, Notes: notes}, nil
}

// queueNameOf is best-effort: a failure lands on queueNameError, never on the pull.
func queueNameOf(ctx context.Context, env config.Env, queueID string) (name, nameErr string) {
	q, err := getQueue(ctx, env, "", queueID)
	if err != nil {
		return "", err.Error()
	}
	return q.Name, ""
}

func (backend) PullQueueItem(ctx context.Context, args orchestrator.PullQueueItemArgs) (orchestrator.PullQueueItemResult, error) {
	if args.URL != "" || args.FolderID != "" {
		return orchestrator.PullQueueItemResult{}, shared.Expectedf("Apex pull_queue_item takes env + queueId + itemId only (url and folderId are UiPath-only)")
	}
	if args.Env == "" || args.QueueID == "" || args.ItemID == "" {
		return orchestrator.PullQueueItemResult{}, shared.Expectedf("Apex pull_queue_item needs env, queueId and itemId (from list_queue_items or find_order_queue_items)")
	}
	item, err := getQueueItem(ctx, args.Env, "", args.QueueID, args.ItemID)
	if err != nil {
		return orchestrator.PullQueueItemResult{}, err
	}
	queueName, queueNameErr := queueNameOf(ctx, args.Env, args.QueueID)
	// IsApproved is forced false so a pulled item can never submit a real auth when re-posted.
	guarded, err := orchestrator.GuardQueueItemSafety(item.SpecificContent, orchestrator.SafetyForce, orchestrator.QueueSafetyLimits{})
	if err != nil {
		return orchestrator.PullQueueItemResult{}, err
	}
	core := toItemCore(item.QueueItem, &item, attemptsFromItem(item.QueueItem), false, queueName)
	core.SpecificContent = guarded.SpecificContent
	return orchestrator.PullQueueItemResult{
		Env:               args.Env,
		Item:              core,
		Portal:            orchestrator.ToPortalRef(reference.ResolvePortal(queueName)),
		IsApprovedForced:  true,
		SuggestedFilename: orchestrator.MemberFilename(guarded.SpecificContent),
		QueueNameError:    queueNameErr,
	}, nil
}

// ---- processes, triggers ------------------------------------------------------

func (backend) ListProcesses(ctx context.Context, args orchestrator.ListProcessesArgs) ([]orchestrator.ProcessCore, error) {
	workflows, err := listWorkflows(ctx, args.Env, args.Folder)
	if err != nil {
		return nil, err
	}
	out := []orchestrator.ProcessCore{}
	for _, w := range workflows {
		if len(out) < args.Top && containsFold(w.Name, args.NameContains) {
			out = append(out, toProcessCore(w))
		}
	}
	return out, nil
}

// ListTriggers: queues stand in for queue triggers (Apex has no /triggers route); cron schedules
// are the time triggers and are not queue-scoped, so a queueId filter returns queue triggers only.
func (backend) ListTriggers(ctx context.Context, args orchestrator.ListTriggersArgs) ([]orchestrator.TriggerCore, error) {
	// Schedules are independent of the queue listing: fetch both at once.
	var (
		schedules []Schedule
		serr      error
		wg        sync.WaitGroup
	)
	if args.QueueID == "" {
		wg.Go(func() { schedules, serr = listSchedules(ctx, args.Env, args.Folder) })
	}
	queues, err := listQueues(ctx, args.Env, args.Folder)
	wg.Wait()
	if err := cmpErr(err, serr); err != nil {
		return nil, err
	}
	out := []orchestrator.TriggerCore{}
	for _, t := range toQueueTriggers(queues) {
		if args.QueueID == "" || (t.QueueID != nil && *t.QueueID == args.QueueID) {
			out = append(out, t)
		}
	}
	for _, s := range schedules {
		out = append(out, toScheduleTrigger(s))
	}
	return out[:min(len(out), args.Top)], nil
}
