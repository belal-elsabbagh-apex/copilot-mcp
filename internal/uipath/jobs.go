package uipath

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// UiPath Orchestrator OData client: job search/detail, robot logs, video, deep links.

// Job is a Jobs row flattened from $expand=Robot($select=Name),Release($select=ProcessVersion).
// "" means absent (OData null / missing).
type Job struct {
	ID              string // numeric OData Id (StopJob addresses jobs by it); "" when absent or 0
	Key             string
	State           string
	ReleaseName     string
	CreationTime    string
	EndTime         string
	OutputArguments string
	InputArguments  string
	RobotName       string // the robot that ran/is running the job
	ProcessVersion  string // the package version the release is pinned to
}

// toJob flattens the expanded Robot/Release nav objects onto the flat Job shape.
func toJob(raw any) Job {
	return Job{
		ID:              idField(raw, "Id"),
		Key:             shared.Str(raw, "Key"),
		State:           shared.Str(raw, "State"),
		ReleaseName:     shared.Str(raw, "ReleaseName"),
		CreationTime:    shared.Str(raw, "CreationTime"),
		EndTime:         shared.Str(raw, "EndTime"),
		OutputArguments: shared.Str(raw, "OutputArguments"),
		InputArguments:  shared.Str(raw, "InputArguments"),
		RobotName:       shared.Str(recField(raw, "Robot"), "Name"),
		ProcessVersion:  shared.Str(recField(raw, "Release"), "ProcessVersion"),
	}
}

// jobExpand is folded into every Jobs query below — confirmed via live $metadata that
// Robot/Release are valid Jobs nav properties; nested $select keeps the expanded payload down to
// just the two fields toJob flattens.
const jobExpand = "Robot($select=Name),Release($select=ProcessVersion)"

// ListRecentJobs returns recent jobs in a folder, newest first — bounded by since/top, with NO
// OutputArguments filter. The reliable path when the `contains` substring scan is rejected
// (UiPath intermittently 400/500s on OutputArguments filtering). Filtering on CreationTime is
// indexed and supported. since and processName "" = unset.
func ListRecentJobs(ctx context.Context, since string, top int, folder, processName string) ([]Job, error) {
	params := map[string]string{
		"$orderby": "CreationTime desc",
		"$top":     fmt.Sprint(top),
		"$select":  "Id,Key,State,ReleaseName,CreationTime,EndTime",
		"$expand":  jobExpand,
	}
	var clauses []string
	if since != "" {
		c, err := sinceClause(since)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, c)
	}
	if needle := strings.TrimSpace(processName); needle != "" {
		clauses = append(clauses, fmt.Sprintf("contains(ReleaseName, '%s')", odataQuote(needle)))
	}
	if len(clauses) > 0 {
		params["$filter"] = strings.Join(clauses, " and ")
	}
	data, err := get(ctx, "/odata/Jobs", params, folder, "")
	if err != nil {
		return nil, err
	}
	rows := odataValues(data)
	jobs := make([]Job, len(rows))
	for i, r := range rows {
		jobs[i] = toJob(r)
	}
	return jobs, nil
}

// FetchJobByKey is the raw single-job read: one job by its GUID Key (the collection is keyed by
// numeric Id, so a Key lookup needs an OData $filter), nil if none matches. It returns the job
// incl. OutputArguments: a single-row `Key eq` filter DOES return it, unlike a broad/multi-row
// collection query (a `contains` scan or a `Key in (...)` batch), which nulls OutputArguments
// regardless of $select — a platform limitation, confirmed live.
//
// A $filter query, not a single-entity fetch: a non-matching key comes back as a normal empty
// result set (200, no rows) — there's no legitimate "not found" error here, so any error is a real
// failure and propagates.
func FetchJobByKey(ctx context.Context, jobKey, folder string) (*Job, error) {
	key := strings.TrimSpace(jobKey)
	if key == "" {
		return nil, nil
	}
	data, err := get(ctx, "/odata/Jobs", map[string]string{
		"$filter": "Key eq " + key,
		"$top":    "1",
		"$select": "Id,Key,State,ReleaseName,CreationTime,EndTime,OutputArguments,InputArguments",
		"$expand": jobExpand,
	}, folder, "")
	if err != nil {
		return nil, err
	}
	rows := odataValues(data)
	if len(rows) == 0 {
		return nil, nil
	}
	job := toJob(rows[0])
	return &job, nil
}

// JobLookup is one job lookup's outcome: Job is nil both when the key genuinely doesn't resolve
// AND when the lookup itself failed — check Err to tell them apart.
type JobLookup struct {
	Job *Job
	Err error
}

// fetchJobsForKeys fetches several jobs by GUID Key, one result per key in input order. Each key
// still costs its own HTTP call — confirmed live that the Jobs collection endpoint DOES support
// `Key in (...)`/`or`, but a multi-row collection response nulls OutputArguments (regardless of
// $select), whereas the single-row `Key eq` filter returns it — so a batched collection call
// can't replace this per-key fan-out when detail is needed. A bounded worker pool keeps a large
// batch from hammering Orchestrator; one key's failure lands on its own Err and loses nobody
// else's result.
func fetchJobsForKeys(ctx context.Context, keys []string, folder string) []JobLookup {
	return shared.MapIsolated(ctx, keys, maxConcurrency, func(ctx context.Context, key string) JobLookup {
		job, err := cachedJobByKey(ctx, key, folder)
		return JobLookup{Job: job, Err: err}
	})
}

const (
	jobDeepLinkPlaceholder = "{key}"
	logWindow              = 500 // $top cap of one RobotLogs fetch
)

var orgTenantRE = regexp.MustCompile(`cloud\.uipath\.com/([^/]+)/([^/]+)/orchestrator_`)

// jobDeepLink fills the job key into a per-org/tenant template derived from the orchestrator
// URL; "" when the URL doesn't name an org/tenant or the key is empty.
func jobDeepLink(jobKey string) string {
	cfg, err := config.Uipath()
	if err != nil || jobKey == "" {
		return ""
	}
	m := orgTenantRE.FindStringSubmatch(base(cfg))
	if m == nil {
		return ""
	}
	tmpl := fmt.Sprintf("https://cloud.uipath.com/%s/%s/orchestrator_/jobs(sidepanel:sidepanel/jobs/%s/details)", m[1], m[2], jobDeepLinkPlaceholder)
	return strings.Replace(tmpl, jobDeepLinkPlaceholder, jobKey, 1)
}
