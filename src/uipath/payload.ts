// UiPath queue-item serializer: turns the canonical native-typed content from
// orchestrator/queue-item.ts into the AddQueueItem request body. UiPath's robot reads the
// array fields and callbackContext as JSON STRINGS, so those (and only those) are stringified.

import { getUipath, type UipathConfig } from "../config/config.js";
import type { QueueItemPayload, QueueItemPayloadArgs } from "../orchestrator/backend.js";
import { guardQueueItemSafety } from "../orchestrator/safety.js";

const STRINGIFIED_FIELDS = [
  "DescriptionOfService",
  "Diagnoses",
  "ServiceCode",
  "Treatments",
  "Units",
  "callbackContext",
] as const;

export function serializeUipathContent(content: Record<string, unknown>): Record<string, unknown> {
  const out = { ...content };
  for (const k of STRINGIFIED_FIELDS) if (k in out) out[k] = JSON.stringify(out[k]);
  return out;
}

// Require the queue-build-specific UiPath fields (only needed by build_queue_item).
function requireQueueFields(u: UipathConfig): {
  queueUrl: string;
  addQueueItemPath: string;
  folderPath: string;
} {
  const { queueUrl, addQueueItemPath, folderPath } = u;
  if (queueUrl && addQueueItemPath && folderPath) return { queueUrl, addQueueItemPath, folderPath };
  const missing = (["queueUrl", "addQueueItemPath", "folderPath"] as const).filter((k) => !u[k]);
  throw new Error(
    `uipath config is missing fields required for build_queue_item: ${missing.join(", ")}. ` +
      "Add them to your COPILOT_MCP_CONFIG uipath block (see copilot-mcp.config.example.json).",
  );
}

export function uipathBuildQueueItemPayload(args: QueueItemPayloadArgs): QueueItemPayload {
  const uipath = getUipath();
  const { queueUrl, addQueueItemPath, folderPath } = requireQueueFields(uipath);
  // Single enforcement point for the IsApproved=false rule.
  const specific = guardQueueItemSafety(
    {
      ...serializeUipathContent(args.content),
      queueUrl,
      serverURL: uipath.serverUrlByEnv?.[args.env] ?? args.beBase ?? "",
    },
    "force",
  ).specificContent;

  return {
    payload: {
      itemData: {
        Name: args.queueName,
        Priority: "Normal",
        Reference: `${args.orderUid}-${Math.floor(Date.now() / 1000)}`,
        SpecificContent: specific,
      },
    },
    notes: [
      "To submit: POST payload.itemData as JSON to meta.postUrl with headers " +
        "'Authorization: Bearer <your uipath.bearer>' and " +
        `'X-UIPATH-FolderPath: ${folderPath}'. Never done automatically by this tool.`,
    ],
    meta: { postUrl: uipath.orchestratorUrl.replace(/\/$/, "") + addQueueItemPath, folderPath },
  };
}
