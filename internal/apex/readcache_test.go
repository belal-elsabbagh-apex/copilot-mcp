package apex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

// logDownloads, listings and detailReads count the upstream calls of one job.
func logDownloads(f *fakeApex) int { return len(f.callsTo("GET", artRoute(logArtifact()))) }

func listings(f *fakeApex) int {
	n := 0
	for _, c := range f.callsTo("GET", "/jobs/"+jobID+"/artifacts") {
		if c.route() == "GET /jobs/"+jobID+"/artifacts" {
			n++
		}
	}
	return n
}

func detailReads(f *fakeApex) int {
	n := 0
	for _, c := range f.callsTo("GET", "/jobs/"+jobID) {
		if c.route() == "GET /jobs/"+jobID {
			n++
		}
	}
	return n
}

func endedAgo(d time.Duration) func(*JobDetail) {
	return func(j *JobDetail) { j.CompletedAt = sp(time.Now().Add(-d).UTC().Format(time.RFC3339Nano)) }
}

func readLogs(t *testing.T) orchestrator.JobLogsEntry {
	t.Helper()
	out, err := Backend.GetJobLogs(t.Context(), logsArgs(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return out.Entries[0]
}

func TestSettledTerminalJobsLogsAreReadFromUpstreamOnce(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(endedAgo(time.Hour)), []Artifact{logArtifact()}, backendLog)
	first := readLogs(t)
	second := readLogs(t)
	if logDownloads(f) != 1 || listings(f) != 1 {
		t.Errorf("log downloads = %d, listings = %d, want 1 each", logDownloads(f), listings(f))
	}
	if detailReads(f) != 2 {
		t.Errorf("job detail reads = %d, want 2 (the detail is live every call)", detailReads(f))
	}
	if first.Returned != second.Returned || second.Returned != 7 {
		t.Errorf("cached read returned %d rows, first read %d", second.Returned, first.Returned)
	}
}

func TestCachedLogServesEveryFilterAndTheDigestAndTheFaultIssue(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(endedAgo(time.Hour)), []Artifact{logArtifact()}, backendLog)
	f.on("GET", "/jobs?", pagedReply([]JobRow{}, 0)) // the fault issue lists the item's attempts
	readLogs(t)
	args := logsArgs()
	args.Filter.Contains = "row not found"
	if out, _ := Backend.GetJobLogs(t.Context(), args, nil); out.Entries[0].Returned != 1 {
		t.Errorf("filtered read = %+v", out.Entries[0])
	}
	if _, err := Backend.GetJobs(t.Context(), orchestrator.GetJobsArgs{Env: config.PreProd, JobIDs: []string{jobID}, IncludeLogDigest: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Backend.BuildFaultedJobIssue(t.Context(), orchestrator.FaultIssueArgs{Env: config.PreProd, JobID: jobID}); err != nil {
		t.Fatal(err)
	}
	if logDownloads(f) != 1 || listings(f) != 1 {
		t.Errorf("log downloads = %d, listings = %d, want 1 each across logs, digest and fault issue", logDownloads(f), listings(f))
	}
}

func TestJobWithinTheSettleWindowIsNeverCached(t *testing.T) {
	cases := map[string]func(*JobDetail){
		"failed 1 minute ago":      func(j *JobDetail) { endedAgo(time.Minute)(j) },
		"cancelled 10 minutes ago": func(j *JobDetail) { j.Status = "cancelled"; endedAgo(10 * time.Minute)(j) },
		"lost 10 minutes ago":      func(j *JobDetail) { j.Status = "lost"; endedAgo(10 * time.Minute)(j) },
		"no completed_at":          func(j *JobDetail) { j.CompletedAt = nil },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeApex(t, nil)
			routeJob(f, failedDetailFixture(mod), []Artifact{logArtifact()}, backendLog)
			readLogs(t)
			readLogs(t)
			if logDownloads(f) != 2 || listings(f) != 2 {
				t.Errorf("log downloads = %d, listings = %d, want 2 each (nothing cached)", logDownloads(f), listings(f))
			}
		})
	}
}

