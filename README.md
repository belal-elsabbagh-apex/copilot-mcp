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
| `diff_settings` | Diff an account's `/api/v1/settings/*` between prod and pre-prod (UID/timestamp noise stripped, lists matched by name). Scope with `groups` (top-level, e.g. `['orders']`) and/or `sections` (exact keys). READ-ONLY. |
| `list_setting_sections` | List the settings sections/groups `diff_settings` can compare (keys, labels, groups, kind). Use it to discover what to scope by. READ-ONLY, no network. |
| `sync_settings` | Additively add settings items that exist in prod but are missing in pre-prod (the `diff_settings` `onlyInProd` items) into pre-prod — additive only, never overwrites or deletes existing pre-prod settings. **STUB — not implemented** (returns a not-implemented error); use `diff_settings` to inspect what's missing and add it by hand for now. |
| `get_order` | Fetch a single order's normalized detail (status, insurance, ICD/CPT, facility, note presence) by uid in a chosen env. READ-ONLY. |
| `doctor` | Probe the server's external connections (Copilot BE prod + pre-prod login, UiPath Orchestrator per env) and report what's reachable. READ-ONLY. |

## Configuration

The server reads one validated config holding both the Copilot BE creds and the UiPath
args. Two ways to provide it:

1. **Single file (preferred):** set `COPILOT_MCP_CONFIG` to a JSON file shaped like
   [`config.example.json`](./config.example.json).
2. **Split legacy files:** set `COPILOT_MCP_LOCAL_DIR` to a directory containing
   `order-copy-credentials.json` + `uipath-config.json` (+ optional `overrides.json`).

The `uipath.noteBucket / queueUrl / addQueueItemPath / serverUrlByEnv` fields are only
required by `build_queue_item`. `overrides` (per-prodUid clone remaps) is optional.

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
