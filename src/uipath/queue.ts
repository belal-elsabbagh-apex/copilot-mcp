// UiPath queue-item URL parsing — the one UiPath-specific piece of pull_queue_item that is
// not an OData call (uipathPullQueueItem in backend.ts does the fetching).

const URL_TXN_RE = /\/transactions\/(\d+)\/details/;
const URL_QUEUE_RE = /\/queues\/(\d+)\/transactions/;

export interface ParsedQueueUrl {
  txnId: number;
  folderId: string;
  queueId: number; // 0 if the URL had no /queues/<id>/ segment
}

// Pull (txnId, folderId, queueId) out of an Orchestrator queue-item URL. The txn id
// lives only in the matrix-parameter `(sidepanel:.../transactions/<id>/details)`
// path segment, and the folder id only in the `fid` query param. Throws if either
// required part is absent.
export function parseQueueItemUrl(url: string): ParsedQueueUrl {
  const u = new URL(url);
  const folderId = u.searchParams.get("fid");
  if (!folderId) throw new Error(`queue-item URL is missing the 'fid' query param: ${url}`);
  const haystack = u.pathname + u.search;
  const txnMatch = URL_TXN_RE.exec(haystack);
  if (!txnMatch)
    throw new Error(`queue-item URL is missing a /transactions/<id>/details segment: ${url}`);
  const queueMatch = URL_QUEUE_RE.exec(haystack);
  return {
    txnId: Number(txnMatch[1]),
    folderId,
    queueId: queueMatch ? Number(queueMatch[1]) : 0,
  };
}
