// Orchestrator reference (per backend, per env), served as copilot://reference/orchestrators.
// Hand-maintained facts, plus the label maps and terminal sets imported from the code that
// applies them so the resource cannot drift. Leaf module: only server.ts imports it (the
// mappers it imports reach mcp/reference.ts, so reference.ts must not import them).

import { APEX_ITEM_LABELS, APEX_JOB_LABELS, APEX_TERMINAL_STATUSES } from "../apex/map.js";
import { DEFAULT_APEX_FOLDERS } from "../config/config.js";
import { UIPATH_TERMINAL_STATES } from "../uipath/map.js";
import { UIPATH_FOLDERS } from "./reference.js";

const APEX_FOLDER_ID =
  "resolved at runtime (GET /folders by name, cached per process) — Apex ids are never hardcoded";

export const ORCHESTRATOR_REFERENCE = {
  uipath: {
    folders: UIPATH_FOLDERS,
    writeRule:
      "pre_prod only (the dev-clone folder, which skips all BE notifications). The MCP never repins releases or creates queues.",
  },
  apex: {
    folders: {
      prod: { name: DEFAULT_APEX_FOLDERS.prod, id: APEX_FOLDER_ID },
      pre_prod: { name: DEFAULT_APEX_FOLDERS.pre_prod, id: APEX_FOLDER_ID },
    },
    folderNote:
      "Names are the defaults; apex.folderByEnv overrides them. Both folders live on ONE Apex deployment with ONE login, so the folder is the only boundary: every request carries X-Folder-Id. Unlike UiPath's dev clone, the folder has no dry-run gate of its own — an item only fires real BE calls if its serverURL is the prod BE and IsApproved is true, which this MCP's guard prevents on every write (IsApproved forced false, serverURL pinned to the configured pre-prod value).",
    writeRule:
      "pre_prod ('Authorization Test') only: schema literal + domain assert + the HTTP chokepoint refusing any non-GET unless env=pre_prod. delete_queue_item refuses anything but status New; stop_job refuses terminal jobs. The MCP never deploys, pins, or creates Processes/queues, and never touches Apex credentials, assets or Action Center.",
  },
  stateLabels: {
    uipathJobState:
      "UiPath job State passes through unchanged (Pending, Running, Successful, Faulted, Stopped, …).",
    apexJobStatusToState: APEX_JOB_LABELS,
    apexItemStatusToStatus: APEX_ITEM_LABELS,
  },
  terminal: {
    uipathJobStates: [...UIPATH_TERMINAL_STATES],
    apexJobStatuses: [...APEX_TERMINAL_STATUSES],
  },
  notes: [
    "One transaction maps to MANY jobs on Apex: a retry is a NEW job on the SAME queue item (retryOfJobId, attempt on each job; the parent ends cancelled), so a queue item's jobs[] is newest-first and a verdict must use the latest attempt. UiPath retries are new queue items, so its jobs[] holds at most one.",
    "Apex logs exist only after the run and only per the Process's artifact_policy: the live log is dashboard-only SSE, so get_job / get_job_logs on a running job report logsUnavailable.reason='running', and a finished job whose policy skipped the artifact (on_failure for a success, never) reports 'artifact-policy'. Use get_job_artifacts for the files themselves (job.log, trace.zip, screenshots, recording).",
    "Never-started jobs: a job that ended failed with started_at null (typically error_class missing_credential, reaped before any worker claimed it) has no logs and no artifacts — logsUnavailable.reason='never-started', details.neverStarted=true, and find_stuck_orders reports verdict 'job-never-started'. Fix the credential binding; re-running does not help.",
  ],
} as const;
