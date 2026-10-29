package uipath

// Terminal-job caches: RobotLogs and the job detail (memory in front of the persistent disk tier of
// internal/memo; logs hold PHI, so see memo for the file modes and the sessionCache:false switch).
// Read side only — FetchJobLogs stays
// the raw read, and every write guard (FetchJobByKey in actions.go, GetQueueItem) is untouched.
//
// The cache is keyed by the EXACT query (orchestrator, resolved folder, job key, the full OData
// params plus the client-side refinement inputs), with no client-side refiltering: OData
// `contains` collation differs from Go's, so a superset could not be filtered locally without
// changing results. An entry is stored only when a LIVE read of the job in the same call shows it
// terminal and settled. UiPath's terminal states are absorbing, so a hit is served with no upstream
// call at all — a per-hit job-state read would spend the tenant's 100 req/min /odata/Jobs budget
// for nothing.

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ponytail: unmeasured guesses. RobotLogs are shipped by the robot, so ingestion lag after the
// state flips is plausible; measure EndTime -> last-row lag, then tune. Row cost is a flat
// overhead plus the text; measure heap per cached result before trusting it.
const (
	settleWindow = 5 * time.Minute
	logRowCost   = 128
)

var (
	logCache = memo.NewPersistent("uipath-logs", memo.DefaultTotalBytes, memo.DefaultEntryBytes, memo.DefaultMaxAge)
	jobCache = memo.NewPersistent("uipath-jobs", memo.DefaultTotalBytes, memo.DefaultEntryBytes, memo.DefaultMaxAge)
)

// A config reload flushes MEMORY only: keys carry the orchestrator URL and folder, so what is on
// disk under another account or tenant can never be served for this one.
func init() {
	config.OnReload(func(config.ReloadInfo) {
		logCache.Flush()
		jobCache.Flush()
	})
}

// settled reports whether job, as read live just now, is terminal and ended at least settleWindow
// ago. A missing job, EndTime or an unparsable one is not settled.
func settled(job *Job) bool {
	if job == nil || !TerminalStates[job.State] {
		return false
	}
	ended, ok := shared.ParseTime(job.EndTime)
	return ok && time.Since(ended) >= settleWindow
}

// fetchJobState is the light live read behind a verdict when the caller has no job in hand:
// only the fields settled() needs.
func fetchJobState(ctx context.Context, jobKey, folder string) (*Job, error) {
	data, err := get(ctx, "/odata/Jobs", map[string]string{
		"$filter": "Key eq " + jobKey,
		"$top":    "1",
		"$select": "Id,Key,State,EndTime",
	}, folder, "")
	if err != nil {
		return nil, err
	}
	rows := odataValues(data)
	if len(rows) == 0 {
		return nil, nil //nolint:nilnil // nil job = the key does not resolve
	}
	job := toJob(rows[0])
	return &job, nil
}

func logsKey(cfg config.UipathConfig, jobKey, folder string, f LogFilter) string {
	tail := "-"
	if f.Tail != nil {
		tail = strconv.Itoa(*f.Tail)
	}
	// url.Values.Encode sorts by key, so the params part is canonical.
	return base(cfg) + "|" + folder + "|" + jobKey + "|" +
		url.Values(toValues(jobLogQueryParams(jobKey, f))).Encode() + "|" + strconv.FormatBool(f.OnlyFailures) + "|" + tail
}

func logResultCost(r LogResult) int {
	n := 0
	for _, l := range r.Logs {
		n += logRowCost + len(l.Level) + len(l.Message) + len(l.TimeStamp) + len(l.RawMessage)
	}
	return n
}

// cachedJobLogs is FetchJobLogs for the read side. A hit is served as-is. On a miss, verdict is the
// live read of the job (it runs concurrently with the logs fetch): the result is stored only if it
// shows the job terminal and settled.
func cachedJobLogs(ctx context.Context, jobKey, folder string, f LogFilter, verdict func(context.Context) (*Job, error)) (LogResult, error) {
	cfg, err := config.Uipath()
	if err != nil || jobKey == "" {
		return FetchJobLogs(ctx, jobKey, folder, f)
	}
	key := logsKey(cfg, jobKey, folder, f)
	if hit, ok := memo.Peek[LogResult](ctx, logCache, key); ok {
		return hit, nil
	}
	return memo.Fetch(ctx, logCache, key, func(ctx context.Context) (LogResult, int, bool, error) {
		var wg sync.WaitGroup
		var job *Job
		var verr error
		wg.Go(func() { job, verr = verdict(ctx) })
		res, err := FetchJobLogs(ctx, jobKey, folder, f)
		wg.Wait()
		return res, logResultCost(res), err == nil && verr == nil && settled(job), err
	})
}

// cachedJobByKey is FetchJobByKey for the read side (get_job, the faults builder): a job that is
// terminal AND settled is served from memory/disk, anything else is read live every time. The
// write guards (actions.go) call FetchJobByKey directly and never see this cache.
// Caveat: ProcessVersion and RobotName are live $expand joins (the release may be re-pinned, the
// robot renamed), so a cached detail reports them as of the moment it was cached; refresh=true
// re-reads.
func cachedJobByKey(ctx context.Context, jobKey, folder string) (*Job, error) {
	key := strings.TrimSpace(jobKey)
	cfg, err := config.Uipath()
	if err != nil || key == "" {
		return FetchJobByKey(ctx, jobKey, folder)
	}
	ck := base(cfg) + "|" + folder + "|job|" + key
	job, ok := memo.Peek[*Job](ctx, jobCache, ck)
	if !ok {
		job, err = memo.Fetch(ctx, jobCache, ck, fetchJobForCache(key, folder))
	}
	if err != nil || job == nil {
		return nil, err
	}
	cp := *job
	return &cp, nil
}

func fetchJobForCache(key, folder string) func(context.Context) (*Job, int, bool, error) {
	return func(ctx context.Context) (*Job, int, bool, error) {
		j, err := FetchJobByKey(ctx, key, folder)
		if err != nil || j == nil {
			return nil, 0, false, err
		}
		return j, 512 + len(j.OutputArguments) + len(j.InputArguments), settled(j), nil
	}
}
