package apex

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ---- build_faulted_job_issue ---------------------------------------------

const maxAttempts = 50

// jobAttempts are the attempts of one queue item, newest first: a retry is a NEW job on the same
// item, and /jobs?reference= is the only verified item filter. Two items can share a reference, so
// keep only this item's jobs.
func jobAttempts(ctx context.Context, env config.Env, folder string, detail JobDetail) ([]orchestrator.JobAttempt, error) {
	if detail.QueueItemRef == nil || *detail.QueueItemRef == "" {
		return []orchestrator.JobAttempt{toJobAttempt(detail.JobRow)}, nil
	}
	rows, err := listJobs(ctx, env, folder, JobFilters{Reference: *detail.QueueItemRef}, maxAttempts)
	if err != nil {
		return nil, err
	}
	attempts := []orchestrator.JobAttempt{}
	for _, r := range rows {
		if sameItem(r.QueueItemID, detail.QueueItemID) {
			attempts = append(attempts, toJobAttempt(r))
		}
	}
	return attempts, nil
}

func sameItem(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// bestEffortLogRows: logs are a best-effort attachment: a failed fetch must not discard a valid
// job lookup (same convention as UiPath's buildFaultedJobIssue). A missing job.log is not a
// failure: never-started reaps and artifact_policy leave none.
func bestEffortLogRows(ctx context.Context, env config.Env, folder string, row JobRow) (rows []orchestrator.JobLog, logsErr string) {
	log, err := readJobLog(ctx, env, folder, row)
	if err != nil {
		return nil, err.Error()
	}
	if log == nil {
		return nil, ""
	}
	return plainLogs(log.Rows), ""
}

func (backend) BuildFaultedJobIssue(ctx context.Context, args orchestrator.FaultIssueArgs) (orchestrator.FaultedJobIssueResult, error) {
	detail, err := getJob(ctx, args.Env, args.Folder, args.JobID)
	if err != nil {
		return orchestrator.FaultedJobIssueResult{}, err
	}
	job := toJobCore(detail.JobRow, &detail, DashboardLink("/jobs/"+detail.ID))
	if detail.Status == "cancelled" {
		return orchestrator.FaultedJobIssueResult{}, shared.Expectedf("job %s is %s (cancelled): a stopped Apex job is a cancellation or a "+
			"superseded retry parent, not a fault — nothing to file", args.JobID, job.State)
	}
	if f := job.Fault; f == nil || f.Message == nil || *f.Message == "" || f.Signature == nil || *f.Signature == "" {
		return orchestrator.FaultedJobIssueResult{}, shared.Expectedf("job %s is %s, not a fault — build_faulted_job_issue needs a failed, "+
			"timed-out or lost job", args.JobID, job.State)
	}
	var (
		attempts []orchestrator.JobAttempt
		aerr     error
		wg       sync.WaitGroup
	)
	wg.Go(func() { attempts, aerr = jobAttempts(ctx, args.Env, args.Folder, detail) })
	rows, logsErr := bestEffortLogRows(ctx, args.Env, args.Folder, detail.JobRow)
	wg.Wait()
	if aerr != nil {
		return orchestrator.FaultedJobIssueResult{}, aerr
	}
	issue := formatFaultIssue(job, rows, faultOptions{Env: args.Env, Attempts: attempts, Repo: args.Repo, Labels: args.Labels})
	return orchestrator.FaultedJobIssueResult{FaultedJobIssue: issue, Found: true, LogsError: logsErr}, nil
}

// ---- build_queue_item serializer -------------------------------------------

// queueBody is the body add_queue_item posts.
type queueBody struct {
	SpecificContent map[string]any `json:"specific_content"`
	Reference       string         `json:"reference"`
	MatchValue      string         `json:"match_value"`
}

type queuePayload struct {
	QueueName string    `json:"queueName"`
	Body      queueBody `json:"body"`
}

// BuildQueueItemPayload is pure given config: native content + the configured callback host/queue,
// guarded (IsApproved forced false), shaped as the body add_queue_item posts. No silent fallback
// for serverURL/queueUrl — an item pointing a robot at the wrong host is worse than none.
func (backend) BuildQueueItemPayload(_ context.Context, args orchestrator.QueueItemPayloadArgs) (orchestrator.QueueItemPayload, error) {
	apex, err := config.ResolveApex()
	if err != nil {
		return orchestrator.QueueItemPayload{}, err
	}
	serverURL := apex.ServerURLByEnv[args.Env]
	if serverURL == "" {
		return orchestrator.QueueItemPayload{}, shared.Expectedf("cannot build an Apex queue item for env '%s': apex.serverUrlByEnv.%s "+
			"is not configured (it is the callback host the item's serverURL must carry)", args.Env, args.Env)
	}
	if apex.QueueURL == "" {
		return orchestrator.QueueItemPayload{}, shared.Expectedf("cannot build an Apex queue item: apex.queueUrl is not configured " +
			"(the callback queue the item's queueUrl must carry)")
	}
	content := make(map[string]any, len(args.Content)+2)
	for k, v := range args.Content {
		content[k] = v
	}
	content["serverURL"], content["queueUrl"] = serverURL, apex.QueueURL
	guarded, err := orchestrator.GuardQueueItemSafety(content, orchestrator.SafetyForce, orchestrator.QueueSafetyLimits{})
	if err != nil {
		return orchestrator.QueueItemPayload{}, err
	}
	native, err := serializeContent(guarded.SpecificContent)
	if err != nil {
		return orchestrator.QueueItemPayload{}, err
	}
	matchValue, err := matchValueOf(native)
	if err != nil {
		return orchestrator.QueueItemPayload{}, err
	}
	notes := []string{
		fmt.Sprintf("serverURL/queueUrl come from apex.serverUrlByEnv.%s / apex.queueUrl; native types "+
			"(arrays, booleans, numbers, callbackContext object) are kept as Apex stores them.", args.Env),
		fmt.Sprintf(`To post: add_queue_item(orchestrator: "apex", env: "pre_prod", queue: "%s", `+
			"reference: payload.body.reference, specificContent: payload.body.specific_content). "+
			"match_value is re-derived from specificContent.automationId at post time.", args.QueueName),
	}
	if len(guarded.Forced) > 0 {
		notes = append([]string{strings.Join(guarded.Forced, ", ") + " forced to false (test-safety guard)."}, notes...)
	}
	return orchestrator.QueueItemPayload{
		Payload: queuePayload{QueueName: args.QueueName, Body: queueBody{
			SpecificContent: native,
			Reference:       fmt.Sprintf("%s-%d", args.OrderUID, time.Now().Unix()),
			MatchValue:      matchValue,
		}},
		Notes: notes,
		Meta: map[string]any{
			"queueName": args.QueueName, "env": args.Env, "orderUid": args.OrderUID,
			"note": "post with add_queue_item(orchestrator: apex)",
		},
	}, nil
}
