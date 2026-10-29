package sweep

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/backends"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

func ptr[T any](v T) *T { return &v }

func stuckOrder(uid *string) StuckOrder {
	if uid == nil {
		return StuckOrder{}
	}
	return StuckOrder{OrderUID: shared.Field{V: *uid, Set: true}}
}

func uids(ids ...string) []StuckOrder {
	out := make([]StuckOrder, len(ids))
	for i, id := range ids {
		out[i] = stuckOrder(ptr(id))
	}
	return out
}

func attempt(mod ...func(*orchestrator.JobAttempt)) orchestrator.JobAttempt {
	a := orchestrator.JobAttempt{Orchestrator: orchestrator.UiPath, ID: "j1"}
	for _, m := range mod {
		m(&a)
	}
	return a
}

func neverStarted(v bool) func(*orchestrator.JobAttempt) {
	return func(a *orchestrator.JobAttempt) { a.NeverStarted = &v }
}

func qItem(mod ...func(*orchestrator.QueueItemCore)) orchestrator.QueueItemCore {
	q := orchestrator.QueueItemCore{
		Orchestrator: orchestrator.UiPath, QueueID: "1", ItemID: "1", Status: "New", RawStatus: "New",
		CreatedAt: "2026-07-01T00:00:00Z", Jobs: []orchestrator.JobAttempt{},
	}
	for _, m := range mod {
		m(&q)
	}
	return q
}

func with(status string, jobs ...orchestrator.JobAttempt) func(*orchestrator.QueueItemCore) {
	return func(q *orchestrator.QueueItemCore) { q.Status = status; q.Jobs = jobs }
}

func constItems(items ...orchestrator.QueueItemCore) func(context.Context, string) ([]orchestrator.QueueItemCore, error) {
	return func(context.Context, string) ([]orchestrator.QueueItemCore, error) { return items, nil }
}

func verdicts(stuck []StuckOrder) []string {
	out := make([]string, len(stuck))
	for i, s := range stuck {
		if s.Orchestrator != nil {
			out[i] = s.Orchestrator.Verdict
		}
	}
	return out
}

func TestCrossCheckSkipsEntriesWithoutAnOrderUID(t *testing.T) {
	stuck := []StuckOrder{stuckOrder(nil)}
	CrossCheckOrders(t.Context(), stuck, constItems(), nil)
	if stuck[0].Orchestrator != nil {
		t.Errorf("orchestrator = %+v, want untouched", stuck[0].Orchestrator)
	}
}

func TestCrossCheckAttachesAVerdictPerOrderFromCorrelate(t *testing.T) {
	stuck := uids("u1", "u2")
	CrossCheckOrders(t.Context(), stuck, func(_ context.Context, uid string) ([]orchestrator.QueueItemCore, error) {
		if uid == "u1" {
			return []orchestrator.QueueItemCore{qItem(with("Successful", attempt()))}, nil
		}
		return nil, nil
	}, nil)
	if got, want := verdicts(stuck), []string{"job-successful-order-stuck", "no-job"}; !reflect.DeepEqual(got, want) {
		t.Errorf("verdicts = %v, want %v (input order kept)", got, want)
	}
	if v := stuck[1].Orchestrator; v.Backend != nil || v.QueueItemCount != 0 {
		t.Errorf("no-job verdict = %+v, want null backend and zero count", v)
	}
}

func TestCrossCheckFailedItemIsJobFaultedAndSurfacesExceptionType(t *testing.T) {
	// The faulted consumer job is invisible to an OutputArguments scan; the queue item's
	// status is what makes it directly determinable.
	stuck := uids("u1")
	CrossCheckOrders(t.Context(), stuck, constItems(qItem(with("Failed", attempt()), func(q *orchestrator.QueueItemCore) { q.ExceptionType = ptr("business") })), nil)
	want := OrderVerdict{Backend: ptr(orchestrator.UiPath), Verdict: "job-faulted", QueueItemCount: 1, QueueItemStatus: "Failed", ExceptionType: "business"}
	if got := *stuck[0].Orchestrator; !reflect.DeepEqual(got, want) {
		t.Errorf("verdict = %+v, want %+v", got, want)
	}
}