func TestCancelledJobIsCachedOnlyAfterTheLongerWindow(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(func(j *JobDetail) { j.Status = "cancelled"; endedAgo(20 * time.Minute)(j) }), []Artifact{logArtifact()}, backendLog)
	readLogs(t)
	readLogs(t)
	if logDownloads(f) != 1 {
		t.Errorf("log downloads = %d, want 1", logDownloads(f))
	}
}

func TestRunningJobIsNeverCached(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(func(j *JobDetail) { j.Status = "running"; j.CompletedAt = nil }), []Artifact{logArtifact()}, backendLog)
	readLogs(t)
	readLogs(t)
	if logDownloads(f) != 0 || listings(f) != 0 {
		t.Errorf("a running job made %d downloads and %d listings, want none", logDownloads(f), listings(f))
	}
}

func TestFailedReadsAreNeverCached(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(endedAgo(time.Hour)), []Artifact{logArtifact()}, backendLog)
	var mu sync.Mutex
	fail := true
	f.route("GET", artRoute(logArtifact()), func(apexCall) reply {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return jsonReply(map[string]any{"detail": "boom"}, 500)
		}
		return reply{status: 200, raw: []byte(backendLog)}
	})
	if _, err := Backend.GetJobLogs(t.Context(), logsArgs(), nil); err == nil {
		t.Fatal("want the download error")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	readLogs(t) // the error left nothing behind: this fetches and stores
	readLogs(t) // this one hits
	if got := logDownloads(f); got != 2 {
		t.Errorf("log downloads = %d, want 2 (error, success; then cached)", got)
	}
}

func TestEntriesAreScopedToTheFolder(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(endedAgo(time.Hour)), []Artifact{logArtifact()}, backendLog)
	readLogs(t)
	args := logsArgs()
	args.Folder = "Authorization"
	if _, err := Backend.GetJobLogs(t.Context(), args, nil); err != nil {
		t.Fatal(err)
	}
	if logDownloads(f) != 2 {
		t.Errorf("log downloads = %d, want 2: a job read under another folder must not hit the first folder's entry", logDownloads(f))
	}
}

func TestReloadFlushesTheReadCache(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(endedAgo(time.Hour)), []Artifact{logArtifact()}, backendLog)
	readLogs(t)
	if readCache.Len() == 0 {
		t.Fatal("nothing was cached")
	}
	ResetClient() // what config.OnReload calls
	if readCache.Len() != 0 {
		t.Fatal("reset left entries behind")
	}
	readLogs(t)
	if logDownloads(f) != 2 {
		t.Errorf("log downloads = %d, want 2 after the flush", logDownloads(f))
	}
}

func TestWritePathStillHitsUpstreamAfterTheSameJobWasCachedByARead(t *testing.T) {
	f := newFakeApex(t, nil)
	routeJob(f, failedDetailFixture(endedAgo(time.Hour)), []Artifact{logArtifact()}, backendLog)
	readLogs(t)
	readLogs(t)
	before := detailReads(f)
	_, _ = Backend.StopJob(t.Context(), orchestrator.StopJobArgs{Env: config.PreProd, JobID: jobID, Strategy: orchestrator.StopSoft})
	if got := detailReads(f) - before; got != 1 {
		t.Errorf("stop_job fetched the job %d times, want 1 live fetch-first read", got)
	}
}

func TestWritePathStillListsQueuesUnderAQueueMemoCtx(t *testing.T) {
	f := newFakeApex(t, nil)
	stubActQueue(f)
	f.on("GET", "/queues/"+actQueueID+"/items", pagedReply([]QueueItem{}, 0))
	queueListings := func() int {
		n := 0
		for _, c := range f.callsTo("GET", "/queues") {
			if c.route() == "GET /queues" {
				n++
			}
		}
		return n
	}
	ctx := WithQueueListMemo(t.Context())
	for range 3 {
		if _, err := Backend.FindOrderQueueItems(ctx, findArgs()); err != nil {
			t.Fatal(err)
		}
	}
	if n := queueListings(); n != 1 {
		t.Fatalf("queue listings after 3 finds = %d, want 1", n)
	}
	if _, err := Backend.AddQueueItem(ctx, addArgs()); err != nil {
		t.Fatal(err)
	}
	if n := queueListings(); n != 2 {
		t.Errorf("queue listings after the write = %d, want 2: resolveQueue must list live", n)
	}
}

