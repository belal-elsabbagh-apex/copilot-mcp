# @belal-elsabbagh-apex/copilot-mcp

MCP server exposing EHR Copilot operations over stdio. Built with [Bun](https://bun.sh)
(`bun build` bundles `src/` → a node-runnable `dist/server.js`), so it runs under either
`bunx` **or** `npx`. Published as a **private** package on GitHub Packages.

## Tools

| Tool | What it does |
|------|--------------|
| `clone_order` | Mirror PROD order(s) into PRE-PROD as fresh orders (clone-only by default; `submit` is opt-in). |
| `find_clone_candidates` | List recent PROD orders that will actually clone to forReview. |
| `delete_preprod_order` | Delete order(s) from PRE-PROD (never touches prod). |
| `build_queue_item` | Build a UiPath AddQueueItem request (payload + curl) from an order. BUILD ONLY — `IsApproved` is always `false`. |
| `analyze_order_execution` | Trace an order to its UiPath Orchestrator job(s) and diagnose the run. READ-ONLY. |
| `build_faulted_job_issue` | Read a faulted UiPath job (by Key) + its robot logs and BUILD a ready-to-post GitHub issue payload (`title`/`body`/`labels`/`faultSignature`/`searchQuery`/`recurrenceComment`) for the RPA repo. READ-ONLY — it does **not** post to GitHub; the caller hands the payload to a GitHub MCP server. |
| `diff_settings` | Diff an account's `/api/v1/settings/*` between prod and pre-prod (UID/timestamp noise stripped, lists matched by name). Scope with `groups` (top-level, e.g. `['orders']`) and/or `sections` (exact keys). READ-ONLY. |
| `list_setting_sections` | List the settings sections `diff_settings`/`sync_settings` can scope to (key, label, group, kind, derived, and each list section's `matchKey`). Narrow with `group` and/or exact `sections` keys. Use it to drive fine-grained, section-level scoping rather than broad `groups`. READ-ONLY, no network. |
| `sync_settings` | Additively copy settings that exist in prod but are missing in pre-prod into pre-prod — additive only (never overwrites or deletes existing pre-prod settings), PRE-PROD only, **dry-run by default** (returns the planned create/merge actions; pass `dryRun:false` to apply). Currently covers the outbound order-type **specialties** domain (`specialties` / `referred-providers` / `referred-facilities`): creates prod-only specialties and merges prod-only facilities/providers into specialties that already exist in pre-prod, remapping payer references prod→pre. Sections without a verified write endpoint are reported under `skippedSections`. |
| `get_order` | Fetch a single order's normalized detail (status, insurance, ICD/CPT, facility, note presence) by uid in a chosen env, plus `documents`: clickable CDN links that exist for the order — the auth-screenshot PDF (when `hasAuthScreenshot`) and medical-authorization summary PDF(s). The CDN is CloudFront signed-cookie protected, so links open only in an authenticated Copilot browser session (never fetched/probed server-side). READ-ONLY. |
| `doctor` | Probe the server's external connections (Copilot BE prod + pre-prod login, UiPath Orchestrator per env) and report what's reachable. READ-ONLY. |

## Prompts

User-invokable workflow prompts chain the tools above for common ops tasks:
`diagnose-order`, `reconcile-settings`, `clone-and-verify-order`, `triage-stuck-orders`, and
`report-faulted-uipath-jobs`.

`report-faulted-uipath-jobs` finds faulted UiPath jobs (prod by default) and files each as a
GitHub issue on `Apex-Medical-AI-Inc/RPAPlaywright` — creating one issue per distinct fault
and commenting on the existing issue when the same fault recurs. This server holds **no
GitHub credentials**: the prompt drives a **GitHub MCP server connected in the host** to do
the actual search/create/comment, so that server must be connected with write access to the
repo.

## Configuration

The server reads one validated config holding both the Copilot BE creds and the UiPath
args. Two ways to provide it:

1. **Single file (preferred):** set `COPILOT_MCP_CONFIG` to a JSON file shaped like
   [`config.example.json`](./config.example.json).
2. **Split legacy files:** set `COPILOT_MCP_LOCAL_DIR` to a directory containing
   `order-copy-credentials.json` + `uipath-config.json` (+ optional `overrides.json`).

The `uipath.noteBucket / queueUrl / addQueueItemPath / serverUrlByEnv` fields are only
required by `build_queue_item`. `overrides` (per-prodUid clone remaps) is optional.

When a tool fails for a reason that looks like a bug in this server (an unexpected
exception — not a bad profile, missing/invalid config, not-found, auth, or any HTTP error
the upstream system returned), the error response includes a `reportIssue` block with a
prefilled GitHub new-issue URL. This is on by default; the optional `feedback` block turns
it off or repoints it:

```json
"feedback": { "enabled": false, "repositoryUrl": "https://github.com/your-org/your-fork" }
```

Other env vars: `COPILOT_MCP_DEBUG_DIR` (where `clone_order` dumps the extracted prod
order JSON; defaults to the OS temp dir).

## Local development

```bash
bun install          # also installs the lefthook pre-commit hook (via `prepare`)
bun run typecheck
bun run lint         # biome check
bun test             # bun's built-in test runner (src/*.test.ts)
bun run build        # bundle src -> dist/server.js (node-runnable, what npx/bunx execute)
bun run start        # run from source via bun (no build needed)
bun run dev          # watch mode
```

### Exercising the tools

`bun run inspect` launches the [MCP Inspector](https://github.com/modelcontextprotocol/inspector)
against the server from source — a UI to list and call the tools without a host. Point it
at a config first (e.g. `COPILOT_MCP_CONFIG=… bun run inspect`).

Logging is silent by default so it never corrupts the stdio JSON-RPC stream. Set
`LOG_LEVEL=debug` (or `COPILOT_MCP_DEBUG=1`) to route progress logs to **stderr**.

A `pre-commit` hook (lefthook) runs `biome check` + `typecheck` on commit; CI
(`.github/workflows/ci.yml`) runs typecheck/lint/build/test on every PR and push to `main`.

## Installing from GitHub Packages (private)

The package is private, so installing requires a GitHub token with `read:packages`.

Add to the **consumer** repo's `.npmrc`:

```ini
@belal-elsabbagh-apex:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${NODE_AUTH_TOKEN}
```

Then `export NODE_AUTH_TOKEN=<your PAT>` and install:

```bash
bun add -d @belal-elsabbagh-apex/copilot-mcp
```

### Wire into `.mcp.json`

```json
{
  "mcpServers": {
    "copilot": {
      "command": "npx",
      "args": ["-y", "@belal-elsabbagh-apex/copilot-mcp"],
      "env": {
        "COPILOT_MCP_CONFIG": "/abs/path/to/config.local.json"
      }
    }
  }
}
```

`npx` and `bunx` are interchangeable here (the published bin, `dist/server.js`, is a
plain node ESM entry). Use `COPILOT_MCP_LOCAL_DIR` instead of `COPILOT_MCP_CONFIG` for
the split legacy files.

## Publishing

Publishing is tag-driven. Bump `version` in `package.json`, commit, then push a matching
`vX.Y.Z` tag:

```bash
git tag v1.2.0
git push origin v1.2.0
```

That one tag push fans out to two workflows (both keyed off the tag, because a
`GITHUB_TOKEN`-created release does not trigger other workflows):

- **Release on tag** (`release.yml`) — creates a GitHub Release with auto-generated notes.
- **Publish** (`publish.yml`) — typechecks, builds, and runs `bun publish --access
  restricted` using the repo's `GITHUB_TOKEN` (`packages: write`), keeping the package
  private. Also runnable manually via **workflow_dispatch**.
