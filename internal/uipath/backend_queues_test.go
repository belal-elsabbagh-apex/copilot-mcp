package uipath

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/reference"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const testOrderUID = "test-order-uid-1"

var pmg = reference.ResolvePortal("PMG")

// queueItemSelect is exactly what the tier-1 query's $select projects — pinned verbatim so a
// drive-by edit can't silently change the wire query.
const wantQueueItemSelect = "Id,Reference,Status,ExecutorJobKey,ProcessingExceptionType,RetryNumber,CreationTime,SpecificContent,QueueDefinitionId"

func queueItemRaw(over map[string]any) map[string]any {
	m := map[string]any{
		"Id": 1, "Reference": "REF-1", "Status": "New", "ExecutorJobKey": "", "ProcessingExceptionType": "", "RetryNumber": 0,
		"CreationTime":      "2026-08-26T00:00:00.000Z",
		"SpecificContent":   map[string]any{"orderUid": testOrderUID, "token": "secret-jwt", "MemberID": "m1", "MemberFullName": "Doe, Jane"},
		"QueueDefinitionId": pmg.SubmitQueueDefID,
		"QueueDefinition":   map[string]any{"Name": "PMG auth submit queue"},
	}
	for k, v := range over {
		m[k] = v
	}
	return m
}

func queueFake(t *testing.T, h http.HandlerFunc) *fake {
	t.Helper()
	f := newFake(t, h)
	f.useConfig(t, nil)
	return f
}

