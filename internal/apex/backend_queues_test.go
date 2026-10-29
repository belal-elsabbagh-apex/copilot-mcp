package apex

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func queueNamedID(id, name string) Queue { return queueNamed(id, name, true) }

func ids[T any](xs []T, id func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = id(x)
	}
	return out
}

func TestListQueuesFiltersByNameKeepsInactiveAndCutsToTop(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/queues", pagedReply([]Queue{
		queueNamedID("q1", "PMG auth submit queue"), queueNamed("q2", "CenCal shared-adapter test queue", false), queueNamedID("q3", "pmg auth sync queue"),
	}, 3))
	all, err := Backend.ListQueues(t.Context(), orchestrator.ListQueuesArgs{Env: config.PreProd, Top: 10, NameContains: "PMG"})
	if err != nil || !slices.Equal(ids(all, func(q orchestrator.QueueCore) string { return q.ID }), []string{"q1", "q3"}) {
		t.Fatalf("ListQueues(PMG) = %v, %v", all, err)
	}
	inactive, _ := Backend.ListQueues(t.Context(), orchestrator.ListQueuesArgs{Env: config.PreProd, Top: 10, NameContains: "test"})
	if len(inactive) != 1 || inactive[0].Active {
		t.Errorf("ListQueues(test) = %+v, want the inactive queue", inactive)
	}
	if one, _ := Backend.ListQueues(t.Context(), orchestrator.ListQueuesArgs{Env: config.PreProd, Top: 1}); len(one) != 1 {
		t.Errorf("top=1 returned %d queues", len(one))
	}
}

func TestListProcessesFiltersWorkflowsByNameAndRenamesFields(t *testing.T) {
	f := newFakeApex(t, nil)
	other := workflowFixture()
	other.ID, other.Name = "w2", "other"
	f.on("GET", "/workflows", pagedReply([]Workflow{workflowFixture(), other}, 2))
	out, err := Backend.ListProcesses(t.Context(), orchestrator.ListProcessesArgs{Env: config.PreProd, Top: 10, NameContains: "DEMO"})
	if err != nil || len(out) != 1 || *out[0].Package != "demo-auth-submit" || *out[0].Version != "1.0.2" {
		t.Errorf("ListProcesses = %+v, %v", out, err)
	}
}

func TestListTriggersReturnsQueueTriggersThenScheduleTriggers(t *testing.T) {
	f := newFakeApex(t, nil)
	noDrain := queueNamedID("q2", "Q2")
	noDrain.AutoDrain = false
	f.on("GET", "/queues", pagedReply([]Queue{queueNamedID("q1", "Q1"), noDrain}, 2))
	f.on("GET", "/schedules", pagedReply([]Schedule{{ID: "s1", WorkflowID: workflowID, CronExpression: "0 6 * * *", Timezone: "UTC"}}, 1))
	out, err := Backend.ListTriggers(t.Context(), orchestrator.ListTriggersArgs{Env: config.PreProd, Top: 10})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tr := range out {
		got = append(got, fmt.Sprintf("%s:%v", tr.Kind, tr.Enabled))
	}
	if want := []string{"queue:true", "queue:false", "time:true"}; !slices.Equal(got, want) {
		t.Errorf("triggers = %v, want %v", got, want)
	}
}

func TestListTriggersQueueIDKeepsThatQueuesTriggerOnlyAndSkipsSchedules(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/queues", pagedReply([]Queue{queueNamedID("q1", "Q1"), queueNamedID("q2", "Q2")}, 2))
	f.on("GET", "/schedules", pagedReply([]Schedule{{ID: "s1"}}, 1))
	out, err := Backend.ListTriggers(t.Context(), orchestrator.ListTriggersArgs{Env: config.PreProd, Top: 10, QueueID: "q2"})
	if err != nil || len(out) != 1 || out[0].ID != "q2" {
		t.Fatalf("ListTriggers = %+v, %v", out, err)
	}
	if n := len(f.callsTo("GET", "/schedules")); n != 0 {
		t.Errorf("schedule requests = %d, want 0", n)
	}
}

// ---- ListQueueItems ----

func pmgQueues() []Queue {
	return []Queue{queueNamedID("q-pmg", "PMG auth submit queue"), queueNamedID("q-pmg-sync", "PMG auth sync queue")}
}

