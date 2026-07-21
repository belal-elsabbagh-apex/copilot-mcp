---
id: prod-preprod-isolation
files: src/copilot/copilot-client.ts, src/copilot/mirror.ts, src/copilot/settings/sync.ts
---

Every write path targets pre-prod only. Prod is read-only, by design, for this whole server.

Concretely:
- `delete_preprod_order` never touches prod.
- `create_preprod_order` never submits an order — it only mints through `forReview`. Only
  `submit_preprod_order` may submit, and only with explicit user authorization at call time.
- `build_queue_item` / `pull_queue_item` force `IsApproved=false`, so a test run can never
  submit a real auth.
- BE clients are env-tagged at construction (`makeClient(base, env)`).
- Every BE write engine (`mintPreprodOrder`, `submitOrder`, the `delete_preprod_order`
  handler, `applySettingsSync`) calls `assertPreProdClient`, which refuses both prod-tagged
  AND untagged clients — it fails closed.

**Why:** this is the single most important safety property of the whole server. A prod
mutation here is not a bug to fix later, it's an incident.

**Violation signature:** any new function that issues a BE write (POST/PUT/DELETE against
`copilot-client.ts`'s `req`) without first calling `assertPreProdClient` on the client it
received; a new client constructed without an `env` tag; any code path where
`create_preprod_order` (or anything it calls) reaches a submit/process-to-final-state call;
`IsApproved` set from caller input instead of hardcoded `false` in queue-item builders.
