package copilot

import (
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// cachedSession is one session entry. The JSON field names/order are the TS server's on-disk
// format — a user switching between the TS and Go servers shares the cache file, so keep them
// byte-compatible (the whole point is not burning support-account logins).
type cachedSession struct {
	Scope          string            `json:"scope"`
	ActiveClinicID *int64            `json:"activeClinicId"`
	Token          string            `json:"token"`
	Cookies        map[string]string `json:"cookies"`
	ExpiresAt      float64           `json:"expiresAt"` // ms since epoch, from the JWT
}

type diskCache struct {
	Version  int                      `json:"version"`
	Sessions map[string]cachedSession `json:"sessions"`
}

var (
	cacheMu sync.Mutex // guards mem and serializes disk read-merge-write within this process
	mem     = map[string]cachedSession{}
)

func fresh(e cachedSession) bool {
	return e.ExpiresAt-float64(time.Now().UnixMilli()) > float64(skew.Milliseconds())
}

// cachePath: COPILOT_MCP_CACHE, else sessions.json under config.CacheDir.
func cachePath() string {
	if p := os.Getenv("COPILOT_MCP_CACHE"); p != "" {
		return p
	}
	return filepath.Join(config.CacheDir(), "sessions.json")
}

// readDisk treats any read failure (missing, unparseable, wrong version) as an empty cache.
func readDisk() diskCache {
	empty := diskCache{Version: 1, Sessions: map[string]cachedSession{}}
	raw, err := os.ReadFile(cachePath())
	if err != nil {
		return empty
	}
	var d diskCache
	if json.Unmarshal(raw, &d) != nil || d.Version != 1 || d.Sessions == nil {
		return empty
	}
	return d
}

// writeDisk re-reads and merges before writing (two host processes share the file; a sibling's
// entry must not be clobbered), and drops expired entries. No torn file, no shared temp name: write
// to a unique temp file in the same dir (0600), then rename. Best-effort — a cache that can't be
// persisted must not fail a login that succeeded, so errors are logged, not returned.
func writeDisk(key string, entry cachedSession) {
	if !config.DiskCacheEnabled() {
		return
	}
	path := cachePath()
	sessions := map[string]cachedSession{}
	nowMs := float64(time.Now().UnixMilli())
	for k, v := range readDisk().Sessions {
		if v.ExpiresAt > nowMs {
			sessions[k] = v
		}
	}
	sessions[key] = entry
	data, err := shared.MarshalJSON(diskCache{Version: 1, Sessions: sessions})
	if err != nil {
		slog.Warn("session cache: encode failed", "err", err)
		return
	}
	if err := memo.WriteAtomic(filepath.Dir(path), filepath.Base(path), data); err != nil {
		slog.Warn("session cache: write failed", "path", path, "err", err)
	}
}

// cacheGet serves a fresh entry from memory, else from disk (promoting it to memory).
func cacheGet(key string) (cachedSession, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if m, ok := mem[key]; ok && fresh(m) {
		return cloneEntry(m), true
	}
	if d, ok := readDisk().Sessions[key]; ok && fresh(d) {
		mem[key] = d
		return cloneEntry(d), true
	}
	return cachedSession{}, false
}

func cachePutEntry(key string, e cachedSession) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	mem[key] = e
	writeDisk(key, e)
}

func cloneEntry(e cachedSession) cachedSession {
	e.Cookies = maps.Clone(e.Cookies)
	return e
}
