---
id: env-profile-required
files: internal/server/tools_*.go, internal/server/prompts*.go, internal/server/schema.go, internal/config/resolve.go
---

Every Copilot tool/prompt operation requires an explicit `env` (`prod` | `pre_prod`) and
`profile` argument. Neither has a default; neither may be silently assumed. In Go a tool-args field
without `omitempty` is required in the JSON schema and a `Default(...)` patch (`internal/server/schema.go`)
is how a default would be added — so a default on `env` or `profile` is the violation.

Orchestrator tools (`list_jobs`, `get_job`, `get_job_logs`, `get_job_artifacts`, `build_faulted_job_issue`,
`list_queues`, `list_queue_items`, `find_order_queue_items`, `pull_queue_item`, `list_processes`,
`list_triggers`, `add_queue_item`, `delete_queue_item`, `start_job`, `stop_job`) are one documented
exception: they take `env` but no `profile`, because orchestrators authenticate globally (UiPath via
`uipath.oauth`/`uipath.bearer`, Apex via the one `apex` login), not per-profile creds. They also take a
required, never-defaulted `orchestrator` (`uipath` | `apex`); the only carve-outs are
`find_order_queue_items` / `find_stuck_orders` (omitted = every backend, `backends.SearchableBackends`) and the
Apex-only `get_job_artifacts`. `list_clinics` is the other profile exception: it takes `env` but no `profile`,
because it's the discovery tool that produces the profile → clinicUid mapping (requiring a profile would be
circular). The Copilot tools that fix their own env take a `profile` and no `env` argument:
`create_preprod_order`, `submit_preprod_order`, `delete_preprod_order` (connect to `pre_prod`),
`find_clone_candidates` (scans prod), and `diff_settings`, `plan_settings_sync`, `apply_settings_sync`, `doctor`
(address both envs). `list_setting_sections` and `build_mcp_issue` touch no tenant and take neither.

`profile` must be validated against the loaded config via `config.ResolveAuth(profile, env)` (or
`config.ResolveSupport(env)` for the support-account credentials themselves) at call time. Profile
names are dynamic (read via `config.ListProfiles()`) — never hardcode a profile name as a fallback or
default.

**Why:** hardcoding or defaulting either value risks a call silently landing in the wrong
tenant/environment.

**Violation signature:** a new Copilot tool-args struct where `env` or `profile` carries `omitempty` or a
`Default(...)` patch; a handler that falls back to a literal env/profile string instead of requiring the
caller to pass one; a new tool that isn't in the orchestrator/`list_clinics`/own-env exception lists above but
omits `profile` or `env`; a hardcoded profile name in non-test code.