func TestListQueueItemsResolvesPortalKeysAndKinds(t *testing.T) {
	f := queueFake(t, answer(http.StatusOK, page(queueItemRaw(nil))))
	args := orchestrator.ListQueueItemsArgs{Env: config.Prod, Queue: "PMG", QueueKind: orchestrator.QueueSubmit, Top: 10}
	res, err := Backend.ListQueueItems(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("/odata/QueueItems")
	if !strings.Contains(calls[0].query("$filter"), "QueueDefinitionId eq "+strconv.Itoa(pmg.SubmitQueueDefID)) {
		t.Errorf("submit filter = %q", calls[0].query("$filter"))
	}
	if res.QueueID != strconv.Itoa(pmg.SubmitQueueDefID) || res.QueueName != "PMG" {
		t.Errorf("res = %+v", res)
	}
	args.Queue, args.QueueKind = "pmg", orchestrator.QueueSync
	if _, err := Backend.ListQueueItems(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if got := f.callsTo("/odata/QueueItems")[1].query("$filter"); !strings.Contains(got, "QueueDefinitionId eq "+strconv.Itoa(pmg.SyncQueueDefID)) {
		t.Errorf("sync filter = %q", got)
	}
}

func TestListQueueItemsNumericIDIsUsedAsIsAndNamedFromTheItem(t *testing.T) {
	f := queueFake(t, answer(http.StatusOK, page(queueItemRaw(map[string]any{"QueueDefinitionId": 999}))))
	res, err := Backend.ListQueueItems(t.Context(), orchestrator.ListQueueItemsArgs{Env: config.Prod, Queue: "999", QueueKind: orchestrator.QueueSubmit, Top: 10})
	if err != nil || res.QueueID != "999" || res.QueueName != "PMG" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if got := f.callsTo("/odata/QueueItems")[0].query("$filter"); !strings.Contains(got, "QueueDefinitionId eq 999") {
		t.Errorf("filter = %q", got)
	}
}

func TestListQueueItemsUnknownPortalIsAnExpectedErrorListingThePortals(t *testing.T) {
	f := queueFake(t, okEmpty)
	_, err := Backend.ListQueueItems(t.Context(), orchestrator.ListQueueItemsArgs{Env: config.Prod, Queue: "NOPE", QueueKind: orchestrator.QueueSubmit, Top: 10})
	if !shared.IsExpected(err) || !strings.Contains(err.Error(), "PMG") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("calls = %d", n)
	}
}

func TestListQueueItemsStatusFilterAndPHILightCores(t *testing.T) {
	f := queueFake(t, answer(http.StatusOK, page(queueItemRaw(map[string]any{"Status": "Failed", "RetryNumber": 2}))))
	res, err := Backend.ListQueueItems(t.Context(), orchestrator.ListQueueItemsArgs{
		Env: config.Prod, Queue: "PMG", QueueKind: orchestrator.QueueSubmit, Status: orchestrator.StatusFailed, Top: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.callsTo("/odata/QueueItems")[0].query("$filter"); !strings.Contains(got, "Status eq 'Failed'") {
		t.Errorf("filter = %q", got)
	}
	it := res.Items[0]
	if it.ItemID != "1" || it.Reference != "REF-1" || deref(it.OrderUID) != testOrderUID || it.Status != "Failed" || it.SpecificContent != nil {
		t.Errorf("item = %+v", it)
	}
	d := it.Details.(ItemDetails)
	if d.RetryNumber != 2 || d.MemberID != "m1" || d.Member != "Doe, Jane" {
		t.Errorf("details = %+v", d)
	}
	if strings.Contains(jsonOf(t, res), "secret-jwt") {
		t.Error("the token must not leak")
	}
}

var findBase = orchestrator.FindOrderArgs{Env: config.Prod, OrderUID: testOrderUID, Top: 50, IncludeSpecificContent: true}

func TestFindOrderTier1IsTimeBoundedAndProjected(t *testing.T) {
	f := queueFake(t, answer(http.StatusOK, page(queueItemRaw(nil))))
	a := findBase
	a.Since = "2026-08-25"
	if _, err := Backend.FindOrderQueueItems(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("/odata/QueueItems")
	if len(calls) != 1 || calls[0].query("$select") != wantQueueItemSelect {
		t.Fatalf("calls = %+v", calls)
	}
	filter := calls[0].query("$filter")
	if !strings.Contains(filter, "contains(SpecificData,") || !strings.Contains(filter, "CreationTime gt 2026-08-25T00:00:00.000Z") {
		t.Errorf("filter = %q", filter)
	}
}

func TestFindOrderSinceBoundedMissRetriesUnboundedAndSaysSo(t *testing.T) {
	f := queueFake(t, okEmpty)
	a := findBase
	a.Since = "2026-08-25"
	res, err := Backend.FindOrderQueueItems(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("/odata/QueueItems")
	if len(calls) != 2 || strings.Contains(calls[1].query("$filter"), "CreationTime") {
		t.Fatalf("calls = %+v", calls)
	}
	if !strings.Contains(strings.Join(res.Notes, "|"), "retried without the time bound") {
		t.Errorf("notes = %v", res.Notes)
	}
}

func TestFindOrderMapsItemsToCores(t *testing.T) {
	queueFake(t, answer(http.StatusOK, page(
		queueItemRaw(map[string]any{"Id": 1, "ExecutorJobKey": "k1", "Status": "Failed", "ProcessingExceptionType": "BusinessException"}),
		queueItemRaw(map[string]any{"Id": 2, "ExecutorJobKey": ""}),
	)))
	res, err := Backend.FindOrderQueueItems(t.Context(), findBase)
	if err != nil {
		t.Fatal(err)
	}
	if res.SearchMode != "contains" || res.Scanned != 2 {
		t.Fatalf("res = %+v", res)
	}
	i0, i1 := res.Items[0], res.Items[1]
	if jsonOf(t, i0.Jobs) != `[{"orchestrator":"uipath","id":"k1","state":null,"neverStarted":null}]` || deref(i0.ExceptionType) != "business" || i0.QueueName != "PMG" {
		t.Errorf("item 0 = %+v", i0)
	}
	if len(i1.Jobs) != 0 || i1.ExceptionType != nil {
		t.Errorf("item 1 = %+v", i1)
	}
}

func TestFindOrderRedactsTheTokenAndHonoursIncludeSpecificContent(t *testing.T) {
	queueFake(t, answer(http.StatusOK, page(queueItemRaw(map[string]any{
		"SpecificContent": map[string]any{"orderUid": testOrderUID, "MemberID": "m1", "token": "jwt.secret"},
	}))))
	res, _ := Backend.FindOrderQueueItems(t.Context(), findBase)
	if got := jsonOf(t, res.Items[0].SpecificContent); got != `{"MemberID":"m1","orderUid":"`+testOrderUID+`","token":"[redacted]"}` {
		t.Errorf("specificContent = %s", got)
	}
	if strings.Contains(jsonOf(t, res), "jwt.secret") {
		t.Error("the JWT leaked")
	}
	a := findBase
	a.IncludeSpecificContent = false
	res, _ = Backend.FindOrderQueueItems(t.Context(), a)
	if res.Items[0].SpecificContent != nil || deref(res.Items[0].OrderUID) != testOrderUID {
		t.Errorf("item = %+v", res.Items[0])
	}
}

func TestFindOrderFallsBackToARecentScanWhenTheFilterIsRejected(t *testing.T) {
	f := queueFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("$filter"), "SpecificData") {
			respond(w, http.StatusBadRequest, map[string]any{"message": "no"})
			return
		}
		respond(w, http.StatusOK, page(queueItemRaw(nil), queueItemRaw(map[string]any{"SpecificContent": map[string]any{"orderUid": "other"}})))
	})
	res, err := Backend.FindOrderQueueItems(t.Context(), findBase)
	if err != nil {
		t.Fatal(err)
	}
	if res.SearchMode != "recent-scan" || res.Scanned != 2 || len(res.Items) != 1 || !strings.Contains(res.SearchError, "400") || len(res.Notes) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if n := len(f.callsTo("/odata/QueueItems")); n != 3 {
		t.Fatalf("calls = %d, want tier 1, tier 1b, tier 2", n)
	}
}

func TestFindOrderRequeriesWithFullRowsWhenTheProjectionNullsSpecificContent(t *testing.T) {
	f := queueFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$select") != "" {
			respond(w, http.StatusOK, page(queueItemRaw(map[string]any{"SpecificContent": nil})))
			return
		}
		respond(w, http.StatusOK, page(queueItemRaw(nil)))
	})
	res, err := Backend.FindOrderQueueItems(t.Context(), findBase)
	if err != nil || len(res.Items) != 1 || len(res.Notes) != 1 || len(f.callsTo("/odata/QueueItems")) != 2 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