func TestQueueListMemoIsPerContextAndNeverRetainsErrors(t *testing.T) {
	f := newFakeApex(t, nil)
	var mu sync.Mutex
	calls, failing := 0, true
	f.route("GET", "/queues", func(apexCall) reply {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if failing {
			return jsonReply(map[string]any{"detail": "boom"}, 500)
		}
		return pagedReply([]Queue{queueNamedID("q1", "Q1")}, 1)
	})
	f.on("GET", "/queues/q1/items", pagedReply([]QueueItem{}, 0))
	ctx := WithQueueListMemo(t.Context())
	if _, err := Backend.FindOrderQueueItems(ctx, findArgs()); err == nil {
		t.Fatal("want the listing error")
	}
	mu.Lock()
	failing = false
	mu.Unlock()
	for range 2 {
		if _, err := Backend.FindOrderQueueItems(ctx, findArgs()); err != nil {
			t.Fatal(err)
		}
	}
	// A different ctx has its own memo; a plain ctx has none.
	if _, err := Backend.FindOrderQueueItems(WithQueueListMemo(t.Context()), findArgs()); err != nil {
		t.Fatal(err)
	}
	if _, err := Backend.FindOrderQueueItems(t.Context(), findArgs()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 4 { // error, ok (memoized), ok (new ctx), ok (no memo)
		t.Errorf("queue listings = %d, want 4", calls)
	}
}

func TestConcurrentFindsUnderOneMemoShareOneListing(t *testing.T) {
	f := newFakeApex(t, nil)
	routeFind(t, f, 3)
	ctx := WithQueueListMemo(t.Context())
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() { _, _ = Backend.FindOrderQueueItems(ctx, findArgs()) })
	}
	wg.Wait()
	n := 0
	for _, c := range f.callsTo("GET", "/queues") {
		if c.route() == "GET /queues" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("queue listings = %d, want 1 for 6 concurrent finds", n)
	}
}

