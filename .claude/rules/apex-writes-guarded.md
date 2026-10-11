---
id: apex-writes-guarded
files: src/apex/client.ts, src/apex/actions.ts, src/orchestrator/safety.ts
---

`src/apex/client.ts`'s `apexRequest` is the ONLY Apex `fetch`: login, paging and artifact downloads
(`apexDownload`) all go through it. It ALWAYS sends `X-Folder-Id` (omitting it silently reads the
user's default folder), sends only verified query params, and refuses any method other than GET
unless `env === "pre_prod"` — before any network call. Writes are never auto-retried.

`src/apex/actions.ts` is the ONLY module that mutates Apex (`add_queue_item`, `delete_queue_item`,
`start_job`, `stop_job`). All four take `env: z.literal("pre_prod")` at the schema layer AND assert
`env === "pre_prod"` in the domain function. Every posted `specific_content` passes
`guardQueueItemSafety` (IsApproved forced false; non-empty `serverURL`/`queueUrl` must match the
configured pre-prod values, failing closed when unset; `<TO-FILL>` rejected (including inside native arrays/objects)). `delete_queue_item` is
fetch-first and refuses unless the item's status is `new`; `stop_job` is fetch-first and refuses every
terminal status (`completed`, `failed`, `cancelled`, `lost`, `timed_out`). Both also prove the folder client-side: `stop_job` refuses a job whose `folder_id` is not the pre_prod folder's, and `delete_queue_item` refuses a `queueId` absent from the pre_prod folder's queue listing. Writes never take a
`folder` override. The MCP never deploys, pins, creates Processes/queues, or touches Apex
credentials, assets or Action Center.

**Why:** the Apex prod and test folders share one deployment and one login, so the folder is the
only boundary; a dropped gate could write to prod.

**Violation signature:** a `fetch(` against Apex outside `apex/client.ts`; a non-GET path that
doesn't pass through the `pre_prod` gate; a request without `X-Folder-Id`; an Apex write outside
`apex/actions.ts`; a write endpoint that accepts a `folder` argument; a retry loop around a
POST; the gate test (`apex/client.test.ts`) not running before `fetch`.
