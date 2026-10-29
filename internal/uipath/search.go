package uipath

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ---- Queue-item order correlation (the order → job correlation path) --------

// QueueItemMatch is a queue item projected for order↔job correlation. Every order flows through
// UiPath as a queue item, and the item is the ONE place the orderUid reliably lives: a
// queue-consumer job carries it in NEITHER its InputArguments NOR its (all-null, when faulted)
// OutputArguments. The item links back to its job via ExecutorJobKey, so this is the single
// correlation path — it reaches faulted/running consumer jobs (which an OutputArguments scan
// structurally misses) as well as the successful ones.
//
// SpecificContent is returned DELIBERATELY, in full: the point is divergence analysis — comparing
// what Copilot actually sent to UiPath (member/clinical fields included) against the order
// record. The JWT `token` is the only redacted field (a live credential, opaque, and useless for
// divergence) — its value becomes "[redacted]".
type QueueItemMatch struct {
	ID                      int64
	Reference               string
	Status                  string // New / InProgress / Failed / Retried / Successful
	ExecutorJobKey          string // "" when the item hasn't been picked up by a robot yet
	ProcessingExceptionType string // e.g. BusinessException; "" when none
	RetryNumber             int64
	CreationTime            string
	SpecificContent         map[string]any // full payload; token redacted
	QueueDefinitionID       int64          // which queue (submit vs sync) the item sits in
}

// confirmQueueItemMatch confirms a raw QueueItems DTO truly belongs to orderID (the
// contains(SpecificData, uid) filter can match incidental substrings) and projects it to a
// QueueItemMatch; ok is false for a non-match. Nested SpecificContent/orderUid filtering is NOT
// supported by Orchestrator's OData, so the server filters the raw SpecificData JSON string and
// this re-confirms against the parsed SpecificContent.orderUid client-side.
func confirmQueueItemMatch(raw any, orderID string) (QueueItemMatch, bool) {
	content := recField(raw, "SpecificContent")
	uid := shared.Str(content, "orderUid")
	if uid == "" || uid != strings.TrimSpace(orderID) {
		return QueueItemMatch{}, false
	}
	redacted := maps.Clone(content)
	if _, ok := redacted["token"]; ok {
		redacted["token"] = "[redacted]"
	}
	return QueueItemMatch{
		ID:                      numField(raw, "Id"),
		Reference:               shared.Str(raw, "Reference"),
		Status:                  shared.Str(raw, "Status"),
		ExecutorJobKey:          shared.Str(raw, "ExecutorJobKey"),
		ProcessingExceptionType: shared.Str(raw, "ProcessingExceptionType"),
		RetryNumber:             numField(raw, "RetryNumber"),
		CreationTime:            shared.Str(raw, "CreationTime"),
		SpecificContent:         redacted,
		QueueDefinitionID:       numField(raw, "QueueDefinitionId"),
	}, true
}

// The QueueItemSearchResult.SearchMode values.
const (
	searchContains   = "contains"
	searchRecentScan = "recent-scan"
)

// QueueItemSearchResult is the outcome of searchQueueItemsByOrderID.
type QueueItemSearchResult struct {
	Matches     []QueueItemMatch
	Scanned     int // raw rows examined (before client-side confirm)
	SearchMode  string
	Notes       []string
	SearchError string // set when tier 1 was rejected and the recent-scan fallback ran
}

// queueItemSelect is exactly the fields confirmQueueItemMatch reads. SpecificContent is selected
// WHOLE: a nested $select (SpecificContent/orderUid) comes back null from this endpoint, and an
// outer $select combined with an $expand 400s (see listQueueItems) — so no $expand here.
const queueItemSelect = "Id,Reference,Status,ExecutorJobKey,ProcessingExceptionType,RetryNumber,CreationTime,SpecificContent,QueueDefinitionId"

