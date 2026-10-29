---
id: prod-preprod-isolation
files: internal/copilot/client.go, internal/copilot/mirror.go, internal/copilot/order.go, internal/copilot/settings/sync.go, internal/server/tools_orders_write.go
---

Every write path targets pre-prod only. Prod is read-only, by design, for this whole server.

Concretely:
- `delete_preprod_order` never touches prod.
- `create_preprod_order` never submits an order — it only mints through `forReview`. Only
  `submit_preprod_order` may submit, and only with explicit user authorization at call time.
- `build_queue_item` / `pull_queue_item` force `IsApproved=false` (`orchestrator.SafetyForce`), so a test run can
  never submit a real auth.
- BE clients are env-tagged at construction (`newHTTPClient(base, env, cookies)` in `internal/copilot/client.go`).
- Every BE write engine (`MintPreprodOrder`, `SubmitOrder`, the `delete_preprod_order`
  handler, `settings.ApplySettingsSync`) calls `copilot.AssertPreProdClient`, which refuses both prod-tagged
  AND untagged clients — it fails closed.

**Why:** this is the single most important safety property of the whole server. A prod
mutation here is not a bug to fix later, it's an incident.

**Violation signature:** any new function that issues a BE write (POST/PUT/DELETE through
`copilot.Client.Req`) without first calling `AssertPreProdClient` on the client it
received; a new client constructed without an `env` tag; any code path where
`create_preprod_order` (or anything it calls) reaches a submit/process-to-final-state call;
`IsApproved` set from caller input instead of forced `false` in queue-item builders.
