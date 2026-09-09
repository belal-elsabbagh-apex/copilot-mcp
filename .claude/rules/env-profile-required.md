---
id: env-profile-required
files: src/server.ts, src/config/config.ts, src/mcp/prompts.ts
---

Every Copilot tool/prompt operation requires an explicit `env` (`prod` | `pre_prod`) and
`profile` argument. Neither has a default; neither may be silently assumed.

UiPath-only tools (`list_jobs`, `get_job_logs`, `get_job`, `list_queues`, `list_processes`,
`list_triggers`, `build_faulted_job_issue`, `add_queue_item`, `delete_queue_item`, `start_job`,
`stop_job`) are one documented exception: they take `env` but no `profile`, because UiPath
authenticates globally via `uipath.oauth`/`uipath.bearer`, not per-profile creds. `list_clinics`
is the other: it takes `env` but no `profile`, because it's the discovery tool that produces the
profile → clinicUid mapping (requiring a profile would be circular).

`profile` must be validated against the loaded config via `resolveAuth(profile, env)` (or
`resolveSupport(env)` for the support-account credentials themselves) at call time. Profile
names are dynamic (read via `listProfiles()`) — never hardcode a profile name as a fallback or
default.

**Why:** hardcoding or defaulting either value risks a call silently landing in the wrong
tenant/environment.

**Violation signature:** a new Copilot tool schema where `env` or `profile` is `.optional()`
or has a `.default(...)`; a handler that falls back to a literal env/profile string instead of
requiring the caller to pass one; a new tool that isn't in the UiPath-only/`list_clinics`
exception list but omits `profile`.
