---
id: settings-sync-plan-apply
files: src/copilot/settings/sync.ts, src/copilot/settings/types.ts, src/copilot/settings/sections/
---

Settings sync is additive and pre-prod-only, split into a read-only plan step and a
re-planning apply step.

- `plan_settings_sync` is READ-ONLY and returns planned actions, each with a stable id
  (`section:op:typeName:itemName`).
- `apply_settings_sync` never accepts request bodies for the actions themselves — it
  **re-plans server-side** and executes only the actions selected by `actionIds` or an
  explicit `all: true`. Exactly one of those is required; there is deliberately no default
  "apply everything." A stale id after state drift lands in `unmatchedIds` instead of
  executing.
- It copies prod-only settings into pre-prod and never overwrites or deletes — `changed` and
  `onlyInPreProd` sets are left untouched.
- Two verified outbound domains today: **specialties** (create prod-only specialties, merge
  prod-only facilities/providers into existing ones via full-replace PUT) and **orders**
  (create prod-only orders under matching order types — create-only, no verified update
  endpoint).
- All env-specific references are remapped prod→pre **by name**, never stripped, never copied
  raw. Anything with no pre-prod match is dropped with a warning, never linked to the wrong
  entity.
- A section only gets sync behavior by attaching a `SectionSyncer` (`types.ts`) to its `sync`
  field, and only once its write endpoint is verified. Sections without one are reported under
  `skippedSections` automatically — `sync.ts`'s dispatch needs no changes to add a new section.

**Why:** the whole design exists to avoid two failure modes — silently overwriting a prod-only
customization in pre-prod, and linking a remapped reference to the wrong entity after a name
collision.

**Violation signature:** `apply_settings_sync` accepting a plan/body from the caller instead of
re-planning; a sync action that touches `changed` or `onlyInPreProd` items; a reference remap
that falls back to copying the raw prod uid when no by-name match exists in pre-prod; a new
`SectionSyncer` wired in before its endpoint has been verified against the live API.
