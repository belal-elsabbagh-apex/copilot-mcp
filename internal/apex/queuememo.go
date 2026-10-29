package apex

import (
	"context"
	"sync"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

// A sweep (find_stuck_orders) cross-checks N orders, and every FindOrderQueueItems lists the same
// folder's queues. WithQueueListMemo scopes one listQueues result per (env, folder) to a context,
// so N orders cost one listing. The memo lives and dies with the ctx — no TTL, no global state —
// and only the read side (FindOrderQueueItems) consults it: write guards (resolveQueue, the
// delete membership proof) call listQueues directly and are never served from it.

type queueMemoKey struct {
	env    config.Env
	folder string
}

type queueMemoEntry struct {
	mu     sync.Mutex // held across the fetch: concurrent callers share one listing
	queues []Queue
	ok     bool // errors are never memoized: the next caller retries
}

type queueListMemo struct {
	mu      sync.Mutex
	entries map[queueMemoKey]*queueMemoEntry
}

type queueMemoCtxKey struct{}

// WithQueueListMemo returns a ctx under which FindOrderQueueItems reuses one queue listing per
// (env, folder). Callers must not mutate the returned queues.
func WithQueueListMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, queueMemoCtxKey{}, &queueListMemo{entries: map[queueMemoKey]*queueMemoEntry{}})
}

// listQueuesForRead is listQueues for the read side: memoized when ctx carries a memo.
func listQueuesForRead(ctx context.Context, env config.Env, folder string) ([]Queue, error) {
	m, ok := ctx.Value(queueMemoCtxKey{}).(*queueListMemo)
	if !ok {
		return listQueues(ctx, env, folder)
	}
	k := queueMemoKey{env, folder}
	m.mu.Lock()
	e := m.entries[k]
	if e == nil {
		e = &queueMemoEntry{}
		m.entries[k] = e
	}
	m.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ok {
		return e.queues, nil
	}
	queues, err := listQueues(ctx, env, folder)
	if err != nil {
		return nil, err
	}
	e.queues, e.ok = queues, true
	return queues, nil
}
