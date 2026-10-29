package uipath

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
)

// UiPath queue-item URL parsing — the one UiPath-specific piece of pull_queue_item that is not an
// OData call.

var (
	urlTxnRE   = regexp.MustCompile(`/transactions/(\d+)/details`)
	urlQueueRE = regexp.MustCompile(`/queues/(\d+)/transactions`)
)

// parsedQueueURL is the (txnId, folderId, queueId) of an Orchestrator queue-item URL.
type parsedQueueURL struct {
	txnID    int64
	folderID string
	queueID  int64 // 0 if the URL had no /queues/<id>/ segment
}

// parseQueueItemURL pulls (txnId, folderId, queueId) out of an Orchestrator queue-item URL. The
// txn id lives only in the matrix-parameter `(sidepanel:.../transactions/<id>/details)` path
// segment, and the folder id only in the `fid` query param. It errors if either required part is
// absent.
func parseQueueItemURL(raw string) (parsedQueueURL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return parsedQueueURL{}, errors.New("Invalid URL: " + raw) //nolint:staticcheck // user-visible wording, as the TS URL constructor's
	}
	folderID := u.Query().Get("fid")
	if folderID == "" {
		return parsedQueueURL{}, fmt.Errorf("queue-item URL is missing the 'fid' query param: %s", raw)
	}
	haystack := u.Path + "?" + u.RawQuery
	txn := urlTxnRE.FindStringSubmatch(haystack)
	if txn == nil {
		return parsedQueueURL{}, fmt.Errorf("queue-item URL is missing a /transactions/<id>/details segment: %s", raw)
	}
	txnID, err := strconv.ParseInt(txn[1], 10, 64)
	if err != nil {
		return parsedQueueURL{}, fmt.Errorf("queue-item URL has an invalid transaction id %q: %w", txn[1], err)
	}
	out := parsedQueueURL{txnID: txnID, folderID: folderID}
	if q := urlQueueRE.FindStringSubmatch(haystack); q != nil {
		out.queueID, _ = strconv.ParseInt(q[1], 10, 64)
	}
	return out, nil
}
