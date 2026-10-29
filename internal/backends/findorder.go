package backends

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// find_order_queue_items across orchestrators: ask every selected backend for the order's
// queue items in parallel, merge them newest first, and keep each backend's failure
// isolated. Routing is decided per portal outside this repo, so where the item actually
// sits is the truth. A backend's own `since` retry (unbounded when nothing matches) is
// that backend's business and is not repeated here.

// BackendInfo is what an answering backend reported about its search.
type BackendInfo struct {
	Folder     string `json:"folder"`
	SearchMode string `json:"searchMode"`
	Scanned    int    `json:"scanned"`
}

// AcrossResult is find_order_queue_items' result. Notes/SearchErrors are omitted when empty.
type AcrossResult struct {
	Count        int                                       `json:"count"`
	JobIDs       map[orchestrator.Orchestrator][]string    `json:"jobIds"` // per answering backend, newest first, deduped
	Items        []orchestrator.QueueItemCore              `json:"items"`  // merged, newest first by createdAt
	Searched     []orchestrator.Orchestrator               `json:"searched"`
	Backends     map[orchestrator.Orchestrator]BackendInfo `json:"backends"`
	Notes        []string                                  `json:"notes,omitempty"` // each prefixed "<backend>: "
	SearchErrors map[orchestrator.Orchestrator]string      `json:"searchErrors,omitempty"`
}

// SelectBackends resolves which backends to ask. `folder` names one orchestrator's folder, so it
// only makes sense next to that orchestrator.
func SelectBackends(o orchestrator.Orchestrator, folder string) ([]orchestrator.Backend, error) {
	if folder != "" && o == "" {
		return nil, shared.Expectedf("`folder` is a per-orchestrator override: pass `orchestrator` together with it, " +
			"or omit `folder` to search every orchestrator's default folder for the env.")
	}
	if o == "" {
		return SearchableBackends(), nil
	}
	b := BackendFor(o)
	if b == nil {
		return nil, shared.Expectedf("unknown orchestrator %q", string(o))
	}
	return []orchestrator.Backend{b}, nil
}

// createdMs: an unreadable timestamp just sorts oldest.
func createdMs(it orchestrator.QueueItemCore) int64 {
	t, ok := shared.ParseTime(it.CreatedAt)
	if !ok {
		return 0
	}
	return t.UnixMilli()
}

// jobIDsOf: items arrive newest first and each item's attempts newest first, so first-seen
// order is newest first; the seen-set drops the repeats a retried transaction produces.
func jobIDsOf(items []orchestrator.QueueItemCore) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, it := range items {
		for _, j := range it.Jobs {
			if !seen[j.ID] {
				seen[j.ID] = true
				ids = append(ids, j.ID)
			}
		}
	}
	return ids
}

// FindOrderAcross asks every backend concurrently; a backend's error or its own SearchError lands
// in SearchErrors without affecting the others (Promise.allSettled semantics).
func FindOrderAcross(ctx context.Context, args orchestrator.FindOrderArgs, bs []orchestrator.Backend) AcrossResult {
	answers := make([]orchestrator.FindOrderResult, len(bs))
	errs := make([]error, len(bs))
	var wg sync.WaitGroup
	for i, b := range bs {
		wg.Go(func() { answers[i], errs[i] = b.FindOrderQueueItems(ctx, args) })
	}
	wg.Wait()

	res := AcrossResult{
		JobIDs:   map[orchestrator.Orchestrator][]string{},
		Items:    []orchestrator.QueueItemCore{},
		Searched: make([]orchestrator.Orchestrator, len(bs)),
		Backends: map[orchestrator.Orchestrator]BackendInfo{},
	}
	for i, b := range bs {
		name := b.Name()
		res.Searched[i] = name
		if errs[i] != nil {
			res.addSearchError(name, errs[i].Error())
			continue
		}
		a := answers[i]
		if a.SearchError != "" {
			res.addSearchError(name, a.SearchError)
		}
		res.Items = append(res.Items, a.Items...)
		for _, n := range a.Notes {
			res.Notes = append(res.Notes, string(name)+": "+n)
		}
		res.Backends[name] = BackendInfo{Folder: a.Folder, SearchMode: a.SearchMode, Scanned: a.Scanned}
	}
	slices.SortStableFunc(res.Items, func(a, b orchestrator.QueueItemCore) int { return cmp.Compare(createdMs(b), createdMs(a)) })
	for name := range res.Backends {
		res.JobIDs[name] = jobIDsOf(slices.DeleteFunc(slices.Clone(res.Items), func(it orchestrator.QueueItemCore) bool { return it.Orchestrator != name }))
	}
	res.Count = len(res.Items)
	return res
}

func (r *AcrossResult) addSearchError(name orchestrator.Orchestrator, msg string) {
	if r.SearchErrors == nil {
		r.SearchErrors = map[orchestrator.Orchestrator]string{}
	}
	r.SearchErrors[name] = msg
}
