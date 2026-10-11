// HTTP helpers shared by every orchestrator client (UiPath, Apex): the pause primitive and the
// 429 Retry-After policy, so both backends back off identically.

// GET/odata/Jobs and GET/odata/QueueItems filtered queries are rate-limited by
// Orchestrator at 100 req/min/tenant for direct API usage (this server's usage —
// "non-automation" per UiPath's classification), 1000/min for the Orchestrator
// System Activities. That's exactly fetchJobByKey / searchQueueItemsByOrderId /
// listQueueItems — the calls analyze_order_execution and find_stuck_orders fan out
// concurrently per job/order. A 429 carries a `Retry-After` (seconds) telling the
// caller precisely when the window reopens
// (https://docs.uipath.com/orchestrator/automation-cloud/latest/api-guide/rate-limits#exposed-headers) —
// honor it instead of failing the whole analysis/sweep outright.
export const MAX_RATE_LIMIT_RETRIES = 5;
export const DEFAULT_RATE_LIMIT_RETRY_MS = 5_000; // used when Retry-After is missing/unparseable
export const MAX_RATE_LIMIT_RETRY_MS = 65_000; // clamp: one rate-limit window, never longer

export const sleep = (ms: number): Promise<void> => {
  const { promise, resolve } = Promise.withResolvers<void>();
  setTimeout(resolve, ms);
  return promise;
};

export function rateLimitDelayMs(retryAfterHeader: string | null): number {
  const seconds = retryAfterHeader ? Number(retryAfterHeader) : Number.NaN;
  const ms =
    Number.isFinite(seconds) && seconds >= 0 ? seconds * 1000 : DEFAULT_RATE_LIMIT_RETRY_MS;
  return Math.min(ms, MAX_RATE_LIMIT_RETRY_MS);
}
