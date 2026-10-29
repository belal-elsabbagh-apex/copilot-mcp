package apex

// Terminal-job read cache (memory in front of internal/memo's persistent disk tier; job.log holds
// PHI, see memo for modes and the sessionCache:false switch). It caches exactly two things for the
// READ side — the artifact listing and the parsed job.log — and only for a job that a live
// getJob in the same call shows terminal and settled. The job detail itself is never cached
// (callback_status / needs_review move after the end, and a superseded retry parent can flip
// failed -> cancelled). Write guards and get_job_artifacts call listArtifacts / getJob directly,
// so they never see a cached value.

import (
	"context"
	"fmt"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ponytail: the settle windows are unmeasured guesses. Nothing shows job.log / the artifact list
// is complete the instant the status flips, and cancelled/lost/timed_out are the statuses most
// likely to be written late. Measure ended_at -> artifact-complete lag, then tune.
const (
	settleWindow     = 5 * time.Minute
	lateSettleWindow = 15 * time.Minute
	// ponytail: artifact rows cost a flat guess; a parsed log costs 1.5x its raw bytes (the Go
	// structs are bigger than the text). Measure heap per cached log before trusting either.
	artifactRowCost = 256
)

var (
	readCache  = memo.NewPersistent("apex", memo.DefaultTotalBytes, memo.DefaultEntryBytes, memo.DefaultMaxAge)
	lateStatus = map[string]bool{"cancelled": true, "lost": true, "timed_out": true}
)

// settled reports whether row, as read live just now, is terminal and ended long enough ago that
// its artifacts can be trusted to be final. A missing or unparsable completed_at is not settled.
func settled(row JobRow) bool {
	if !IsTerminal(row.Status) || row.CompletedAt == nil {
		return false
	}
	ended, ok := shared.ParseTime(*row.CompletedAt)
	if !ok {
		return false
	}
	window := settleWindow
	if lateStatus[row.Status] {
		window = lateSettleWindow
	}
	return time.Since(ended) >= window
}

// cacheKey scopes an entry to the upstream, the RESOLVED folder id and the job: a job read under
// folder A must never satisfy a call that passes folder B.
func cacheKey(ctx context.Context, env config.Env, folder, kind, jobID, extra string) (string, error) {
	cfg, err := config.ResolveApex()
	if err != nil {
		return "", err
	}
	f, err := ResolveFolderID(ctx, env, folder)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s", kind, cfg.APIURL, f.ID, jobID, extra), nil
}

// cachedRead serves key from the cache when row is settled, else runs fetch; the result is
// stored only when row is settled and fetch succeeded. An unresolvable key (config/folder error)
// falls through to fetch, which surfaces the same error.
func cachedRead[T any](ctx context.Context, row JobRow, key func() (string, error), fetch func(context.Context) (T, int, error)) (T, error) {
	k, err := key()
	if err != nil {
		v, _, ferr := fetch(ctx)
		return v, ferr
	}
	ok := settled(row)
	if ok {
		if v, hit := memo.Peek[T](ctx, readCache, k); hit {
			return v, nil
		}
	}
	return memo.Fetch(ctx, readCache, k, func(ctx context.Context) (T, int, bool, error) {
		v, size, err := fetch(ctx)
		return v, size, ok, err
	})
}

// cachedArtifacts is the read-side artifact listing of the job `row` (a live read).
func cachedArtifacts(ctx context.Context, env config.Env, folder string, row JobRow) ([]Artifact, error) {
	return cachedRead(ctx, row,
		func() (string, error) { return cacheKey(ctx, env, folder, "artifacts", row.ID, "") },
		func(ctx context.Context) ([]Artifact, int, error) {
			a, err := listArtifacts(ctx, env, folder, row.ID)
			return a, len(a) * artifactRowCost, err
		})
}

// cachedLog is the read-side parsed job.log of `row` for one listed artifact. The artifact id and
// size are part of the key: a re-upload is a different log.
func cachedLog(ctx context.Context, env config.Env, folder string, row JobRow, artifact Artifact) (readLog, error) {
	return cachedRead(ctx, row,
		func() (string, error) {
			return cacheKey(ctx, env, folder, "log", row.ID, fmt.Sprintf("%s|%d", artifact.ID, artifact.SizeBytes))
		},
		func(ctx context.Context) (readLog, int, error) {
			l, err := readLogArtifact(ctx, artifact)
			return l, l.rawBytes * 3 / 2, err
		})
}
