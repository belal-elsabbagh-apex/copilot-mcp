package apex

import "github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"

// ---- queues, processes, triggers ----------------------------------------------

// QueueDetails is QueueCore.Details.
type QueueDetails struct {
	AutoDrain              bool `json:"autoDrain"`
	MaxConcurrentJobs      *int `json:"maxConcurrentJobs"`
	MaxRetries             *int `json:"maxRetries"`
	EnforceUniqueReference bool `json:"enforceUniqueReference"`
}

func toQueueCore(q Queue) orchestrator.QueueCore {
	return orchestrator.QueueCore{
		Orchestrator: orchestrator.Apex, ID: q.ID, Name: q.Name, ProcessID: q.WorkflowID, Active: q.IsActive,
		Details: QueueDetails{q.AutoDrain, q.MaxConcurrentJobs, q.MaxRetries, q.EnforceUniqueReference},
	}
}

// ProcessDetails is ProcessCore.Details.
type ProcessDetails struct {
	TimeoutSeconds  *int   `json:"timeoutSeconds"`
	MaxAttempts     *int   `json:"maxAttempts"`
	BackoffStrategy string `json:"backoffStrategy"`
	ArtifactPolicy  string `json:"artifactPolicy"`
}

func toProcessCore(w Workflow) orchestrator.ProcessCore {
	return orchestrator.ProcessCore{
		Orchestrator: orchestrator.Apex, ID: w.ID, Name: w.Name, Package: w.PackageName, Version: w.CurrentVersion, Active: w.IsActive,
		Details: ProcessDetails{w.TimeoutSeconds, w.MaxAttempts, w.BackoffStrategy, w.ArtifactPolicy},
	}
}

// QueueTriggerDetails is the Details of a queue-derived trigger.
type QueueTriggerDetails struct {
	MaxConcurrent *int `json:"maxConcurrent"`
}

// ScheduleTriggerDetails is the Details of a schedule trigger.
type ScheduleTriggerDetails struct {
	Timezone string `json:"timezone"`
}

// toQueueTriggers: Apex has no /triggers route (404). A queue that is active and auto-draining is
// the equivalent of an enabled UiPath queue trigger; max_concurrent_jobs is its maxJobsForActivation.
func toQueueTriggers(queues []Queue) []orchestrator.TriggerCore {
	out := make([]orchestrator.TriggerCore, len(queues))
	for i, q := range queues {
		out[i] = orchestrator.TriggerCore{
			Orchestrator: orchestrator.Apex, ID: q.ID, Name: q.Name, Kind: orchestrator.TriggerQueue,
			ProcessID: q.WorkflowID, QueueID: new(q.ID), Enabled: q.IsActive && q.AutoDrain,
			Details: QueueTriggerDetails{q.MaxConcurrentJobs},
		}
	}
	return out
}

func toScheduleTrigger(s Schedule) orchestrator.TriggerCore {
	return orchestrator.TriggerCore{
		Orchestrator: orchestrator.Apex, ID: s.ID, Name: s.CronExpression + " (" + s.Timezone + ")", Kind: orchestrator.TriggerTime,
		ProcessID: new(s.WorkflowID), Enabled: !s.IsPaused, Cron: new(s.CronExpression),
		Details: ScheduleTriggerDetails{s.Timezone},
	}
}