func TestCrossCheckNewItemIsQueuedUntilAJobAttemptExists(t *testing.T) {
	stuck := uids("u1", "u2")
	CrossCheckOrders(t.Context(), stuck, func(_ context.Context, uid string) ([]orchestrator.QueueItemCore, error) {
		if uid == "u1" {
			return []orchestrator.QueueItemCore{qItem(with("New"))}, nil
		}
		return []orchestrator.QueueItemCore{qItem(with("New", attempt()))}, nil
	}, nil)
	if got, want := verdicts(stuck), []string{"queued-not-picked-up", "job-found"}; !reflect.DeepEqual(got, want) {
		t.Errorf("verdicts = %v, want %v", got, want)
	}
}

func TestCrossCheckInProgressBeatsAnOlderSuccessfulItem(t *testing.T) {
	stuck := uids("u1")
	CrossCheckOrders(t.Context(), stuck, constItems(
		qItem(with("InProgress", attempt(func(a *orchestrator.JobAttempt) { a.ID = "j2" }))),
		qItem(with("Successful", attempt())),
	), nil)
	if got := stuck[0].Orchestrator.Verdict; got != "job-running" {
		t.Errorf("verdict = %s, want job-running", got)
	}
}

func TestCrossCheckNeverStartedNewestAttemptBeatsJobFaulted(t *testing.T) {
	stuck := uids("u1")
	CrossCheckOrders(t.Context(), stuck, constItems(qItem(with("Failed",
		attempt(neverStarted(true)), attempt(neverStarted(false))))), nil)
	if v := stuck[0].Orchestrator; v.Verdict != "job-never-started" || v.QueueItemStatus != "Failed" {
		t.Errorf("verdict = %+v, want job-never-started over a Failed item", v)
	}
}

func TestCrossCheckOnlyTheNewestAttemptOfTheNewestItemDecidesNeverStarted(t *testing.T) {
	stuck := uids("older-item", "older-attempt", "unknown")
	CrossCheckOrders(t.Context(), stuck, func(_ context.Context, uid string) ([]orchestrator.QueueItemCore, error) {
		switch uid {
		case "older-item": // newest item succeeded; the reaped attempt belongs to an older, failed item
			return []orchestrator.QueueItemCore{qItem(with("Successful", attempt(neverStarted(false)))), qItem(with("Failed", attempt(neverStarted(true))))}, nil
		case "older-attempt": // an earlier attempt was reaped, the retry is running
			return []orchestrator.QueueItemCore{qItem(with("InProgress", attempt(neverStarted(false)), attempt(neverStarted(true))))}, nil
		}
		// NeverStarted nil means "backend can't tell", never "never started"
		return []orchestrator.QueueItemCore{qItem(with("Failed", attempt()))}, nil
	}, nil)
	if got, want := verdicts(stuck), []string{"job-faulted", "job-running", "job-faulted"}; !reflect.DeepEqual(got, want) {
		t.Errorf("verdicts = %v, want %v", got, want)
	}
}

func TestCrossCheckAttributesTheVerdictToTheOrchestratorHoldingTheNewestItem(t *testing.T) {
	stuck := uids("both")
	apexAttempt := func(a *orchestrator.JobAttempt) { a.Orchestrator = orchestrator.Apex }
	CrossCheckOrders(t.Context(), stuck, constItems(
		qItem(with("InProgress", attempt(apexAttempt)), func(q *orchestrator.QueueItemCore) { q.Orchestrator = orchestrator.Apex }),
		qItem(with("Failed", attempt())),
	), nil)
	v := stuck[0].Orchestrator
	if v.Backend == nil || *v.Backend != orchestrator.Apex || v.QueueItemCount != 2 {
		t.Errorf("verdict = %+v, want apex backend and 2 items", v)
	}
}

func TestCrossCheckARejectedLookupBecomesThatOrdersErrorVerdictOnly(t *testing.T) {
	stuck := uids("u1", "u2")
	CrossCheckOrders(t.Context(), stuck, func(_ context.Context, uid string) ([]orchestrator.QueueItemCore, error) {
		if uid == "u1" {
			return nil, errors.New("orchestrator timeout")
		}
		return nil, nil
	}, nil)
	if got, want := *stuck[0].Orchestrator, (OrderVerdict{Verdict: "error: orchestrator timeout"}); !reflect.DeepEqual(got, want) {
		t.Errorf("u1 verdict = %+v, want %+v", got, want)
	}
	if got := stuck[1].Orchestrator.Verdict; got != "no-job" {
		t.Errorf("u2 verdict = %s, want no-job (not blocked by u1)", got)
	}
}