func confirmAll(raws []any, needle string) []QueueItemMatch {
	matches := []QueueItemMatch{}
	for _, r := range raws {
		if m, ok := confirmQueueItemMatch(r, needle); ok {
			matches = append(matches, m)
		}
	}
	return matches
}

// searchQueueItemsByOrderID finds the queue item(s) an order flowed through, PHI-safe. Three tiers:
// Tier 1 — server-side contains(SpecificData, uid) — filter on the raw SpecificData JSON string
// (nested SpecificContent/orderUid filtering isn't supported) — narrowed by `since` (indexed
// CreationTime bound) when given, projected down to queueItemSelect since only a handful of fields
// are ever read from a match. Ordered CreationTime desc so a still-New (not-yet-picked-up) item
// isn't pushed past $top.
// Tier 1b — the guard that makes the $select safe: if the projected query fails, or scans rows but
// confirms none (the tenant may silently null SpecificContent under $select rather than 400ing),
// re-issue the identical filter with full rows.
// Tier 2 — only when tier 1b itself fails: a bounded recent-QueueItems scan confirmed client-side
// (mirrors ListRecentJobs), no $select (last-resort path).
func searchQueueItemsByOrderID(ctx context.Context, orderID string, scope FolderScope, top int, since string) (QueueItemSearchResult, error) {
	needle := strings.TrimSpace(orderID)
	if needle == "" {
		return QueueItemSearchResult{Matches: []QueueItemMatch{}, SearchMode: searchContains, Notes: []string{}}, nil
	}
	clauses := []string{fmt.Sprintf("contains(SpecificData, '%s')", odataQuote(needle))}
	sinceBound := ""
	if since != "" {
		c, err := sinceClause(since)
		if err != nil {
			return QueueItemSearchResult{}, err
		}
		sinceBound = c
		clauses = append(clauses, c)
	}
	filter := strings.Join(clauses, " and ")

	containsQuery := func(withSelect bool) ([]any, error) {
		params := map[string]string{"$filter": filter, "$orderby": "CreationTime desc", "$top": fmt.Sprint(top)}
		if withSelect {
			params["$select"] = queueItemSelect
		}
		data, err := get(ctx, "/odata/QueueItems", params, scope.FolderPath, scope.OrgUnitID)
		return odataValues(data), err
	}

	notes := []string{}
	raws, err := containsQuery(true)
	if err == nil {
		matches := confirmAll(raws, needle)
		if len(matches) > 0 || len(raws) == 0 {
			return QueueItemSearchResult{Matches: matches, Scanned: len(raws), SearchMode: searchContains, Notes: notes}, nil
		}
		notes = append(notes, "queue-item $select returned rows with no readable SpecificContent; re-queried with full rows.")
	} else {
		notes = append(notes, fmt.Sprintf("queue-item $select rejected by Orchestrator; re-queried with full rows. (%s)", err))
	}

	fullRaws, err := containsQuery(false)
	if err == nil {
		return QueueItemSearchResult{Matches: confirmAll(fullRaws, needle), Scanned: len(fullRaws), SearchMode: searchContains, Notes: notes}, nil
	}
	searchError := err.Error()
	scan := map[string]string{"$orderby": "CreationTime desc", "$top": fmt.Sprint(top)}
	if sinceBound != "" {
		scan["$filter"] = sinceBound
	}
	data, err := get(ctx, "/odata/QueueItems", scan, scope.FolderPath, scope.OrgUnitID)
	if err != nil {
		return QueueItemSearchResult{}, err
	}
	scanned := odataValues(data)
	notes = append(notes, fmt.Sprintf("SpecificData filter rejected by Orchestrator; fell back to scanning the %d most-recent queue item(s).", len(scanned)))
	return QueueItemSearchResult{
		Matches:     confirmAll(scanned, needle),
		Scanned:     len(scanned),
		SearchMode:  searchRecentScan,
		Notes:       notes,
		SearchError: searchError,
	}, nil
}
