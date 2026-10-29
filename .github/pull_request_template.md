## What changed

<!-- The change itself, in a sentence or two. -->

## Why

<!-- The reason it needed changing. If it fixes an issue, link it: Fixes #N -->

## Checklist

- [ ] `make check` passes locally
- [ ] `CHANGELOG.md` has an entry under `## [Unreleased]`
- [ ] The MCP surface (tool/prompt/resource names, input schemas, result shapes) is unchanged, or the change is documented in the changelog
- [ ] Releases are cut with `make release VERSION=X.Y.Z`, never by hand-pushing a tag
