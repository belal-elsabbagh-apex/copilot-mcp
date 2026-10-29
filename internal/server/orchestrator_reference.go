package server

// Orchestrator reference (per backend, per env), served as copilot://reference/orchestrators.
// Hand-maintained facts, plus the label maps and terminal sets taken from the code that applies
// them so the resource cannot drift.

import (
	"bytes"
	"encoding/json"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/apex"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/reference"
)

const apexFolderID = "resolved at runtime (GET /folders by name, cached per process) — Apex ids are never hardcoded"

type apexFolder struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// labelObject marshals ordered labels as a JSON object in dashboard order (a Go map would not
// keep it).
type labelObject []apex.Label

func (l labelObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, label := range l {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(label.Wire)
		v, _ := json.Marshal(label.Label)
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// uipathTerminalStates in the order the TS set listed them; orchestrator_reference_test pins them
// to uipath.TerminalStates so a new terminal state cannot be missed.
var uipathTerminalStates = []string{"Successful", "Faulted", "Stopped"}

type uipathRef struct {
	Folders   any    `json:"folders"`
	WriteRule string `json:"writeRule"`
}

type apexRef struct {
	Folders struct {
		Prod    apexFolder `json:"prod"`
		PreProd apexFolder `json:"pre_prod"`
	} `json:"folders"`
	FolderNote string `json:"folderNote"`
	WriteRule  string `json:"writeRule"`
}

type stateLabels struct {
	UipathJobState        string      `json:"uipathJobState"`
	ApexJobStatusToState  labelObject `json:"apexJobStatusToState"`
	ApexItemStatusToState labelObject `json:"apexItemStatusToStatus"`
}

type terminalSets struct {
	UipathJobStates []string `json:"uipathJobStates"`
	ApexJobStatuses []string `json:"apexJobStatuses"`
}

type orchestratorRef struct {
	UiPath      uipathRef    `json:"uipath"`
	Apex        apexRef      `json:"apex"`
	StateLabels stateLabels  `json:"stateLabels"`
	Terminal    terminalSets `json:"terminal"`
	Notes       []string     `json:"notes"`
}

func orchestratorReference() orchestratorRef {
	ref := orchestratorRef{
		UiPath: uipathRef{
			Folders:   reference.UiPathFolders,
			WriteRule: "pre_prod only (the 'Authorization Dev Clone' folder). That folder is NOT a BE sandbox: its runs fire the same real BackendApi calls as prod, at whatever serverURL the item names, so the queue-item guard (IsApproved forced false, serverURL pinned to the configured pre-prod value) is what keeps a test run harmless. The MCP never repins releases or creates queues.",
		},
		Apex: apexRef{
			FolderNote: "Names are the defaults; apex.folderByEnv overrides them. Both folders live on ONE Apex deployment with ONE login, so the folder is the only boundary: every request carries X-Folder-Id. Like UiPath's dev clone, the folder is not a BE sandbox — an item fires real BE calls against whatever serverURL it names, and a real submission only if IsApproved is true; this MCP's guard prevents both on every write (IsApproved forced false, serverURL pinned to the configured pre-prod value).",
			WriteRule:  "pre_prod ('Authorization Test') only: schema literal + domain assert + the HTTP chokepoint refusing any non-GET unless env=pre_prod. delete_queue_item refuses anything but status New; stop_job refuses terminal jobs. The MCP never deploys, pins, or creates Processes/queues, and never touches Apex credentials, assets or Action Center.",
		},
		StateLabels: stateLabels{
			UipathJobState:        "UiPath job State passes through unchanged (Pending, Running, Successful, Faulted, Stopped, …).",
			ApexJobStatusToState:  apex.JobLabels,
			ApexItemStatusToState: apex.ItemLabels,
		},
		Terminal: terminalSets{UipathJobStates: uipathTerminalStates, ApexJobStatuses: apex.TerminalStatuses},
		Notes: []string{
			"One transaction maps to MANY jobs on Apex: a retry is a NEW job on the SAME queue item (retryOfJobId, attempt on each job; the superseded parent usually ends failed, sometimes cancelled), so a queue item's jobs[] is newest-first and a verdict must use the latest attempt. UiPath retries are new queue items, so its jobs[] holds at most one.",
			"Apex logs exist only after the run and only per the Process's artifact_policy: the live log is dashboard-only SSE, so get_job / get_job_logs on a running job report logsUnavailable.reason='running', and a finished job whose policy skipped the artifact (on_failure for a success, never) reports 'artifact-policy'. Use get_job_artifacts for the files themselves (job.log, trace.zip, screenshots, recording).",
			"Never-started jobs: a job that ended failed with started_at null (typically error_class missing_credential, reaped before any worker claimed it) has no logs and no artifacts — logsUnavailable.reason='never-started', details.neverStarted=true, and find_stuck_orders reports verdict 'job-never-started'. Fix the credential binding; re-running does not help.",
			"Read caching: get_job / get_job_logs / build_faulted_job_issue / get_job_artifacts serve a TERMINAL-AND-SETTLED job from a persistent on-disk cache (under the cache dir, off when copilot.sessionCache is false) only after a live read in the same call confirms it; non-terminal jobs, errors, lists, queue items, processes and triggers are never cached and no write guard ever reads the cache. Pass refresh:true to those four tools to bypass it and replace the cached entry.",
		},
	}
	ref.Apex.Folders.Prod = apexFolder{config.DefaultApexFolders[config.Prod], apexFolderID}
	ref.Apex.Folders.PreProd = apexFolder{config.DefaultApexFolders[config.PreProd], apexFolderID}
	return ref
}
