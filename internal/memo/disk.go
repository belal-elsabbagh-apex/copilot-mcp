package memo

// The persistent tier: one JSON file per entry under <config.CacheDir>/cache/<namespace>/, named
// by sha256(key) so no job id, URL or PHI shows in a path. Dir 0700, files 0600, written through a
// temp file + rename so a reader never sees a torn file. Memory stays in front of it: reads go
// memory -> disk -> fetch, and a disk hit repopulates memory. Everything is best-effort: a disk
// failure degrades to memory-only with ONE warning per cache and never fails the caller. The tier
// is off when copilot.sessionCache is false (the single "nothing on disk" switch).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

// staleTmpAge: a write takes milliseconds, so a temp file this old belongs to a killed process.
const staleTmpAge = time.Hour

// ponytail: unmeasured. 512 MiB per namespace and 30 days are sized for "keep every settled job
// log I looked at this month"; measure real job.log / RobotLogs sizes and tune. The age cap only
// bounds how long data later redacted or purged upstream can linger (Refresh overrides it).
const (
	DiskMaxBytes = 512 << 20
	DiskMaxAge   = 30 * 24 * time.Hour
)

type refreshKey struct{}

// WithRefresh marks ctx so cache READS (memory and disk) are skipped; a cacheable fresh result is
// still stored, replacing the old entry.
func WithRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, refreshKey{}, true)
}

// Refresh reports whether ctx carries WithRefresh.
func Refresh(ctx context.Context) bool {
	v, _ := ctx.Value(refreshKey{}).(bool)
	return v
}

// RefreshIf is WithRefresh when on.
func RefreshIf(ctx context.Context, on bool) context.Context {
	if on {
		return WithRefresh(ctx)
	}
	return ctx
}

type diskTier struct {
	ns       string
	maxBytes int64
	maxAge   time.Duration
	warn     sync.Once
}

// envelope is the file format: the metadata beside the JSON value.
type envelope struct {
	StoredAt int64           `json:"storedAt"` // unix ms
	Size     int             `json:"size"`
	Value    json.RawMessage `json:"value"`
}

func hashName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (d *diskTier) dir() (string, bool) {
	if !config.DiskCacheEnabled() {
		return "", false
	}
	return filepath.Join(config.CacheDir(), "cache", d.ns), true
}

func (d *diskTier) fail(op string, err error) {
	d.warn.Do(func() {
		slog.Warn("disk cache disabled for this process after a failure", "namespace", d.ns, "op", op, "err", err)
	})
}

// read returns the stored value bytes and the file size. Anything unreadable, corrupt or too old
// is removed and reads as a miss.
func (d *diskTier) read(key string) (json.RawMessage, int, bool) {
	dir, ok := d.dir()
	if !ok {
		return nil, 0, false
	}
	path := filepath.Join(dir, hashName(key)+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			d.fail("read", err)
		}
		return nil, 0, false
	}
	var env envelope
	if json.Unmarshal(raw, &env) != nil || len(env.Value) == 0 || time.Since(time.UnixMilli(env.StoredAt)) > d.maxAge {
		_ = os.Remove(path)
		return nil, 0, false
	}
	return env.Value, len(raw), true
}

func (d *diskTier) remove(key string) {
	if dir, ok := d.dir(); ok {
		_ = os.Remove(filepath.Join(dir, hashName(key)+".json"))
	}
}

// write stores val (JSON) and then enforces the bounds.
func (d *diskTier) write(key string, val any, size int) {
	dir, ok := d.dir()
	if !ok {
		return
	}
	value, err := json.Marshal(val)
	if err != nil {
		d.fail("encode", err)
		return
	}
	data, err := json.Marshal(envelope{StoredAt: time.Now().UnixMilli(), Size: size, Value: value})
	if err != nil {
		d.fail("encode", err)
		return
	}
	if int64(len(data)) > d.maxBytes {
		return
	}
	if err := WriteAtomic(dir, hashName(key)+".json", data); err != nil {
		d.fail("write", err)
		return
	}
	d.evict(dir)
}

// evict drops files past maxAge, then the oldest until the namespace fits maxBytes. A temp file
// left by a killed write can hold partial PHI: one older than staleTmpAge is removed, and a live
// one counts toward the byte total (it is not evictable — a writer may still own it).
// ponytail: a directory scan per write; fine for hundreds of files, index it if it ever isn't.
func (d *diskTier) evict(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		d.fail("scan", err)
		return
	}
	type file struct {
		path  string
		mtime time.Time
		size  int64
	}
	var files []file
	var total int64
	for _, e := range ents {
		isTmp := strings.HasSuffix(e.Name(), ".tmp")
		if !isTmp && !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if isTmp {
			if time.Since(info.ModTime()) > staleTmpAge {
				_ = os.Remove(p)
			} else {
				total += info.Size()
			}
			continue
		}
		if time.Since(info.ModTime()) > d.maxAge {
			_ = os.Remove(p)
			continue
		}
		files = append(files, file{p, info.ModTime(), info.Size()})
		total += info.Size()
	}
	slices.SortFunc(files, func(a, b file) int { return a.mtime.Compare(b.mtime) })
	for _, f := range files {
		if total <= d.maxBytes {
			return
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

// WriteAtomic writes data to dir/name (dir 0700, file 0600) via a unique temp file + rename.
func WriteAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, name+".*.tmp") // CreateTemp is 0600
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}

// Persisted credentials: one file per token under <config.CacheDir>/tokens/, named by
// sha256(key), same modes and the same sessionCache switch as the Copilot session cache.
// The caller supplies the expiry it wants honoured (already net of its skew).

type tokenFile struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"` // unix ms
}

func tokenDir() (string, bool) {
	if !config.DiskCacheEnabled() {
		return "", false
	}
	return filepath.Join(config.CacheDir(), "tokens"), true
}

// LoadToken returns a persisted, unexpired token. An unreadable, corrupt or expired file reads as
// a miss and is removed.
func LoadToken(key string) (string, time.Time, bool) {
	dir, ok := tokenDir()
	if !ok {
		return "", time.Time{}, false
	}
	path := filepath.Join(dir, hashName(key)+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}, false
	}
	var t tokenFile
	if json.Unmarshal(raw, &t) != nil || t.Token == "" || !time.UnixMilli(t.ExpiresAt).After(time.Now()) {
		_ = os.Remove(path)
		return "", time.Time{}, false
	}
	return t.Token, time.UnixMilli(t.ExpiresAt), true
}

// SaveToken persists token until expires. Best-effort.
func SaveToken(key, token string, expires time.Time) {
	dir, ok := tokenDir()
	if !ok {
		return
	}
	data, err := json.Marshal(tokenFile{Token: token, ExpiresAt: expires.UnixMilli()})
	if err == nil {
		err = WriteAtomic(dir, hashName(key)+".json", data)
	}
	if err != nil {
		slog.Warn("token cache: write failed", "err", err)
	}
}

// DropToken forgets a persisted token (a 401 rejected it).
func DropToken(key string) {
	if dir, ok := tokenDir(); ok {
		_ = os.Remove(filepath.Join(dir, hashName(key)+".json"))
	}
}
