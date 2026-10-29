package memo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

type doc struct {
	Name string
	N    int
}

// diskEnv points the cache dir at a temp dir and loads a config whose sessionCache is `on`.
func diskEnv(t *testing.T, on bool) string {
	t.Helper()
	root := t.TempDir()
	creds := map[string]any{"be": "https://be.example.com", "email": "a@example.com", "password": "pw"}
	b, err := json.Marshal(map[string]any{"copilot": map[string]any{"sessionCache": on, "prod": creds, "pre_prod": creds},
		"uipath": map[string]any{"orchestratorUrl": "https://cloud.uipath.com/o/t/orchestrator_", "bearer": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfg, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_MCP_CONFIG", cfg)
	t.Setenv("COPILOT_MCP_CACHE", filepath.Join(root, "cm", "sessions.json"))
	config.ResetForTest()
	t.Cleanup(config.ResetForTest)
	return filepath.Join(root, "cm")
}

func persistent(ns string) *Cache { return NewPersistent(ns, 1<<20, 1<<20, time.Hour) }

func counting(n *atomic.Int32, v doc, store bool) func(context.Context) (doc, int, bool, error) {
	return func(context.Context) (doc, int, bool, error) {
		n.Add(1)
		return v, 100, store, nil
	}
}

func entryFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	return m
}

func TestAFreshInstanceOverTheSameDirIsServedFromDisk(t *testing.T) {
	root := diskEnv(t, true)
	var calls atomic.Int32
	first := persistent("ns")
	if _, err := Fetch(t.Context(), first, "job-1", counting(&calls, doc{"a", 1}, true)); err != nil {
		t.Fatal(err)
	}
	second := persistent("ns") // what a new process looks like
	got, ok := Peek[doc](t.Context(), second, "job-1")
	if !ok || got != (doc{"a", 1}) {
		t.Fatalf("second instance = %+v, %v; want a disk hit", got, ok)
	}
	if calls.Load() != 1 || second.Len() != 1 {
		t.Errorf("fetches = %d, memory len = %d; want 1 and the hit promoted to memory", calls.Load(), second.Len())
	}
	if n := len(entryFiles(t, filepath.Join(root, "cache", "ns"))); n != 1 {
		t.Errorf("entry files = %d, want 1", n)
	}
}

func TestEntryFilesAre0600InA0700DirNamedByHashOnly(t *testing.T) {
	root := diskEnv(t, true)
	_, _ = Fetch(t.Context(), persistent("ns"), "job-SECRET-123", counting(new(atomic.Int32), doc{"a", 1}, true))
	dir := filepath.Join(root, "cache", "ns")
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, %v; want 0700", st.Mode().Perm(), err)
	}
	files := entryFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	if fi, _ := os.Stat(files[0]); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", fi.Mode().Perm())
	}
	if base := filepath.Base(files[0]); strings.Contains(base, "SECRET") || len(base) != 64+len(".json") {
		t.Errorf("filename %q is not a bare sha256", base)
	}
}

func TestNeverPersistsUnstoredErroredOrOversizedResults(t *testing.T) {
	root := diskEnv(t, true)
	c := NewPersistent("ns", 1<<20, 50, time.Hour)
	_, _ = Fetch(t.Context(), c, "declined", counting(new(atomic.Int32), doc{"a", 1}, false))
	_, _ = Fetch(t.Context(), c, "errored", func(context.Context) (doc, int, bool, error) {
		return doc{"partial", 1}, 1, true, errors.New("boom")
	})
	_, _ = Fetch(t.Context(), c, "big", counting(new(atomic.Int32), doc{"a", 1}, true)) // size 100 > entry cap 50
	if n := len(entryFiles(t, filepath.Join(root, "cache", "ns"))); n != 0 {
		t.Errorf("%d files persisted, want none", n)
	}
}

func TestACorruptFileReadsAsAMissAndIsRemoved(t *testing.T) {
	root := diskEnv(t, true)
	c := persistent("ns")
	_, _ = Fetch(t.Context(), c, "k", counting(new(atomic.Int32), doc{"a", 1}, true))
	path := entryFiles(t, filepath.Join(root, "cache", "ns"))[0]
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Peek[doc](t.Context(), persistent("ns"), "k"); ok {
		t.Fatal("corrupt file served")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("corrupt file still present: %v", err)
	}
}

