package apex

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const (
	actQueueID    = "aaaaaaaa-1111-4111-8111-111111111111"
	actItemID     = "bbbbbbbb-2222-4222-8222-222222222222"
	actJobID      = "cccccccc-3333-4333-8333-333333333333"
	actWorkflowID = "dddddddd-4444-4444-8444-444444444444"
)

var testFolderID = folderIDs["Authorization Test"]

func actQueue() map[string]any {
	return map[string]any{"id": actQueueID, "folder_id": testFolderID, "name": "CENCAL auth submit queue", "workflow_id": actWorkflowID, "is_active": false}
}

func mustRefuse(t *testing.T, err error, wantSub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), wantSub) {
		t.Fatalf("error = %v, want it to contain %q", err, wantSub)
	}
}

func addArgs() orchestrator.AddQueueItemArgs {
	return orchestrator.AddQueueItemArgs{
		Env: config.PreProd, Queue: "cencal auth submit queue", Reference: "ord-1-1700000000", Priority: orchestrator.PriorityNormal,
		SpecificContent: map[string]any{
			"orderUid": "ord-1", "automationId": "auto-1", "IsApproved": true, "serverURL": testServerURL, "queueUrl": testQueueURL,
			"Units": []any{"1"}, "callbackContext": map[string]any{"physicianId": 7},
		},
	}
}

func stubActQueue(f *fakeApex) {
	f.on("GET", "/queues?", pagedReply([]any{actQueue()}, 1))
	f.on("POST", "/queues/"+actQueueID+"/items", jsonReply(map[string]any{"id": actItemID, "status": "new"}, 201))
}

func TestAddQueueItemRefusesProdInTheDomainBeforeAnyRequest(t *testing.T) {
	f := newFakeApex(t, nil)
	args := addArgs()
	args.Env = config.Prod
	_, err := Backend.AddQueueItem(t.Context(), args)
	mustRefuse(t, err, "pre_prod-only")
	if n := f.count(); n != 0 {
		t.Errorf("%d requests made, want 0", n)
	}
}

func TestAddQueueItemGuardViolationBlocksEverything(t *testing.T) {
	cases := map[string]struct {
		patch map[string]any
		want  string
	}{
		"a serverURL that is not the configured pre-prod host": {map[string]any{"serverURL": "https://prod-be.example.com"}, "serverURL"},
		"a queueUrl that is not the configured one":            {map[string]any{"queueUrl": "https://sqs.example/other"}, "queueUrl"},
		"a <TO-FILL> placeholder":                              {map[string]any{"Facility": "<TO-FILL>"}, "placeholder"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeApex(t, nil)
			args := addArgs()
			for k, v := range tc.patch {
				args.SpecificContent[k] = v
			}
			_, err := Backend.AddQueueItem(t.Context(), args)
			mustRefuse(t, err, tc.want)
			if n := f.count(); n != 0 {
				t.Errorf("%d requests made, want 0", n)
			}
		})
	}
}

func TestAddQueueItemRefusesContentWithoutAUsableAutomationIDBeforeAnyRequest(t *testing.T) {
	for name, patch := range map[string]func(map[string]any){
		"absent": func(c map[string]any) { delete(c, "automationId") },
		"empty":  func(c map[string]any) { c["automationId"] = "" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeApex(t, nil)
			args := addArgs()
			patch(args.SpecificContent)
			_, err := Backend.AddQueueItem(t.Context(), args)
			if !shared.IsExpected(err) {
				t.Errorf("error = %v, want an expected error", err)
			}
			if n := f.count(); n != 0 {
				t.Errorf("%d requests made, want 0", n)
			}
		})
	}
}

