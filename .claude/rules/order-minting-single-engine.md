---
id: order-minting-single-engine
files: src/copilot/mirror.ts
---

`mintPreprodOrder(pre, spec)` in `src/copilot/mirror.ts` is the one and only order-minting
engine. It owns the sequence invariants: order names reset speciality/provider, `placeOfService`
is set LAST, and it posts-forReview with an E6001-tolerant `/process` retry. It **always stops
at forReview** — it never submits.

`create_preprod_order` is its only caller, minting from an explicit `MintSpec`. There is no
dedicated "clone" tool: the `clone-and-verify-order` prompt reads a prod order via `get_order`
and derives the spec fields itself — there is no automated cross-env reference matching; an
agent resolves prod names to pre-prod uids via `get_settings`/`diff_settings`.

Submitting is a fully separate step (`submitOrder`, backing `submit_preprod_order`), always
explicit, never implicit in minting.

The raw atoms (create draft / PUT field / process) must never be exposed as standalone tools.
The tool boundary is the safe transaction, not the HTTP verb.

**Why:** exposing the atoms individually would let a caller assemble an unsafe sequence (e.g.
setting placeOfService before the reset fields, or calling `/process` enough times to reach a
submitted state) that the engine's ordering was written specifically to prevent.

**Violation signature:** a new tool that wraps a single BE call (create draft, PUT a field,
`/process`) instead of going through `mintPreprodOrder`; a second code path that mints or
mutates a pre-prod order outside `mirror.ts`; any change that lets `create_preprod_order`
proceed past `forReview`.
