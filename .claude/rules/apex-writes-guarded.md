---
id: apex-writes-guarded
files: internal/apex/client.go, internal/apex/session.go, internal/apex/actions.go, internal/orchestrator/safety.go
---

All Apex HTTP lives in `internal/apex/client.go` (login and the `/folders` listing in `session.go`).
`apexRequest` is the ONLY entry point to Apex's data and write routes: it ALWAYS sends `X-Folder-Id`
(omitting it silently reads the user's default folder), sends only verified query params, and refuses any
method other than GET unless `env == config.PreProd` (`assertWritable`) — before any network call. A write
whose pre_prod and prod folders resolve to the same id is refused too (`assertEnvFoldersDistinct`). The only
other requests, all in `internal/apex`, are the login POST, the `GET /folders` listing that resolves folder
ids, and `apexDownload` (GET-only, Bearer, same-origin `/api/v1/` paths, no folder header). Writes are never
retried on a network error or a 429 (those retries are GET-only), and never take the data-GET concurrency slot or
a cached read. The one replay is `transport`'s single re-login on a 401 (any method: the server rejected the
credentials, so nothing ran); a sweep must not flag it.

`internal/apex/actions.go` is the ONLY file that mutates Apex (`AddQueueItem`, `DeleteQueueItem`, `StartJob`,
`StopJob` on `backend`). All four are `pre_prod`-only at the schema layer (`preProdOnly()` in
`internal/server/tools_orchestrator_common.go`, a JSON-schema `const`) AND assert `env == config.PreProd` in
the domain function (`assertPreProd`). Every posted `specific_content` passes
`orchestrator.GuardQueueItemSafety` in `SafetyPreProdPost` mode (IsApproved forced false; non-empty
`serverURL`/`queueUrl` must match the configured pre-prod values, failing closed when unset; `<TO-FILL>`
rejected, including inside native arrays/objects). `DeleteQueueItem` is fetch-first and refuses unless the
item's status is `new`; `StopJob` is fetch-first and refuses every terminal status (`completed`, `failed`,
`cancelled`, `lost`, `timed_out`). Both also prove the folder client-side: `StopJob` refuses a job whose
`folder_id` is not the pre_prod folder's, and `DeleteQueueItem` refuses a `queueId` absent from the pre_prod
folder's queue listing (`containsID`). Writes never take a `folder` override. The fetch-first reads and the
queue/workflow lookups call the raw `getJob`/`listQueues`/`listWorkflows`/`getQueueItem`, never the cached
read wrappers (`readcache.go`, `queuememo.go`). The MCP never deploys, pins, creates Processes/queues, or
touches Apex credentials, assets or Action Center.

**Why:** the Apex prod and test folders share one deployment and one login, so the folder is the
only boundary; a dropped gate could write to prod.

**Violation signature:** an `http.Client`/`http.NewRequest` against Apex outside `internal/apex/client.go` and
`session.go`; a non-GET path that doesn't pass through `assertWritable`; an `apexRequest` path that omits
`X-Folder-Id`; an `apexDownload` that is not GET or leaves the configured host's `/api/v1/`; an Apex write
outside `actions.go`; a write endpoint that accepts a `folder` argument; a retry loop around a POST (other than `transport`'s single 401 re-login); a write
guard reading through `cachedRead`/`listQueuesForRead`; the gate tests (`client_test.go`:
`TestWritesOnProdAreRefusedBeforeAnyRequest`, `TestWriteWithAFolderOverrideIsRefusedBeforeAnyRequest`,
`TestPreProdWriteIsRefusedWhenBothEnvsResolveToOneFolder`) removed or weakened.