var pullSC = map[string]any{"orderUid": testOrderUID, "IsApproved": true, "MemberFirstName": "Jane", "MemberLastName": "Doe"}

func pullFake(t *testing.T, row map[string]any) *fake {
	t.Helper()
	return queueFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/odata/QueueItems("):
			respond(w, http.StatusOK, row)
		case strings.Contains(r.URL.Path, "/odata/QueueDefinitions("):
			respond(w, http.StatusOK, map[string]any{"Name": "PMG auth submit queue"})
		default:
			okEmpty(w, r)
		}
	})
}

func TestPullByItemIDForcesIsApprovedFalseAndResolvesThePortal(t *testing.T) {
	f := pullFake(t, queueItemRaw(map[string]any{"Id": 5, "SpecificContent": pullSC}))
	res, err := Backend.PullQueueItem(t.Context(), orchestrator.PullQueueItemArgs{Env: config.PreProd, ItemID: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Env != config.PreProd || !res.IsApprovedForced || res.Item.SpecificContent["IsApproved"] != false {
		t.Fatalf("res = %+v", res)
	}
	if res.Item.ItemID != "5" || res.Item.QueueName != "PMG" || deref(res.Item.OrderUID) != testOrderUID {
		t.Errorf("item = %+v", res.Item)
	}
	if !res.Portal.Matched || res.Portal.Key != "PMG" || res.Portal.BuildArtifact != "pmg-auth.cjs" || res.SuggestedFilename != "jane-doe.yaml" {
		t.Errorf("portal = %+v, filename = %q", res.Portal, res.SuggestedFilename)
	}
	if !strings.Contains(f.calls()[0].Path, "/odata/QueueItems(5)") {
		t.Errorf("path = %s", f.calls()[0].Path)
	}
	if pullSC["IsApproved"] != true {
		t.Error("the guard mutated the source content")
	}
}

func TestPullByURLDerivesEnvAndFolderFromTheFid(t *testing.T) {
	f := pullFake(t, queueItemRaw(map[string]any{"Id": 7, "SpecificContent": pullSC}))
	url := "https://cloud.uipath.com/myorg/mytenant/orchestrator_/queues/38592/transactions(sidepanel:sidepanel/transactions/7/details)?fid=434039"
	res, err := Backend.PullQueueItem(t.Context(), orchestrator.PullQueueItemArgs{URL: url})
	if err != nil || res.Env != config.PreProd || res.Item.ItemID != "7" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	c := f.calls()[0]
	if !strings.Contains(c.Path, "/odata/QueueItems(7)") || c.Header.Get("X-Uipath-Organizationunitid") != "434039" {
		t.Errorf("call = %s %v", c.Path, c.Header)
	}
}

func TestPullFallsBackToTheQueueDefinitionLookupForAnUnnamedItem(t *testing.T) {
	pullFake(t, queueItemRaw(map[string]any{"SpecificContent": pullSC, "QueueDefinition": nil, "Name": ""}))
	res, err := Backend.PullQueueItem(t.Context(), orchestrator.PullQueueItemArgs{Env: config.Prod, ItemID: "1"})
	if err != nil || res.Item.QueueName != "PMG" || !res.Portal.Matched || res.QueueNameError != "" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestPullQueueDefinitionLookupFailureSurfacesAsQueueNameError(t *testing.T) {
	queueFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/odata/QueueItems(") {
			respond(w, http.StatusOK, queueItemRaw(map[string]any{"SpecificContent": pullSC, "QueueDefinition": nil, "Name": ""}))
			return
		}
		respond(w, http.StatusInternalServerError, map[string]any{"message": "boom"})
	})
	res, err := Backend.PullQueueItem(t.Context(), orchestrator.PullQueueItemArgs{Env: config.Prod, ItemID: "1"})
	if err != nil || !strings.Contains(res.QueueNameError, "500") || res.Portal.Matched {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestPullBadInputsAreExpectedErrorsBeforeAnyRequest(t *testing.T) {
	f := queueFake(t, okEmpty)
	for name, args := range map[string]orchestrator.PullQueueItemArgs{
		"itemId without env":   {ItemID: "5"},
		"non-numeric itemId":   {Env: config.Prod, ItemID: "abc"},
		"neither url nor item": {Env: config.Prod},
		"non-positive item id": {Env: config.Prod, ItemID: "-3"},
	} {
		if _, err := Backend.PullQueueItem(t.Context(), args); !shared.IsExpected(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("calls = %d", n)
	}
}

func TestListQueuesProcessesAndTriggersMapToCores(t *testing.T) {
	queueFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/odata/QueueDefinitions") {
			respond(w, http.StatusOK, page(map[string]any{"Id": 3, "Name": "PMG auth submit queue", "Description": "d", "CreationTime": "t"}))
			return
		}
		respond(w, http.StatusOK, page(map[string]any{
			"Id": 8, "Key": "release-guid", "Name": "PMG auth submit", "ProcessKey": "pkg", "ProcessVersion": "1.2.3", "IsLatestVersion": true,
		}))
	})
	queues, err := Backend.ListQueues(t.Context(), orchestrator.ListQueuesArgs{Env: config.Prod, Top: 10})
	if err != nil || queues[0].ID != "3" || queues[0].Name != "PMG auth submit queue" {
		t.Fatalf("queues = %+v, err = %v", queues, err)
	}
	procs, err := Backend.ListProcesses(t.Context(), orchestrator.ListProcessesArgs{Env: config.Prod, Top: 10})
	if err != nil || procs[0].ID != "release-guid" || deref(procs[0].Package) != "pkg" || deref(procs[0].Version) != "1.2.3" {
		t.Fatalf("procs = %+v, err = %v", procs, err)
	}
}

func TestListTriggersQueueFilterKeepsOnlyTriggersWatchingThatQueue(t *testing.T) {
	row := func(id, queueDefID int) map[string]any {
		return map[string]any{
			"Id": id, "Name": "t" + strconv.Itoa(id), "Enabled": true, "ReleaseId": 8, "ReleaseKey": "release-guid", "ReleaseName": "PMG auth submit",
			"QueueDefinitionId": queueDefID, "QueueDefinitionName": "", "ItemsActivationThreshold": 1, "MaxJobsForActivation": 1,
		}
	}
	queueFake(t, answer(http.StatusOK, page(row(1, 77), row(2, 88), row(3, 0))))
	all, err := Backend.ListTriggers(t.Context(), orchestrator.ListTriggersArgs{Env: config.Prod, Top: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, tr := range all {
		got += tr.ID + ":" + string(tr.Kind) + " "
	}
	if got != "1:queue 2:queue 3:time " {
		t.Fatalf("triggers = %s", got)
	}
	only, _ := Backend.ListTriggers(t.Context(), orchestrator.ListTriggersArgs{Env: config.Prod, Top: 10, QueueID: "77"})
	if len(only) != 1 || only[0].ID != "1" {
		t.Errorf("queueId 77 = %+v", only)
	}
	none, _ := Backend.ListTriggers(t.Context(), orchestrator.ListTriggersArgs{Env: config.Prod, Top: 10, QueueID: "0"})
	if len(none) != 0 {
		t.Errorf("a time trigger must never match queueId 0: %+v", none)
	}
}
