package uipath

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ---- writes: translate the neutral args onto actions.go, pre_prod-only there ----

func (backend) AddQueueItem(ctx context.Context, args orchestrator.AddQueueItemArgs) (orchestrator.AddQueueItemResult, error) {
	r, err := addQueueItem(ctx, args)
	if err != nil {
		return orchestrator.AddQueueItemResult{}, err
	}
	queueID := ""
	if r.queueDefinitionID != 0 {
		queueID = fmt.Sprint(r.queueDefinitionID)
	}
	return orchestrator.AddQueueItemResult{
		Env: args.Env, QueueID: queueID, QueueName: r.queueName, Reference: r.reference,
		ItemID: fmt.Sprint(r.itemID), Status: r.status, Forced: r.forced,
	}, nil
}

var itemIDRE = regexp.MustCompile(`^[1-9]\d*$`)

// maxSafeInteger is JS Number.MAX_SAFE_INTEGER.
const maxSafeInteger = 1<<53 - 1

// parseItemID: UiPath queue-item ids are positive integers; the neutral arg is a string (Apex ids
// are uuids).
func parseItemID(itemID string) (int64, error) {
	id, err := strconv.ParseInt(itemID, 10, 64)
	if !itemIDRE.MatchString(itemID) || err != nil || id > maxSafeInteger {
		return 0, shared.Expectedf("UiPath queue item ids are positive integers, got '%s'", itemID)
	}
	return id, nil
}

func (backend) DeleteQueueItem(ctx context.Context, args orchestrator.DeleteQueueItemArgs) (orchestrator.DeleteQueueItemResult, error) {
	id, err := parseItemID(args.ItemID)
	if err != nil {
		return orchestrator.DeleteQueueItemResult{}, err
	}
	r, err := deleteQueueItem(ctx, id, args.Env)
	if err != nil {
		return orchestrator.DeleteQueueItemResult{}, err
	}
	return orchestrator.DeleteQueueItemResult{
		Env: args.Env, ItemID: fmt.Sprint(id), Deleted: true,
		PreviousStatus: string(orchestrator.StatusNew), Reference: r.reference,
	}, nil
}

func (backend) StartJob(ctx context.Context, args orchestrator.StartJobArgs) (orchestrator.StartJobResult, error) {
	started, err := startJob(ctx, startArgs{env: args.Env, releaseKey: args.ProcessID, inputArguments: args.Input, jobsCount: args.JobsCount})
	if err != nil {
		return orchestrator.StartJobResult{}, err
	}
	return orchestrator.StartJobResult{Env: args.Env, Jobs: mapSlice(started, func(j startedJob) orchestrator.StartedJob {
		return orchestrator.StartedJob{ID: j.key, State: j.state, ProcessName: j.releaseName, Link: jobDeepLink(j.key)}
	})}, nil
}

func (backend) StopJob(ctx context.Context, args orchestrator.StopJobArgs) (orchestrator.StopJobResult, error) {
	key, err := stopJob(ctx, args.Env, args.JobID, args.Strategy)
	if err != nil {
		return orchestrator.StopJobResult{}, err
	}
	return orchestrator.StopJobResult{Env: args.Env, JobID: key, Strategy: args.Strategy, Stopped: true}, nil
}
