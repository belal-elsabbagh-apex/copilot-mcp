package sweep

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/backends"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// crossCheckPool bounds concurrent per-order orchestrator lookups.
const crossCheckPool = 4

// The order's own BE creationDate is a hard floor for its queue item's CreationTime —
// UiPath cannot queue an order before Copilot creates it. Narrowing the per-order search
// to (creationDate - margin) is therefore a free, correctness-preserving tightening (it
// can never cause a missed match, unlike ageHours()'s clinical dates, which can sit
// arbitrarily far from when the order was actually queued) — it turns an otherwise
// unbounded contains(SpecificData, uid) scan into one bounded to the relevant window,
// without an extra Orchestrator round trip (creationDate is already in the page scan).
// An explicit caller `since` is a floor too, so the tighter (later) of the two wins —
// this never loosens a bound the caller deliberately set.
const creationToQueueMargin = 24 * time.Hour // clock skew / BE -> queue processing lag

// queueSearchSince is the ISO `since` for one order's queue search; "" = unbounded.
func queueSearchSince(callerSince, orderCreationDate string) string {
	var floor time.Time
	if t, ok := parseDate(callerSince); ok {
		floor = t
	}
	if t, ok := parseDate(orderCreationDate); ok {
		if t = t.Add(-creationToQueueMargin); t.After(floor) {
			floor = t
		}
	}
	if floor.IsZero() {
		return ""
	}
	return floor.UTC().Format("2006-01-02T15:04:05.000Z")
}

// Summarize an order's queue item(s) into a coarse verdict. The queue item is the
// source of truth for what happened to the order: its status directly determines
// faulted (Failed) / running (InProgress) / stuck-but-succeeded (Successful), and a
// New item with no job attempt means "queued, not yet picked up". Items arrive newest
// first (createdAt desc), so [0] is the latest item and its jobs[0] the latest attempt.
// Priority: never-started > faulted > running > queued-not-picked-up > successful-order-stuck.
// Only the latest attempt of the latest item decides never-started: an earlier reaped
// attempt that a retry replaced is history, not the order's current state.
var verdictRules = []struct {
	verdict string
	matches func(items []orchestrator.QueueItemCore) bool
}{
	{"job-never-started", func(items []orchestrator.QueueItemCore) bool {
		jobs := items[0].Jobs
		return len(jobs) > 0 && jobs[0].NeverStarted != nil && *jobs[0].NeverStarted
	}},
	{"job-faulted", anyItem(func(q orchestrator.QueueItemCore) bool { return q.Status == "Failed" })},
	{"job-running", anyItem(func(q orchestrator.QueueItemCore) bool { return q.Status == "InProgress" })},
	{"queued-not-picked-up", anyItem(func(q orchestrator.QueueItemCore) bool { return q.Status == "New" && len(q.Jobs) == 0 })},
	{"job-successful-order-stuck", func(items []orchestrator.QueueItemCore) bool {
		return !slices.ContainsFunc(items, func(q orchestrator.QueueItemCore) bool { return q.Status != "Successful" })
	}},
}

func anyItem(f func(orchestrator.QueueItemCore) bool) func([]orchestrator.QueueItemCore) bool {
	return func(items []orchestrator.QueueItemCore) bool { return slices.ContainsFunc(items, f) }
}

func orderVerdict(items []orchestrator.QueueItemCore) OrderVerdict {
	if len(items) == 0 {
		return OrderVerdict{Verdict: "no-job"}
	}
	newest := items[0]
	v := OrderVerdict{Backend: &newest.Orchestrator, Verdict: "job-found", QueueItemCount: len(items), QueueItemStatus: newest.Status}
	for _, r := range verdictRules {
		if r.matches(items) {
			v.Verdict = r.verdict
			break
		}
	}
	if i := slices.IndexFunc(items, func(q orchestrator.QueueItemCore) bool { return q.ExceptionType != nil && *q.ExceptionType != "" }); i >= 0 {
		v.ExceptionType = *items[i].ExceptionType
	}
	return v
}

// itemsOrError: FindOrderAcross isolates one orchestrator's failure into SearchErrors. For the
// sweep an order with no items AND a failed search is unknown, not "no-job": return an error so
// CrossCheckOrders records it as that order's error verdict. With items found, the verdict
// stands on them.
func itemsOrError(found backends.AcrossResult) ([]orchestrator.QueueItemCore, error) {
	if found.Count > 0 || len(found.SearchErrors) == 0 {
		return found.Items, nil
	}
	var parts []string
	for _, name := range found.Searched {
		if msg, ok := found.SearchErrors[name]; ok {
			parts = append(parts, fmt.Sprintf("%s: %s", name, msg))
		}
	}
	return nil, fmt.Errorf("%s", strings.Join(parts, "; "))
}

// CrossCheckOrders sets each order's Orchestrator verdict from correlate (items newest first),
// four orders at a time. A real per-order failure lands on that order's verdict, never lost and
// never blocking the rest. correlate is injected so this is testable without HTTP.
func CrossCheckOrders(ctx context.Context, stuck []StuckOrder, correlate func(ctx context.Context, orderUID string) ([]orchestrator.QueueItemCore, error), onProgress shared.StepProgress) {
	var candidates []int
	for i := range stuck {
		if uid, _ := stuck[i].OrderUID.V.(string); uid != "" {
			candidates = append(candidates, i)
		}
	}
	var (
		mu      sync.Mutex // serializes progress callbacks and the checked counter
		checked int
	)
	shared.MapIsolated(ctx, candidates, crossCheckPool, func(ctx context.Context, i int) struct{} {
		uid, _ := stuck[i].OrderUID.V.(string)
		items, err := correlate(ctx, uid)
		v := orderVerdict(items)
		if err != nil {
			v = OrderVerdict{Verdict: "error: " + err.Error()}
		}
		stuck[i].Orchestrator = &v // each call owns its own element

		mu.Lock()
		defer mu.Unlock()
		checked++
		onProgress.Step(checked, len(candidates), fmt.Sprintf("cross-checked %d order(s) against orchestrators", checked))
		return struct{}{}
	})
}
