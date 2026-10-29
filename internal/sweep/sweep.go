// Package sweep is find_stuck_orders: page recent orders in an env and flag the ones sitting
// in a non-terminal ("stuck") status, optionally correlating each to its queue item(s) on the
// orchestrator(s). Read-only. Reuses the order-filter scan (same shape as find_clone_candidates)
// and the cross-orchestrator queue-item search in internal/backends.
package sweep

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/apex"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/backends"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const (
	defaultScanPages = 8
	defaultTop       = 50
	pageSize         = 50
	// pageWindow: pages are independent (the only early exit is an empty page), so the next few
	// are in flight while the current one is processed; pages past the first empty one are discarded.
	pageWindow = 4
)

// DefaultStuckStatuses are "stuck" by default: submitted-but-not-finished, or never completed.
// Terminal/healthy statuses (e.g. forReview, completed) are excluded.
var DefaultStuckStatuses = []string{"inProgress", "incomplete", "pending"}

// now is the clock behind ageHours; tests pin it.
var now = time.Now

// OrderVerdict summarizes an order's queue item(s) on the orchestrator(s).
type OrderVerdict struct {
	Backend         *orchestrator.Orchestrator `json:"backend"` // orchestrator holding the newest queue item; null = none found / lookup failed
	Verdict         string                     `json:"verdict"`
	QueueItemCount  int                        `json:"queueItemCount"`
	QueueItemStatus string                     `json:"queueItemStatus,omitempty"` // PHI-safe: New/InProgress/Failed/Retried/Successful
	ExceptionType   string                     `json:"exceptionType,omitempty"`   // business | application
}

// StuckOrder is one flagged order. The BE pass-through fields are omitted when the row lacks them
// and null when the row has null.
type StuckOrder struct {
	OrderUID     shared.Field  `json:"orderUid,omitzero"`
	Status       shared.Field  `json:"status,omitzero"`
	Type         shared.Field  `json:"type,omitzero"`
	Facility     shared.Field  `json:"facility,omitzero"`
	Insurance    shared.Field  `json:"insurance,omitzero"`
	AgeHours     *int64        `json:"ageHours"`
	Orchestrator *OrderVerdict `json:"orchestrator,omitempty"`
}

// Args are FindStuckOrders' inputs; Env is required. Zero values mean "default": ScanPages 8,
// Statuses DefaultStuckStatuses, Top 50, Orchestrator = every orchestrator.
type Args struct {
	Env            config.Env
	Profile        string // "" = none
	ScanPages      int
	Statuses       []string
	OlderThanHours float64
	CrossCheck     bool
	Orchestrator   orchestrator.Orchestrator
	Since          string
	Top            int
	OnProgress     shared.StepProgress
}

// Result is find_stuck_orders' result.
type Result struct {
	Env      config.Env   `json:"env"`
	Scanned  int          `json:"scanned"`
	Statuses []string     `json:"statuses"`
	Found    int          `json:"found"`
	Stuck    []StuckOrder `json:"stuck"`
}

// parseDate mirrors the Date.parse inputs a BE row carries: zoned ISO timestamps, date-only ISO
// (UTC), and — as JS reads them — zoneless date-times, US M/D/YYYY and the BE's real display
// format "Oct 14, 2025" as LOCAL time.
func parseDate(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "Jan 2, 2006", "1/2/2006"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ageHours is the best-effort age in hours from whatever timestamp the row carries; nil if none parse.
func ageHours(row copilot.BeOrder) *int64 {
	for _, k := range []string{"orderDate", "encounterDate", "appointmentDate"} {
		t, ok := parseDate(row.Str(k))
		if !ok {
			continue
		}
		h := int64(math.Floor(now().Sub(t).Hours() + 0.5)) // Math.round: halves toward +Inf
		return &h
	}
	return nil
}

// FindStuckOrders scans up to ScanPages pages of recent outbound referrals, flags the stuck
// ones, and (with CrossCheck) attaches each one's queue-item verdict.
func FindStuckOrders(ctx context.Context, args Args) (Result, error) {
	scanPages := cmp.Or(args.ScanPages, defaultScanPages)
	wanted := args.Statuses
	if wanted == nil {
		wanted = DefaultStuckStatuses
	}
	statuses := []string{}
	for _, st := range wanted {
		statuses = append(statuses, strings.ToLower(st))
	}

	session, err := copilot.Connect(ctx, args.Env, args.Profile)
	if err != nil {
		return Result{}, err
	}

	stuck := []StuckOrder{}
	creationDateByUID := map[string]string{} // internal-only: not part of StuckOrder's public shape
	scanned := 0
	err = scanPagesOf(ctx, session.Client, scanPages, func(page int, rows []copilot.BeOrder) {
		for _, o := range rows {
			scanned++
			if !slices.Contains(statuses, strings.ToLower(o.Str("status"))) {
				continue
			}
			age := ageHours(o)
			if args.OlderThanHours > 0 && (age == nil || float64(*age) < args.OlderThanHours) {
				continue
			}
			stuck = append(stuck, StuckOrder{
				OrderUID: o.Pass("orderUid"), Status: o.Pass("status"), Type: o.Pass("orderType", "name"),
				Facility: o.Pass("referredFacility", "name"), Insurance: o.Pass("insurance", "name"), AgeHours: age,
			})
			if uid := o.Str("orderUid"); uid != "" {
				creationDateByUID[uid] = o.Str("creationDate")
			}
		}
		args.OnProgress.Step(page+1, scanPages, fmt.Sprintf("page %d: %d scanned, %d stuck", page+1, scanned, len(stuck)))
	})
	if err != nil {
		return Result{}, err
	}

	if args.CrossCheck {
		bs, err := backends.SelectBackends(args.Orchestrator, "")
		if err != nil {
			return Result{}, err
		}
		top := cmp.Or(args.Top, defaultTop)
		// Request-scoped Apex queue-list memo: the per-order lookups share one queue listing.
		ctx = apex.WithQueueListMemo(ctx)
		correlate := func(ctx context.Context, uid string) ([]orchestrator.QueueItemCore, error) {
			found := backends.FindOrderAcross(ctx, orchestrator.FindOrderArgs{
				Env: args.Env, OrderUID: uid, Since: queueSearchSince(args.Since, creationDateByUID[uid]),
				Top: top, IncludeSpecificContent: false,
			}, bs)
			return itemsOrError(found)
		}
		// Offset past the page scan so progress stays monotonic across both phases. The candidate
		// count isn't known until the scan finishes, so the total grows here — allowed (total is
		// advisory), and better than reporting the cross-check silently.
		CrossCheckOrders(ctx, stuck, correlate, func(done, total int, label string) {
			args.OnProgress.Step(scanPages+done, scanPages+total, label)
		})
	}

	return Result{Env: args.Env, Scanned: scanned, Statuses: statuses, Found: len(stuck), Stuck: stuck}, nil
}
