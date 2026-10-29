package shared

import (
	"context"
	"strconv"
	"time"
)

// The pause primitive and the 429 Retry-After policy shared by every orchestrator client (UiPath,
// Apex), so both backends back off identically.
//
// GET /odata/Jobs and GET /odata/QueueItems filtered queries are rate-limited by Orchestrator at
// 100 req/min/tenant for direct API usage (this server's usage — "non-automation" per UiPath's
// classification), 1000/min for the Orchestrator System Activities. That's exactly the job-by-key,
// queue-items-by-order and queue-item listing calls that find_stuck_orders and the per-job batch
// reads fan out concurrently. A 429 carries a `Retry-After` (seconds) telling the caller precisely
// when the window reopens
// (https://docs.uipath.com/orchestrator/automation-cloud/latest/api-guide/rate-limits#exposed-headers)
// — honor it instead of failing the whole analysis/sweep outright.
const (
	MaxRateLimitRetries     = 5
	DefaultRateLimitRetry   = 5 * time.Second  // used when Retry-After is missing/unparseable
	MaxRateLimitRetryWindow = 65 * time.Second // clamp: one rate-limit window, never longer
)

// RateLimitDelay turns a Retry-After header (seconds) into the pause before the next attempt.
func RateLimitDelay(retryAfter string) time.Duration {
	d := DefaultRateLimitRetry
	if secs, err := strconv.ParseFloat(retryAfter, 64); err == nil && secs >= 0 {
		d = time.Duration(secs * float64(time.Second))
	}
	return min(d, MaxRateLimitRetryWindow)
}

// Sleep pauses for d, returning early with ctx's error when the call is cancelled.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