func TestDiskCapEvictsTheOldestEntries(t *testing.T) {
	root := diskEnv(t, true)
	c := persistent("ns")
	_, _ = Fetch(t.Context(), c, "k0", counting(new(atomic.Int32), doc{"zero", 0}, true))
	dir := filepath.Join(root, "cache", "ns")
	one, _ := os.Stat(entryFiles(t, dir)[0])
	c.disk.maxBytes = one.Size()*2 + one.Size()/2 // room for two entries
	age := func(h time.Duration) {                // age every just-written file, so "oldest" is unambiguous
		for _, f := range entryFiles(t, dir) {
			if st, _ := os.Stat(f); time.Since(st.ModTime()) < time.Minute {
				old := time.Now().Add(-h * time.Hour)
				_ = os.Chtimes(f, old, old)
			}
		}
	}
	age(3) // k0
	_, _ = Fetch(t.Context(), c, "k1", counting(new(atomic.Int32), doc{"x", 1}, true))
	age(2)
	_, _ = Fetch(t.Context(), c, "k2", counting(new(atomic.Int32), doc{"x", 2}, true))
	fresh := persistent("ns")
	if _, ok := Peek[doc](t.Context(), fresh, "k0"); ok {
		t.Error("the oldest entry survived the cap")
	}
	if _, ok := Peek[doc](t.Context(), fresh, "k2"); !ok {
		t.Error("the newest entry was evicted")
	}
}

func TestDiskEntriesOlderThanTheMaxAgeAreAMiss(t *testing.T) {
	diskEnv(t, true)
	c := persistent("ns")
	_, _ = Fetch(t.Context(), c, "k", counting(new(atomic.Int32), doc{"a", 1}, true))
	old := persistent("ns")
	old.disk.maxAge = -time.Second
	if _, ok := Peek[doc](t.Context(), old, "k"); ok {
		t.Fatal("expired disk entry served")
	}
}

func TestRefreshSkipsReadsButStillReplacesTheStoredEntry(t *testing.T) {
	diskEnv(t, true)
	var calls atomic.Int32
	c := persistent("ns")
	_, _ = Fetch(t.Context(), c, "k", counting(&calls, doc{"old", 1}, true))
	ctx := WithRefresh(t.Context())
	if _, ok := Peek[doc](ctx, c, "k"); ok {
		t.Fatal("refresh read the cache")
	}
	got, _ := Fetch(ctx, c, "k", counting(&calls, doc{"new", 2}, true))
	if calls.Load() != 2 || got.Name != "new" {
		t.Fatalf("calls = %d, got %+v; want exactly one extra fetch", calls.Load(), got)
	}
	again, ok := Peek[doc](t.Context(), persistent("ns"), "k")
	if !ok || again.Name != "new" {
		t.Errorf("disk entry after refresh = %+v, %v; want it overwritten", again, ok)
	}
}

func TestSessionCacheOffWritesNothingToDisk(t *testing.T) {
	root := diskEnv(t, false)
	_, _ = Fetch(t.Context(), persistent("ns"), "k", counting(new(atomic.Int32), doc{"a", 1}, true))
	SaveToken("tok", "secret", time.Now().Add(time.Hour))
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cache dir created with sessionCache:false: %v", err)
	}
}

func TestTokensRoundTripWithModesAndAreDroppedOnRequest(t *testing.T) {
	root := diskEnv(t, true)
	SaveToken("apex|https://x|me", "jwt", time.Now().Add(time.Hour))
	got, exp, ok := LoadToken("apex|https://x|me")
	if !ok || got != "jwt" || time.Until(exp) <= 0 {
		t.Fatalf("load = %q, %v, %v", got, exp, ok)
	}
	files, _ := filepath.Glob(filepath.Join(root, "tokens", "*.json"))
	if len(files) != 1 || strings.Contains(files[0], "apex") {
		t.Fatalf("token files = %v", files)
	}
	if st, _ := os.Stat(files[0]); st.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(files[0])); st.Mode().Perm() != 0o700 {
		t.Errorf("token dir mode = %v", st.Mode().Perm())
	}
	DropToken("apex|https://x|me")
	if _, _, ok := LoadToken("apex|https://x|me"); ok {
		t.Error("dropped token still loads")
	}
	SaveToken("exp", "old", time.Now().Add(-time.Minute))
	if _, _, ok := LoadToken("exp"); ok {
		t.Error("expired token loaded")
	}
}

func TestEvictRemovesStaleTempFilesAndCountsLiveOnes(t *testing.T) {
	root := diskEnv(t, true)
	c := persistent("ns")
	_, _ = Fetch(t.Context(), c, "k0", counting(new(atomic.Int32), doc{"zero", 0}, true))
	dir := filepath.Join(root, "cache", "ns")
	stale, live := filepath.Join(dir, "a.1.tmp"), filepath.Join(dir, "b.2.tmp")
	for _, p := range []string{stale, live} {
		if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(stale, old, old)
	c.disk.evict(dir)
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale temp file survived: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("live temp file removed: %v", err)
	}
	// The live temp counts toward the cap: with room for less than entry+temp, the entry goes.
	c.disk.maxBytes = 1
	c.disk.evict(dir)
	if n := len(entryFiles(t, dir)); n != 0 {
		t.Errorf("%d entries left; the live temp's bytes were not counted", n)
	}
}
