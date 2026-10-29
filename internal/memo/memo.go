// Package memo is a small byte-bounded LRU with single-flight on the miss path and an optional
// persistent disk tier (disk.go). It backs the terminal-job read caches of internal/uipath and
// internal/apex. Entries hold job logs (PHI), so the disk tier is 0600/0700, hashed names, and off
// when copilot.sessionCache is false. Flush drops memory only.
//
// The cache never decides what is safe to store: Fetch stores a value only when the fetch itself
// says so (its `store` result), and Peek only returns what an earlier Fetch stored. Callers own
// the "terminal and settled" verdict and must derive it from a live read in the same call.
package memo

import (
	"container/list"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// Default bounds. ponytail: unmeasured — 64 MiB total / 8 MiB per entry is sized for a few dozen
// 5 MiB job logs; measure real job.log / RobotLogs sizes before tuning. The 24h max age only
// bounds how long data later redacted or purged upstream can linger.
const (
	DefaultTotalBytes = 64 << 20
	DefaultEntryBytes = 8 << 20
	DefaultMaxAge     = 24 * time.Hour
)

type entry struct {
	key    string
	val    any
	size   int
	stored time.Time
}

type flight struct {
	done chan struct{}
	val  any
	err  error
}

// Cache is safe for concurrent use.
type Cache struct {
	maxTotal, maxEntry int
	maxAge             time.Duration
	now                func() time.Time
	disk               *diskTier // nil = memory only

	mu      sync.Mutex
	gen     int // bumped by Flush so a fetch that started before it never stores
	bytes   int
	lru     *list.List // front = most recently used; values are *entry
	items   map[string]*list.Element
	flights map[string]*flight
}

// New returns a memory-only cache bounded to totalBytes, with at most entryBytes per entry and
// entries expiring after maxAge.
func New(totalBytes, entryBytes int, maxAge time.Duration) *Cache {
	return &Cache{
		maxTotal: totalBytes, maxEntry: entryBytes, maxAge: maxAge, now: time.Now,
		lru: list.New(), items: map[string]*list.Element{}, flights: map[string]*flight{},
	}
}

// NewPersistent is New with the disk tier under <cache dir>/cache/<namespace>/. Stored values
// must round-trip through encoding/json.
func NewPersistent(namespace string, totalBytes, entryBytes int, maxAge time.Duration) *Cache {
	c := New(totalBytes, entryBytes, maxAge)
	c.disk = &diskTier{ns: namespace, maxBytes: DiskMaxBytes, maxAge: DiskMaxAge}
	return c
}

// Flush drops every entry (config reload, tests). Fetches already in flight still return their
// value to their callers but do not store it.
func (c *Cache) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.bytes = 0
	c.lru.Init()
	clear(c.items)
}

// Len is the number of stored entries.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

// Peek returns the stored value for key: memory first (refreshing its recency; an expired entry
// is dropped), then disk (a hit repopulates memory). A Refresh ctx always misses.
func Peek[T any](ctx context.Context, c *Cache, key string) (T, bool) {
	var zero T
	if Refresh(ctx) {
		c.debug(key, "refresh-bypass")
		return zero, false
	}
	if v, ok := peekMemory[T](c, key); ok {
		c.debug(key, "hit-memory")
		return v, true
	}
	if c.disk == nil {
		return zero, false
	}
	raw, size, ok := c.disk.read(key)
	if !ok {
		return zero, false
	}
	var v T
	if json.Unmarshal(raw, &v) != nil {
		c.disk.remove(key)
		return zero, false
	}
	c.mu.Lock()
	c.add(key, v, size)
	c.mu.Unlock()
	c.debug(key, "hit-disk")
	return v, true
}

func peekMemory[T any](c *Cache, key string) (T, bool) {
	var zero T
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return zero, false
	}
	e := el.Value.(*entry) //nolint:forcetypeassert // the list only holds *entry
	if c.now().Sub(e.stored) > c.maxAge {
		c.remove(el)
		return zero, false
	}
	v, ok := e.val.(T)
	if !ok {
		return zero, false
	}
	c.lru.MoveToFront(el)
	return v, true
}

// debug logs a cache event for a named (persistent) cache; the key is hashed, never printed.
func (c *Cache) debug(key, event string) {
	if c.disk != nil {
		slog.Debug("cache", "namespace", c.disk.ns, "key", hashName(key)[:12], "event", event)
	}
}

// Fetch runs fn once per key at a time: concurrent callers with the same key wait for the leader
// and share its value. fn returns the value, its cost in bytes, and whether it may be stored
// (memory, and disk for a persistent cache). An error is never stored; a waiter whose leader
// failed retries as a leader itself (the leader's ctx may be the one that was cancelled). A value
// larger than the per-entry cap is returned but not stored. Fetch never READS the cache — callers
// Peek first, gated on their own settled verdict — so a stored result replaces any older entry.
func Fetch[T any](ctx context.Context, c *Cache, key string, fn func(ctx context.Context) (val T, size int, store bool, err error)) (T, error) {
	var zero T
	for {
		c.mu.Lock()
		if f, ok := c.flights[key]; ok {
			c.mu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return zero, ctx.Err()
			}
			if v, ok := f.val.(T); ok && f.err == nil {
				return v, nil
			}
			continue
		}
		f := &flight{done: make(chan struct{})}
		c.flights[key] = f
		gen := c.gen
		c.mu.Unlock()

		c.debug(key, "fetch")
		val, size, store, err := fn(ctx)

		c.mu.Lock()
		delete(c.flights, key)
		persist := false
		if err == nil {
			f.val = val
			if store && gen == c.gen {
				c.add(key, val, size)
				persist = size <= c.maxEntry && size <= c.maxTotal
			}
		}
		f.err = err
		c.mu.Unlock()
		close(f.done)
		if persist && c.disk != nil {
			c.disk.write(key, val, size)
			c.debug(key, "stored")
		}
		return val, err
	}
}

// add stores under c.mu, evicting least-recently-used entries until the total fits.
func (c *Cache) add(key string, val any, size int) {
	if size > c.maxEntry || size > c.maxTotal {
		return
	}
	if el, ok := c.items[key]; ok {
		c.remove(el)
	}
	c.items[key] = c.lru.PushFront(&entry{key: key, val: val, size: size, stored: c.now()})
	c.bytes += size
	for c.bytes > c.maxTotal {
		c.remove(c.lru.Back())
	}
}

func (c *Cache) remove(el *list.Element) {
	e := c.lru.Remove(el).(*entry) //nolint:forcetypeassert // the list only holds *entry
	delete(c.items, e.key)
	c.bytes -= e.size
}