func TestAddQueueItemPostsGuardedNativeContentWithMatchValueInTheTestFolder(t *testing.T) {
	f := newFakeApex(t, nil)
	stubActQueue(f)
	got, err := Backend.AddQueueItem(t.Context(), addArgs())
	if err != nil {
		t.Fatal(err)
	}
	writes := f.writes()
	if len(writes) != 1 || writes[0].Path != "/queues/"+actQueueID+"/items" || writes[0].Header.Get("X-Folder-Id") != testFolderID {
		t.Fatalf("writes = %+v, want one POST of the item into the test folder", writes)
	}
	body, _ := writes[0].Body.(map[string]any)
	content, _ := body["specific_content"].(map[string]any)
	if content["IsApproved"] != false || content["automationId"] != "auto-1" || body["reference"] != "ord-1-1700000000" || body["match_value"] != "auto-1" {
		t.Errorf("posted body = %v, want IsApproved forced false and match_value = automationId", body)
	}
	if _, isArray := content["Units"].([]any); !isArray {
		t.Errorf("Units = %T, want the native array kept", content["Units"])
	}
	want := orchestrator.AddQueueItemResult{
		Env: config.PreProd, QueueID: actQueueID, QueueName: "CENCAL auth submit queue", Reference: "ord-1-1700000000",
		ItemID: actItemID, Status: "New", Forced: []string{"IsApproved"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

func TestAddQueueItemResolvesTheQueueByUUIDCaseInsensitively(t *testing.T) {
	f := newFakeApex(t, nil)
	stubActQueue(f)
	args := addArgs()
	args.Queue = strings.ToUpper(actQueueID)
	got, err := Backend.AddQueueItem(t.Context(), args)
	if err != nil || got.QueueID != actQueueID {
		t.Fatalf("AddQueueItem = %+v, %v, want the queue resolved by uuid", got, err)
	}
}

func TestAddQueueItemUnknownOrAmbiguousQueuePostsNothing(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/queues?", pagedReply([]any{actQueue()}, 1))
	args := addArgs()
	args.Queue = "Nope"
	_, err := Backend.AddQueueItem(t.Context(), args)
	mustRefuse(t, err, "no queue")

	dup := actQueue()
	dup["id"], dup["name"] = "eeeeeeee-5555-4555-8555-555555555555", "Cencal Auth Submit Queue"
	f.on("GET", "/queues?", pagedReply([]any{actQueue(), dup}, 2))
	_, err = Backend.AddQueueItem(t.Context(), addArgs())
	mustRefuse(t, err, "matches 2 queues")
	if n := len(f.writes()); n != 0 {
		t.Errorf("%d writes, want 0", n)
	}
}

func TestAddQueueItemWithoutPreProdServerURLFailsClosedNamingTheConfigKey(t *testing.T) {
	f := newFakeApex(t, func(s map[string]any) map[string]any {
		delete(s, "serverUrlByEnv")
		return s
	})
	args := addArgs()
	args.SpecificContent = map[string]any{"automationId": "auto-1", "serverURL": testServerURL}
	_, err := Backend.AddQueueItem(t.Context(), args)
	mustRefuse(t, err, "apex.serverUrlByEnv.pre_prod")
	if n := f.count(); n != 0 {
		t.Errorf("%d requests made, want 0", n)
	}
}

// ---- DeleteQueueItem ----

func delArgs() orchestrator.DeleteQueueItemArgs {
	return orchestrator.DeleteQueueItemArgs{Env: config.PreProd, ItemID: actItemID, QueueID: actQueueID}
}

var itemPath = "/queues/" + actQueueID + "/items/" + actItemID

func TestDeleteQueueItemRefusalsBeforeAnyRequest(t *testing.T) {
	f := newFakeApex(t, nil)
	prod := delArgs()
	prod.Env = config.Prod
	_, err := Backend.DeleteQueueItem(t.Context(), prod)
	mustRefuse(t, err, "pre_prod-only")
	noQueue := delArgs()
	noQueue.QueueID = ""
	_, err = Backend.DeleteQueueItem(t.Context(), noQueue)
	mustRefuse(t, err, "queueId is required")
	if n := f.count(); n != 0 {
		t.Errorf("%d requests made, want 0", n)
	}
}

func TestDeleteQueueItemRefusesEveryStatusButNewAndPostsNothing(t *testing.T) {
	for status, label := range map[string]string{
		"in_progress": "InProgress", "successful": "Successful", "failed": "Failed",
		"retried": "Retried", "abandoned": "Abandoned", "deleted": "Deleted",
	} {
		t.Run(status, func(t *testing.T) {
			f := newFakeApex(t, nil)
			f.on("GET", "/queues?", pagedReply([]any{actQueue()}, 1))
			f.on("GET", itemPath, jsonReply(map[string]any{"id": actItemID, "status": status, "reference": "ref-1"}))
			_, err := Backend.DeleteQueueItem(t.Context(), delArgs())
			if !shared.IsExpected(err) {
				t.Errorf("error = %v, want an expected refusal", err)
			}
			mustRefuse(t, err, label)
			if n := len(f.writes()); n != 0 {
				t.Errorf("%d writes, want 0", n)
			}
		})
	}
}

func TestDeleteQueueItemFetchesFirstThenPostsDelete(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/queues?", pagedReply([]any{actQueue()}, 1))
	f.on("GET", itemPath, jsonReply(map[string]any{"id": actItemID, "status": "new", "reference": "ref-1"}))
	f.on("POST", itemPath+":delete", jsonReply(map[string]any{}))
	got, err := Backend.DeleteQueueItem(t.Context(), delArgs())
	want := orchestrator.DeleteQueueItemResult{Env: config.PreProd, ItemID: actItemID, Deleted: true, PreviousStatus: "New", Reference: "ref-1"}
	if err != nil || got != want {
		t.Fatalf("DeleteQueueItem = %+v, %v, want %+v", got, err, want)
	}
	wantSeq := "GET /queues|GET " + itemPath + "|POST " + itemPath + ":delete"
	if seq := strings.Join(f.sequence(), "|"); seq != wantSeq {
		t.Errorf("sequence = %s, want %s", seq, wantSeq)
	}
	w := f.writes()[0]
	if body, ok := w.Body.(map[string]any); !ok || len(body) != 0 || w.Header.Get("X-Folder-Id") != testFolderID {
		t.Errorf("write = %+v, want an empty JSON object into the test folder", w)
	}
}

func TestDeleteQueueItemNeverDeletesAnItemThatCannotBeFetched(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/queues?", pagedReply([]any{actQueue()}, 1)) // the item GET answers 404
	if _, err := Backend.DeleteQueueItem(t.Context(), delArgs()); err == nil {
		t.Fatal("DeleteQueueItem succeeded without a fetchable item")
	}
	if n := len(f.writes()); n != 0 {
		t.Errorf("%d writes, want 0", n)
	}
}

func TestDeleteQueueItemRefusesAQueueIDOutsideThePreProdFolder(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/queues?", pagedReply([]any{}, 0))
	_, err := Backend.DeleteQueueItem(t.Context(), delArgs())
	mustRefuse(t, err, "pre_prod folder")
	for _, c := range f.actionCalls() {
		if strings.HasPrefix(c.Path, "/queues/") {
			t.Errorf("unexpected by-id request %s %s after the folder proof failed", c.Method, c.Path)
		}
	}
}

// ---- StartJob ----

func startArgs() orchestrator.StartJobArgs {
	return orchestrator.StartJobArgs{Env: config.PreProd, ProcessID: actWorkflowID, Input: map[string]any{"dryRun": true}, JobsCount: 3, MatchValue: "auto-1"}
}

func stubWorkflows(f *fakeApex) {
	f.on("GET", "/workflows?", pagedReply([]any{map[string]any{"id": actWorkflowID, "name": "apex-auth-submit-cencal"}}, 1))
}

func TestStartJobRefusalsBeforeAnyRequest(t *testing.T) {
	f := newFakeApex(t, nil)
	prod := startArgs()
	prod.Env = config.Prod
	_, err := Backend.StartJob(t.Context(), prod)
	mustRefuse(t, err, "pre_prod-only")
	for _, mv := range []string{"", "  "} {
		a := startArgs()
		a.MatchValue = mv
		_, err = Backend.StartJob(t.Context(), a)
		mustRefuse(t, err, "matchValue is required")
	}
	if n := f.count(); n != 0 {
		t.Errorf("%d requests made, want 0", n)
	}
}

func TestStartJobUnknownProcessPostsNothing(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/workflows?", pagedReply([]any{}, 0))
	_, err := Backend.StartJob(t.Context(), startArgs())
	mustRefuse(t, err, "no Process")
	if n := len(f.writes()); n != 0 {
		t.Errorf("%d writes, want 0", n)
	}
}

func TestStartJobPostsOneSequentialJobPerCountWithTheDocumentedBody(t *testing.T) {
	f := newFakeApex(t, nil)
	stubWorkflows(f)
	var mu sync.Mutex
	n := 0
	f.route("POST", "/jobs", func(apexCall) reply {
		mu.Lock()
		defer mu.Unlock()
		n++
		return jsonReply(map[string]any{"id": fmt.Sprintf("aaaaaaaa-0000-4000-8000-00000000000%d", n), "status": "queued"}, 201)
	})
	got, err := Backend.StartJob(t.Context(), startArgs())
	if err != nil {
		t.Fatal(err)
	}
	writes := f.writes()
	if len(writes) != 3 {
		t.Fatalf("writes = %d, want 3", len(writes))
	}
	for _, w := range writes {
		body, _ := w.Body.(map[string]any)
		input, _ := body["input_payload"].(map[string]any)
		bucket, hasBucket := body["bucket_id"]
		if w.Path != "/jobs" || w.Header.Get("X-Folder-Id") != testFolderID || body["workflow_id"] != actWorkflowID ||
			body["folder_id"] != testFolderID || body["match_value"] != "auto-1" || input["dryRun"] != true || !hasBucket || bucket != nil {
			t.Errorf("write = %+v, want the documented start body", w)
		}
	}
	first := orchestrator.StartedJob{ID: "aaaaaaaa-0000-4000-8000-000000000001", State: "Pending", ProcessName: "apex-auth-submit-cencal", Link: f.URL + "/jobs/aaaaaaaa-0000-4000-8000-000000000001"}
	if got.Env != config.PreProd || len(got.Jobs) != 3 || got.Jobs[0] != first || got.Jobs[2].ID != "aaaaaaaa-0000-4000-8000-000000000003" {
		t.Errorf("result = %+v, want 3 started jobs, first %+v", got, first)
	}
}

func TestStartJobFailedPostIsNeverRetriedAndAlreadyStartedJobsAreReported(t *testing.T) {
	f := newFakeApex(t, nil)
	stubWorkflows(f)
	var mu sync.Mutex
	n := 0
	f.route("POST", "/jobs", func(apexCall) reply {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return jsonReply(map[string]any{"id": actJobID, "status": "queued"}, 201)
		}
		return jsonReply(map[string]any{"detail": "boom"}, 500)
	})
	_, err := Backend.StartJob(t.Context(), startArgs())
	if !shared.IsExpected(err) {
		t.Errorf("error = %v, want an expected error", err)
	}
	mustRefuse(t, err, actJobID)
	mustRefuse(t, err, "boom")
	if got := len(f.writes()); got != 2 { // 1 ok + 1 failed: no retry, third never attempted
		t.Errorf("writes = %d, want 2", got)
	}
}

func TestStartJobFailureOfTheFirstPostIsNotRetriedEither(t *testing.T) {
	f := newFakeApex(t, nil)
	stubWorkflows(f)
	f.on("POST", "/jobs", jsonReply(map[string]any{"detail": "boom"}, 500))
	_, err := Backend.StartJob(t.Context(), startArgs())
	mustRefuse(t, err, "boom")
	if got := len(f.writes()); got != 1 {
		t.Errorf("writes = %d, want 1", got)
	}
}

// ---- StopJob ----

var jobPath = "/jobs/" + actJobID

func stopJob(ctx context.Context, strategy orchestrator.StopStrategy) (orchestrator.StopJobResult, error) {
	return Backend.StopJob(ctx, orchestrator.StopJobArgs{Env: config.PreProd, JobID: actJobID, Strategy: strategy})
}

func TestStopJobRefusesProdInTheDomainBeforeAnyRequest(t *testing.T) {
	f := newFakeApex(t, nil)
	_, err := Backend.StopJob(t.Context(), orchestrator.StopJobArgs{Env: config.Prod, JobID: actJobID, Strategy: orchestrator.StopKill})
	mustRefuse(t, err, "pre_prod-only")
	if n := f.count(); n != 0 {
		t.Errorf("%d requests made, want 0", n)
	}
}

func TestStopJobRefusesEveryTerminalStateNamingItAndPostsNothing(t *testing.T) {
	for status, label := range map[string]string{
		"completed": "Successful", "failed": "Faulted", "cancelled": "Stopped", "lost": "Lost", "timed_out": "Timed Out",
	} {
		t.Run(status, func(t *testing.T) {
			f := newFakeApex(t, nil)
			f.on("GET", jobPath, jsonReply(map[string]any{"id": actJobID, "status": status, "folder_id": testFolderID}))
			_, err := stopJob(t.Context(), orchestrator.StopKill)
			if !shared.IsExpected(err) {
				t.Errorf("error = %v, want an expected refusal", err)
			}
			mustRefuse(t, err, label)
			if n := len(f.writes()); n != 0 {
				t.Errorf("%d writes, want 0", n)
			}
		})
	}
}

func TestStopJobSoftStopPostsCancelWithForceFalse(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", jobPath, jsonReply(map[string]any{"id": actJobID, "status": "running", "folder_id": testFolderID}))
	f.on("POST", jobPath+"/cancel", jsonReply(map[string]any{}))
	got, err := stopJob(t.Context(), orchestrator.StopSoft)
	if want := (orchestrator.StopJobResult{Env: config.PreProd, JobID: actJobID, Strategy: orchestrator.StopSoft, Stopped: true}); err != nil || got != want {
		t.Fatalf("StopJob = %+v, %v, want %+v", got, err, want)
	}
	if seq := strings.Join(f.sequence(), "|"); seq != "GET "+jobPath+"|POST "+jobPath+"/cancel" {
		t.Errorf("sequence = %s", seq)
	}
	w := f.writes()[0]
	if body, _ := w.Body.(map[string]any); body["force"] != false || w.Header.Get("X-Folder-Id") != testFolderID {
		t.Errorf("write = %+v, want {force:false} into the test folder", w)
	}
}

func TestStopJobKillPostsForceCancelWithAnEmptyBody(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", jobPath, jsonReply(map[string]any{"id": actJobID, "status": "cancelling", "folder_id": testFolderID}))
	f.on("POST", jobPath+"/force_cancel", jsonReply(map[string]any{}))
	got, err := stopJob(t.Context(), orchestrator.StopKill)
	if err != nil || got.Strategy != orchestrator.StopKill {
		t.Fatalf("StopJob = %+v, %v", got, err)
	}
	w := f.writes()[0]
	if body, ok := w.Body.(map[string]any); w.Path != jobPath+"/force_cancel" || !ok || len(body) != 0 {
		t.Errorf("write = %+v, want force_cancel with {}", w)
	}
}

func TestStopJobKill403BecomesAnExpectedErrorNamingTheMissingRole(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", jobPath, jsonReply(map[string]any{"id": actJobID, "status": "running", "folder_id": testFolderID}))
	f.on("POST", jobPath+"/force_cancel", jsonReply(map[string]any{"detail": "forbidden"}, 403))
	_, err := stopJob(t.Context(), orchestrator.StopKill)
	if !shared.IsExpected(err) {
		t.Errorf("error = %v, want expected", err)
	}
	mustRefuse(t, err, "jobs:force_cancel")
	if n := len(f.writes()); n != 1 {
		t.Errorf("writes = %d, want 1", n)
	}
}

func TestStopJobNeverCancelsAJobThatCannotBeFetched(t *testing.T) {
	f := newFakeApex(t, nil)
	if _, err := stopJob(t.Context(), orchestrator.StopSoft); err == nil {
		t.Fatal("StopJob succeeded without a fetchable job")
	}
	if n := len(f.writes()); n != 0 {
		t.Errorf("%d writes, want 0", n)
	}
}

func TestStopJobRefusesAJobOutsideThePreProdFolder(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", jobPath, jsonReply(map[string]any{"id": actJobID, "status": "running", "folder_id": folderIDs["Authorization"]}))
	_, err := stopJob(t.Context(), orchestrator.StopKill)
	if !shared.IsExpected(err) {
		t.Errorf("error = %v, want expected", err)
	}
	mustRefuse(t, err, "Authorization Test")
	if n := len(f.writes()); n != 0 {
		t.Errorf("%d writes, want 0", n)
	}
}