func TestListQueueItemsResolvesThePortalsQueueMapsStatusToWireAndOmitsContent(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/queues?", pagedReply(pmgQueues(), 2))
	item := itemFixture()
	item.Status = "in_progress"
	f.on("GET", "/queues/q-pmg/items", pagedReply([]QueueItem{item}, 1))
	out, err := Backend.ListQueueItems(t.Context(), orchestrator.ListQueueItemsArgs{
		Env: config.PreProd, Queue: "pmg", QueueKind: orchestrator.QueueSubmit, Status: orchestrator.StatusInProgress, Top: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := queryOf(t, f.callsTo("GET", "/queues/q-pmg/items")[0]).Get("status"); got != "in_progress" {
		t.Errorf("status param = %q, want in_progress", got)
	}
	it := out.Items[0]
	if out.QueueID != "q-pmg" || out.QueueName != "PMG auth submit queue" || it.Status != "InProgress" || it.RawStatus != "in_progress" ||
		it.SpecificContent != nil || len(it.Jobs) != 1 || it.Jobs[0].ID != jobID || it.Jobs[0].State != nil {
		t.Errorf("result = %+v", out)
	}
}

func TestListQueueItemsSyncKindPicksTheSyncQueueAndNoStatusSendsNoStatusParam(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/queues?", pagedReply(pmgQueues(), 2))
	f.on("GET", "/queues/q-pmg-sync/items", pagedReply([]QueueItem{}, 0))
	if _, err := Backend.ListQueueItems(t.Context(), orchestrator.ListQueueItemsArgs{Env: config.PreProd, Queue: "PMG", QueueKind: orchestrator.QueueSync, Top: 5}); err != nil {
		t.Fatal(err)
	}
	if queryOf(t, f.callsTo("GET", "/queues/q-pmg-sync/items")[0]).Has("status") {
		t.Error("a status param was sent without a status filter")
	}
}

func TestListQueueItemsUnknownQueueIsExpectedAndListsWhatExists(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/queues?", pagedReply(pmgQueues(), 2))
	_, err := Backend.ListQueueItems(t.Context(), orchestrator.ListQueueItemsArgs{Env: config.PreProd, Queue: "nope", QueueKind: orchestrator.QueueSubmit, Top: 5})
	if !shared.IsExpected(err) || !strings.Contains(err.Error(), "PMG auth submit queue") {
		t.Errorf("error = %v, want an expected error listing the queues", err)
	}
}

// ---- FindOrderQueueItems ----

func findArgs() orchestrator.FindOrderArgs {
	return orchestrator.FindOrderArgs{Env: config.PreProd, OrderUID: "order-1", Top: 10, IncludeSpecificContent: true}
}

func routeFind(t *testing.T, f *fakeApex, queueCount int) {
	t.Helper()
	item := func(mod func(*QueueItem)) QueueItem { i := itemFixture(); mod(&i); return i }
	exact := item(func(i *QueueItem) { i.ID, i.Reference, i.CreatedAt = "i-new", "R2", "2026-10-08T12:00:00Z" })
	older := item(func(i *QueueItem) { i.ID, i.Reference, i.CreatedAt = "i-old", "R1", "2026-10-07T12:00:00Z" })
	fuzzy := item(func(i *QueueItem) {
		i.ID, i.Reference, i.SpecificContent = "i-fuzzy", "R9", map[string]any{"orderUid": "order-10"}
	})
	queues := make([]Queue, queueCount)
	for i := range queues {
		name := fmt.Sprintf("Q%d", i)
		if i == 0 {
			name = "PMG auth submit queue"
		}
		queues[i] = queueNamedID(fmt.Sprintf("q%d", i), name)
	}
	f.on("GET", "/queues?", pagedReply(queues, queueCount))
	f.on("GET", "/queues/", pagedReply([]QueueItem{}, 0)) // every other queue has no hits
	f.on("GET", "/queues/q0/items", pagedReply([]QueueItem{exact, fuzzy, older}, 3))
	jobFor := func(id, itemID, status string, attempt int) JobRow {
		r := jobRowFixture()
		r.ID, r.QueueItemID, r.Status, r.AttemptNumber = id, sp(itemID), status, attempt
		return r
	}
	f.route("GET", "/jobs", func(c apexCall) reply {
		if queryOf(t, c).Get("reference") == "R2" {
			return pagedReply([]JobRow{jobFor("j-retry", "i-new", "failed", 2), jobFor("j-first", "i-new", "cancelled", 1), jobFor("j-other-item", "someone-else", "completed", 1)}, 3)
		}
		return pagedReply([]JobRow{jobFor("j-old", "i-old", "completed", 1)}, 1)
	})
}

func TestFindOrderSearchesEveryQueueKeepsOnlyTheExactOrderUIDNewestFirstTokenRedacted(t *testing.T) {
	f := newFakeApex(t, nil)
	routeFind(t, f, 8)
	out, err := Backend.FindOrderQueueItems(t.Context(), findArgs())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		calls := f.callsTo("GET", fmt.Sprintf("/queues/q%d/items", i))
		if len(calls) != 1 || queryOf(t, calls[0]).Get("search") != "order-1" {
			t.Errorf("queue q%d searched %d times, want once with search=order-1", i, len(calls))
		}
	}
	if got := ids(out.Items, func(i orchestrator.QueueItemCore) string { return i.ItemID }); !slices.Equal(got, []string{"i-new", "i-old"}) {
		t.Errorf("items = %v, want [i-new i-old]", got)
	}
	if out.Items[0].SpecificContent["token"] != "[redacted]" || out.Items[0].QueueName != "PMG auth submit queue" {
		t.Errorf("item 0 = %+v", out.Items[0])
	}
	if out.SearchMode != "search" || out.Scanned != 3 || len(out.Notes) != 0 || out.Notes == nil || out.Folder != "Authorization Test" {
		t.Errorf("result = %+v", out)
	}
}

func TestFindOrderJobsAreTheItemsOwnAttemptsFromTheReferenceListingNewestFirst(t *testing.T) {
	f := newFakeApex(t, nil)
	routeFind(t, f, 2)
	out, _ := Backend.FindOrderQueueItems(t.Context(), findArgs())
	var got []string
	for _, j := range out.Items[0].Jobs {
		got = append(got, *j.State+":"+j.ID)
	}
	if want := []string{"Faulted:j-retry", "Stopped:j-first"}; !slices.Equal(got, want) {
		t.Errorf("jobs = %v, want %v (the other item's job dropped)", got, want)
	}
}

func TestFindOrderFallsBackToTheItemsLatestJobWhenTheReferenceListingHasNoneOfItsJobs(t *testing.T) {
	f := newFakeApex(t, nil)
	routeFind(t, f, 2)
	f.on("GET", "/jobs", pagedReply([]JobRow{}, 0))
	out, _ := Backend.FindOrderQueueItems(t.Context(), findArgs())
	want := `[{"orchestrator":"apex","id":"` + jobID + `","state":null,"neverStarted":null}]`
	if got := jsonOf(t, out.Items[0].Jobs); got != want {
		t.Errorf("jobs = %s, want %s", got, want)
	}
}

func TestFindOrderOptionsIncludeContentTopAndSince(t *testing.T) {
	f := newFakeApex(t, nil)
	routeFind(t, f, 2)

	noContent := findArgs()
	noContent.IncludeSpecificContent = false
	out, _ := Backend.FindOrderQueueItems(t.Context(), noContent)
	for _, i := range out.Items {
		if i.SpecificContent != nil {
			t.Errorf("item %s carries content without includeSpecificContent", i.ItemID)
		}
	}

	top := findArgs()
	top.Top = 1
	out, _ = Backend.FindOrderQueueItems(t.Context(), top)
	if len(out.Items) != 1 || out.Items[0].ItemID != "i-new" {
		t.Errorf("top=1 items = %+v, want the newest", out.Items)
	}

	since := findArgs()
	since.Since = "2026-10-08T00:00:00Z"
	out, _ = Backend.FindOrderQueueItems(t.Context(), since)
	if len(out.Items) != 1 || out.Items[0].ItemID != "i-new" || len(out.Notes) != 0 {
		t.Errorf("bounded result = %+v", out)
	}
	since.Since = "2026-11-01T00:00:00Z"
	out, _ = Backend.FindOrderQueueItems(t.Context(), since)
	if len(out.Items) != 2 || len(out.Notes) != 1 || !strings.Contains(out.Notes[0], "without the time bound") {
		t.Errorf("empty-window result = %+v", out)
	}
}

func TestFindOrderWithNoExactMatchReturnsNoItemsStillCountsScannedAndLooksUpNoJobs(t *testing.T) {
	f := newFakeApex(t, nil)
	routeFind(t, f, 2)
	args := findArgs()
	args.OrderUID = "order-1x"
	out, err := Backend.FindOrderQueueItems(t.Context(), args)
	if err != nil || len(out.Items) != 0 || out.Items == nil || out.Scanned != 3 {
		t.Fatalf("result = %+v, %v", out, err)
	}
	if n := len(f.callsTo("GET", "/jobs")); n != 0 {
		t.Errorf("job requests = %d, want 0", n)
	}
}

// ---- PullQueueItem ----

func pullArgs() orchestrator.PullQueueItemArgs {
	return orchestrator.PullQueueItemArgs{Env: config.PreProd, QueueID: queueID, ItemID: itemID}
}

func TestPullForcesIsApprovedFalseKeepsTheTokenAndResolvesPortalAndFilename(t *testing.T) {
	f := newFakeApex(t, nil)
	q := queueFixture()
	q.Name = "PMG auth submit queue"
	f.on("GET", "/queues/"+queueID, jsonReply(q))
	f.on("GET", "/queues/"+queueID+"/items/"+itemID, jsonReply(QueueItemDetail{QueueItem: itemFixture()}))
	out, err := Backend.PullQueueItem(t.Context(), pullArgs())
	if err != nil {
		t.Fatal(err)
	}
	if out.Env != config.PreProd || !out.IsApprovedForced || out.SuggestedFilename != "jane-doe.yaml" || !out.Portal.Matched || out.Portal.Key != "PMG" ||
		out.Item.Status != "Failed" || out.Item.QueueName != "PMG auth submit queue" || *out.Item.OrderUID != "order-1" || out.QueueNameError != "" {
		t.Errorf("result = %+v", out)
	}
	if sc := out.Item.SpecificContent; sc["IsApproved"] != false || sc["token"] != "synthetic-token" {
		t.Errorf("content = %v, want IsApproved false and the token kept", sc)
	}
	if _, full := out.Item.Details.(ItemDetailsFull); !full {
		t.Errorf("details = %T, want the detail projection with lineage", out.Item.Details)
	}
}

func TestPullFailedQueueNameLookupIsQueueNameErrorNotAFailedPull(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/queues/"+queueID, jsonReply(map[string]any{"detail": "kaput"}, 500))
	f.on("GET", "/queues/"+queueID+"/items/"+itemID, jsonReply(QueueItemDetail{QueueItem: itemFixture()}))
	out, err := Backend.PullQueueItem(t.Context(), pullArgs())
	if err != nil || !strings.Contains(out.QueueNameError, "kaput") || out.Portal.Matched || out.Item.ItemID != itemID {
		t.Errorf("result = %+v, %v", out, err)
	}
}

func TestPullRequiresEnvQueueIDAndItemIDAndRejectsUiPathOnlyArgs(t *testing.T) {
	f := newFakeApex(t, nil)
	for name, mod := range map[string]func(*orchestrator.PullQueueItemArgs){
		"no queueId": func(a *orchestrator.PullQueueItemArgs) { a.QueueID = "" },
		"no env":     func(a *orchestrator.PullQueueItemArgs) { a.Env = "" },
		"url":        func(a *orchestrator.PullQueueItemArgs) { a.URL = "https://x/y" },
		"folderId":   func(a *orchestrator.PullQueueItemArgs) { a.FolderID = "f" },
	} {
		a := pullArgs()
		mod(&a)
		if _, err := Backend.PullQueueItem(t.Context(), a); !shared.IsExpected(err) {
			t.Errorf("%s: error = %v, want an expected refusal", name, err)
		}
	}
	if n := f.count(); n != 0 {
		t.Errorf("%d requests made, want 0", n)
	}
}
