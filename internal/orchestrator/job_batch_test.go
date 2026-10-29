package orchestrator

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func TestResolveJobIDsLoneJobIDIsASingleLookup(t *testing.T) {
	got, err := ResolveJobIDs("job-1", nil)
	if err != nil || got.Batch || !slices.Equal(got.IDs, []string{"job-1"}) {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestResolveJobIDsJobIDsIsABatchEvenWithOneID(t *testing.T) {
	got, err := ResolveJobIDs("", []string{"job-1"})
	if err != nil || !got.Batch || !slices.Equal(got.IDs, []string{"job-1"}) {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestResolveJobIDsBothOrNeitherIsAnExpectedError(t *testing.T) {
	_, both := ResolveJobIDs("job-1", []string{"job-2"})
	_, neither := ResolveJobIDs("", nil)
	if !shared.IsExpected(both) || !shared.IsExpected(neither) {
		t.Fatalf("not expected errors: %v, %v", both, neither)
	}
	if !strings.Contains(neither.Error(), "either jobId or jobIds") {
		t.Errorf("neither: %v", neither)
	}
	if errors.Is(both, neither) {
		t.Error("distinct errors compared equal")
	}
}

type testEntry struct {
	ID    string `json:"id"`
	Found bool   `json:"found"`
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJobEntriesEnvelopeSingleFlattensTheOnlyEntryBesideTheEnvelope(t *testing.T) {
	got := marshal(t, JobEntriesEnvelope(UiPath, config.Prod, "Authorization", false, []testEntry{{"a", true}, {"b", false}}))
	want := `{"orchestrator":"uipath","env":"prod","folder":"Authorization","id":"a","found":true}`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestJobEntriesEnvelopeBatchNestsEveryEntryUnderJobsWithACount(t *testing.T) {
	got := marshal(t, JobEntriesEnvelope(UiPath, config.Prod, "Authorization", true, []testEntry{{"a", true}, {"b", false}}))
	want := `{"orchestrator":"uipath","env":"prod","folder":"Authorization","count":2,"jobs":[{"id":"a","found":true},{"id":"b","found":false}]}`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
