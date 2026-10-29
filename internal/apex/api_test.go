package apex

import (
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

func queryOf(t *testing.T, c apexCall) url.Values {
	t.Helper()
	u, err := url.Parse("https://x" + c.Path)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestListJobsSendsOnlyTheVerifiedParamsSnakeCasedNewestFirst(t *testing.T) {
	f := newFakeApex(t, nil)
	f.route("GET", "/jobs", func(apexCall) reply { return pagedReply([]any{jobRowFixture()}, 1) })
	_, err := listJobs(t.Context(), config.PreProd, "", JobFilters{
		Status: "failed", QueueID: "q1", WorkflowID: "w1", CreatedAfter: "2026-10-01T00:00:00Z", Reference: "r1",
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	q := queryOf(t, f.callsTo("GET", "/jobs")[0])
	want := map[string]string{
		"sort_by": "created_at", "sort_dir": "desc", "status": "failed", "queue_id": "q1", "workflow_id": "w1",
		"created_after": "2026-10-01T00:00:00Z", "reference": "r1", "page": "1", "page_size": "10",
	}
	got := map[string]string{}
	for k := range q {
		got[k] = q.Get(k)
	}
	if !maps.Equal(got, want) {
		t.Errorf("query = %v, want %v", got, want)
	}
}

func TestListJobsOmitsUnsetFiltersInsteadOfSendingEmptyValues(t *testing.T) {
	f := newFakeApex(t, nil)
	f.route("GET", "/jobs", func(apexCall) reply { return pagedReply([]any{}, 0) })
	if _, err := listJobs(t.Context(), config.PreProd, "", JobFilters{Reference: "r1"}, 10); err != nil {
		t.Fatal(err)
	}
	q := queryOf(t, f.callsTo("GET", "/jobs")[0])
	if q.Get("reference") != "r1" || q.Has("status") {
		t.Errorf("query = %v, want reference only", q)
	}
}

func TestListQueueItemsMapsStatusAndSearch(t *testing.T) {
	f := newFakeApex(t, nil)
	f.route("GET", "/queues/q1/items", func(apexCall) reply { return pagedReply([]any{}, 0) })
	if _, err := listQueueItems(t.Context(), config.PreProd, "", "q1", ItemFilters{Status: "in_progress", Search: "uid-1"}, 10); err != nil {
		t.Fatal(err)
	}
	q := queryOf(t, f.callsTo("GET", "/queues/q1/items")[0])
	if q.Get("status") != "in_progress" || q.Get("search") != "uid-1" || q.Has("reference") {
		t.Errorf("query = %v", q)
	}
}

func TestFolderNameOverrideScopesTheArtifactListing(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/"+jobID+"/artifacts", jsonReply([]any{artifactFixture("job.log", "logs", 1)}))
	if _, err := listArtifacts(t.Context(), config.PreProd, "Authorization", jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := listArtifacts(t.Context(), config.PreProd, "", jobID); err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("GET", "/jobs/"+jobID+"/artifacts")
	if calls[0].Header.Get("X-Folder-Id") != folderIDs["Authorization"] || calls[1].Header.Get("X-Folder-Id") != folderIDs["Authorization Test"] {
		t.Errorf("folder headers = %q, %q", calls[0].Header.Get("X-Folder-Id"), calls[1].Header.Get("X-Folder-Id"))
	}
}

func TestNonListArtifactResponseIsAnErrorNotAnEmptyList(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/"+jobID+"/artifacts", jsonReply(map[string]any{"items": []any{}}))
	if _, err := listArtifacts(t.Context(), config.PreProd, "", jobID); err == nil || !strings.Contains(err.Error(), "not a list") {
		t.Errorf("error = %v, want 'not a list'", err)
	}
}

func TestDownloadForwardsTheRangeHeaderOnlyWhenGiven(t *testing.T) {
	f := newFakeApex(t, nil)
	a := artifactFixture("job.log", "logs", 4)
	path := strings.TrimPrefix(a.DownloadURL, apiPrefix)
	f.on("GET", path, reply{status: 206, raw: []byte("tail")})
	if _, err := apexDownload(t.Context(), a.DownloadURL, "bytes=-4", io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := apexDownload(t.Context(), a.DownloadURL, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("GET", path)
	if calls[0].Header.Get("Range") != "bytes=-4" || calls[1].Header.Get("Range") != "" {
		t.Errorf("Range headers = %q, %q", calls[0].Header.Get("Range"), calls[1].Header.Get("Range"))
	}
}

func TestGetJob404IsNotFoundButA500IsNot(t *testing.T) {
	f := newFakeApex(t, nil)
	f.on("GET", "/jobs/gone", jsonReply(map[string]any{"detail": "Job not found"}, 404))
	f.on("GET", "/jobs/boom", jsonReply(map[string]any{"detail": "kaput"}, 500))
	_, gone := getJob(t.Context(), config.PreProd, "", "gone")
	_, boom := getJob(t.Context(), config.PreProd, "", "boom")
	if !notFound(gone) || notFound(boom) || notFound(http.ErrAbortHandler) {
		t.Errorf("notFound(gone,boom,other) = %v %v %v, want true false false", notFound(gone), notFound(boom), notFound(http.ErrAbortHandler))
	}
}