func TestListTriggersFetchesQueuesAndSchedulesConcurrently(t *testing.T) {
	f := newFakeApex(t, nil)
	arrived := make(chan string, 2)
	release := make(chan struct{})
	gate := func(name string, body reply) handler {
		return func(apexCall) reply {
			arrived <- name
			<-release
			return body
		}
	}
	f.route("GET", "/queues", gate("queues", pagedReply([]Queue{queueNamedID("q1", "Q1")}, 1)))
	f.route("GET", "/schedules", gate("schedules", pagedReply([]Schedule{}, 0)))
	done := make(chan error, 1)
	go func() {
		_, err := Backend.ListTriggers(t.Context(), orchestrator.ListTriggersArgs{Env: config.PreProd, Top: 10})
		done <- err
	}()
	got := map[string]bool{}
	for range 2 {
		select {
		case n := <-arrived:
			got[n] = true
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatalf("only %v arrived while the other was held: the fetches are sequential", got)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// ---- apexPages ----

func TestApexPagesFetchesLaterPagesConcurrentlyAndReturnsThemInPageOrder(t *testing.T) {
	f := newFakeApex(t, nil)
	var mu sync.Mutex
	inFlight, peak := 0, 0
	f.route("GET", "/jobs", func(c apexCall) reply {
		n := 1
		if p := param(t, c, "page"); p != "" {
			n = int(p[0] - '0')
		}
		if n > 1 {
			mu.Lock()
			inFlight++
			peak = max(peak, inFlight)
			mu.Unlock()
			time.Sleep(time.Duration(6-n) * 20 * time.Millisecond) // later pages answer first
			mu.Lock()
			inFlight--
			mu.Unlock()
		}
		return pageOf(n, 5, 3, 3)
	})
	rows, err := apexPages[idRow](t.Context(), config.Prod, "/jobs", map[string]string{"page_size": "3"}, 15, "")
	if err != nil || len(rows) != 15 {
		t.Fatalf("apexPages = %d rows, %v", len(rows), err)
	}
	for i, r := range rows {
		if want := fmt.Sprintf("p%d-%c", i/3+1, 'a'+i%3); r.ID != want {
			t.Fatalf("row %d = %s, want %s (page order)", i, r.ID, want)
		}
	}
	if peak < 2 || peak > pageWindow {
		t.Errorf("peak concurrent page requests = %d, want between 2 and %d", peak, pageWindow)
	}
}

// ---- artifacts skip-if-present ----

func downloadsOf(f *fakeApex, name string) int {
	return len(f.callsTo("GET", strings.TrimPrefix(artifactFixture(name, "x", 0).DownloadURL, apiPrefix)))
}

func TestDownloadSkipsAFileOnlyWhenTheSidecarMatchesTheFreshListing(t *testing.T) {
	f, root := stubArtifacts(t)
	get := func() []DownloadedArtifact {
		_, files, err := DownloadJobArtifacts(t.Context(), config.PreProd, jobID, []string{"trace.zip"}, false, root, "")
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	get()
	files := get()
	if n := downloadsOf(f, "trace.zip"); n != 1 {
		t.Fatalf("downloads after an unchanged re-run = %d, want 1 (skipped)", n)
	}
	if files[0].SizeBytes != 4 {
		t.Errorf("skipped file reports size %d, want its on-disk 4", files[0].SizeBytes)
	}

	// The artifact grew (a running job's partial file): same id, new size -> fetched again.
	grown := artifactFixture("trace.zip", "other", 8)
	f.on("GET", "/jobs/"+jobID+"/artifacts", jsonReply([]Artifact{artifactFixture("job.log", "logs", 11), grown}))
	get()
	if n := downloadsOf(f, "trace.zip"); n != 2 {
		t.Fatalf("downloads after the listing's size changed = %d, want 2", n)
	}
	// Re-uploaded under a new id with the same size -> fetched again.
	reup := grown
	reup.ID = "99999999-reupload"
	reup.DownloadURL = apiPrefix + "/jobs/" + jobID + "/artifacts/" + reup.ID
	f.on("GET", "/jobs/"+jobID+"/artifacts", jsonReply([]Artifact{artifactFixture("job.log", "logs", 11), reup}))
	f.on("GET", strings.TrimPrefix(reup.DownloadURL, apiPrefix), reply{status: 200, raw: []byte("NEWZIPXX")})
	get()
	if n := len(f.callsTo("GET", strings.TrimPrefix(reup.DownloadURL, apiPrefix))); n != 1 {
		t.Fatalf("downloads of the re-uploaded artifact = %d, want 1", n)
	}
}

func TestDownloadNeverTrustsDiskAlone(t *testing.T) {
	f, root := stubArtifacts(t)
	dir := filepath.Join(root, jobID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trace.zip"), []byte("partial"), 0o600); err != nil { // no sidecar
		t.Fatal(err)
	}
	if _, _, err := DownloadJobArtifacts(t.Context(), config.PreProd, jobID, []string{"trace.zip"}, false, root, ""); err != nil {
		t.Fatal(err)
	}
	if n := downloadsOf(f, "trace.zip"); n != 1 {
		t.Errorf("downloads = %d, want 1: a file without a matching sidecar is replaced", n)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "trace.zip")); string(b) != "ZIP!" {
		t.Errorf("trace.zip = %q, want the downloaded bytes", b)
	}
}

func TestSidecarHoldsOnlyIDAndSize(t *testing.T) {
	_, root := stubArtifacts(t)
	if _, _, err := DownloadJobArtifacts(t.Context(), config.PreProd, jobID, []string{"job.log"}, false, root, ""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(sidecarPath(filepath.Join(root, jobID), "job.log"))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"artifactId":%q,"sizeBytes":11}`, artifactFixture("job.log", "logs", 11).ID)
	if string(b) != want {
		t.Errorf("sidecar = %s, want %s", b, want)
	}
}

func TestRefreshRedownloadsAnArtifactWhoseSidecarMatches(t *testing.T) {
	f, root := stubArtifacts(t)
	get := func(ctx context.Context) {
		if _, _, err := DownloadJobArtifacts(ctx, config.PreProd, jobID, []string{"trace.zip"}, false, root, ""); err != nil {
			t.Fatal(err)
		}
	}
	get(t.Context())
	get(memo.WithRefresh(t.Context()))
	if n := downloadsOf(f, "trace.zip"); n != 2 {
		t.Fatalf("downloads = %d, want 2 (refresh bypasses the sidecar skip)", n)
	}
}
