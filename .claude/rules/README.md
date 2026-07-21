# Rules

One file per hard invariant from `CLAUDE.md`'s "Invariants (do not break)" section, broken out
so a review pass (human or agent) can check one rule at a time instead of parsing the whole
block. Each file: the rule statement, **Why**, and a **Violation signature** — concrete things
to grep/read for that indicate the rule is broken.

These are consumed by `.claude/workflows/invariant-compliance-sweep.js`, and are useful input
for `/code-review` or any manual audit of a change touching a write path.

If an invariant in `CLAUDE.md` changes, update the matching file here too — they're meant to
stay in sync, not fork.

- [env-profile-required.md](env-profile-required.md)
- [stdout-jsonrpc-channel.md](stdout-jsonrpc-channel.md)
- [prod-preprod-isolation.md](prod-preprod-isolation.md)
- [order-minting-single-engine.md](order-minting-single-engine.md)
- [uipath-writes-guarded.md](uipath-writes-guarded.md)
- [settings-sync-plan-apply.md](settings-sync-plan-apply.md)
- [tool-response-contract.md](tool-response-contract.md)