func TestCrossCheckRunsAPoolOfFourAndKeepsInputOrder(t *testing.T) {
	ids := make([]string, 12)
	for i := range ids {
		ids[i] = fmt.Sprintf("u%d", i)
	}
	stuck := uids(ids...)
	var inFlight, peak atomic.Int32
	CrossCheckOrders(t.Context(), stuck, func(_ context.Context, uid string) ([]orchestrator.QueueItemCore, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(5 * time.Millisecond)
		return []orchestrator.QueueItemCore{qItem(func(q *orchestrator.QueueItemCore) { q.ItemID = uid })}, nil
	}, nil)
	if p := peak.Load(); p != crossCheckPool {
		t.Errorf("peak concurrency = %d, want %d", p, crossCheckPool)
	}
	for i, s := range stuck {
		if s.Orchestrator == nil || s.Orchestrator.QueueItemCount != 1 {
			t.Errorf("stuck[%d] verdict = %+v, want one item", i, s.Orchestrator)
		}
	}
}

func TestCrossCheckReportsProgressPerOrderCountingOnlyRealCandidates(t *testing.T) {
	// 11 with a uid + 1 without: the uid-less one is never queried, so it's not in the total.
	stuck := append(uids("a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"), stuckOrder(nil))
	var mu sync.Mutex
	var steps []string
	CrossCheckOrders(t.Context(), stuck, constItems(), func(done, total int, _ string) {
		mu.Lock()
		steps = append(steps, fmt.Sprintf("%d/%d", done, total))
		mu.Unlock()
	})
	if len(steps) != 11 || steps[0] != "1/11" || steps[10] != "11/11" {
		t.Errorf("progress = %v, want 1/11 … 11/11", steps)
	}
}

func across(items []orchestrator.QueueItemCore, errs map[orchestrator.Orchestrator]string) backends.AcrossResult {
	return backends.AcrossResult{
		Count: len(items), Items: items, SearchErrors: errs,
		Searched: []orchestrator.Orchestrator{orchestrator.UiPath, orchestrator.Apex},
	}
}

func TestItemsFoundStandEvenIfAnotherOrchestratorsSearchFailed(t *testing.T) {
	items := []orchestrator.QueueItemCore{qItem()}
	got, err := itemsOrError(across(items, map[orchestrator.Orchestrator]string{orchestrator.Apex: "boom"}))
	if err != nil || len(got) != 1 {
		t.Errorf("itemsOrError = %v, %v; want the items, no error", got, err)
	}
}

func TestNothingFoundWithAFailedSearchIsUnknownNotNoJob(t *testing.T) {
	_, err := itemsOrError(across(nil, map[orchestrator.Orchestrator]string{orchestrator.UiPath: "503", orchestrator.Apex: "login failed"}))
	if want := "uipath: 503; apex: login failed"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestNothingFoundAndNothingFailedIsNoJob(t *testing.T) {
	if got, err := itemsOrError(across(nil, nil)); err != nil || len(got) != 0 {
		t.Errorf("itemsOrError = %v, %v; want empty, nil", got, err)
	}
}

func TestQueueSearchSince(t *testing.T) {
	const (
		created = "2026-08-20T12:00:00.000Z"
		floored = "2026-08-19T12:00:00.000Z"
	)
	for _, c := range []struct{ name, caller, created, want string }{
		{"unbounded without caller since or creationDate", "", "", ""},
		{"floors to creationDate minus the processing margin", "", created, floored},
		{"a later caller since wins (tighter)", "2026-08-20T00:00:00.000Z", "2026-08-01T00:00:00.000Z", "2026-08-20T00:00:00.000Z"},
		{"a later order floor wins — never loosens the caller's bound", "2026-01-01T00:00:00.000Z", created, floored},
		{"an unparseable creationDate is ignored", "2026-08-20T00:00:00.000Z", "not-a-date", "2026-08-20T00:00:00.000Z"},
	} {
		if got := queueSearchSince(c.caller, c.created); got != c.want {
			t.Errorf("%s: queueSearchSince(%q, %q) = %q, want %q", c.name, c.caller, c.created, got, c.want)
		}
	}
}
