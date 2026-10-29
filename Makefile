.PHONY: build dist npm-pack mcpb package test vet fmt lint lint-actions check hooks release

# The semver tag this build reports as the MCP serverInfo version (cmd/copilot-mcp/main.go's
# `version`). Derived from the git tag rather than stored in a file, so there is no version string
# to forget to bump. release.yml checks out with fetch-depth: 0 so tags resolve; outside a tagged
# tree it degrades to a bare SHA (or "dev"). package.json/manifest.json stay at 0.0.0-dev in git:
# release.yml stamps the tag's version into them at release time. The leading v is stripped so
# serverInfo.version is a bare semver (2.0.0).
VERSION ?= $(shell { git describe --tags --always --dirty 2>/dev/null || echo dev; } | sed 's/^v//')

# CGO off so each binary is static and the cross-compiles need no C toolchain; -trimpath keeps
# builder paths out of the binary; -s -w drops symbols to roughly halve its size (it ships 3x).
GOFLAGS_BUILD = -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

# goos/goarch pairs the npm launcher (bin/copilot-mcp.js) knows how to pick.
PLATFORMS = linux/amd64 darwin/arm64 windows/amd64

build:
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o copilot-mcp ./cmd/copilot-mcp

# Layout the launcher expects: dist/<goos>-<goarch>/copilot-mcp[.exe]. Rebuilt from scratch so a
# platform dropped from PLATFORMS cannot linger in the tarball.
dist:
	rm -rf dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
		echo "dist/$$os-$$arch/copilot-mcp$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS_BUILD) \
			-o "dist/$$os-$$arch/copilot-mcp$$ext" ./cmd/copilot-mcp || exit 1; \
	done

npm-pack: dist
	npm pack

# MCP Bundle for MCPB hosts (Claude Desktop). Staged in a scratch dir so only the launcher, the
# binaries and the manifest go in. Pinned: an unpinned `npx mcpb` would make release builds
# non-reproducible. Needs the version already stamped into manifest.json (release.yml does).
MCPB ?= @anthropic-ai/mcpb@2.1.2

mcpb: dist
	rm -rf .mcpb-stage copilot-mcp.mcpb
	mkdir -p .mcpb-stage/bin
	cp -r dist .mcpb-stage/dist
	cp bin/copilot-mcp.js .mcpb-stage/bin/
	cp manifest.json copilot-mcp.config.example.json .mcpb-stage/
	npx -y $(MCPB) pack .mcpb-stage copilot-mcp.mcpb
	rm -rf .mcpb-stage

# -race, not a bare run: tool calls run concurrently over shared session/folder caches, so a data
# race is a production bug class this catches and plain `go test` cannot see.
test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needs to be run on:"; gofmt -l .; exit 1)

# Pinned, not @latest: an unpinned linter makes CI non-reproducible. `go run pkg@version` does not
# touch go.mod, so these stay out of the module graph.
ACTIONLINT ?= github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
# golangci-lint refuses to run when the Go it was built with is older than this module's go
# directive ("Go language version ... is lower than the targeted Go version"), and its bundled
# x/tools can only read export data up to some Go release: v2.13.1 fails on go1.27.3 with "export
# data version 5 is greater than maximum supported version 4"; v2.15.0 is verified on go1.27.3.
# Whenever the go directive or check.yml's go-version moves, re-verify this pin.
GOLANGCI ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

lint:
	go run $(GOLANGCI) run

# Weaker locally than in CI unless shellcheck is installed: actionlint shells out to it for every
# `run:` block when it is on PATH, which it is on GitHub runners. Hence the warning rather than a
# silent pass -- a shell bug in a workflow would otherwise surface only after the push.
lint-actions:
	@command -v shellcheck >/dev/null || echo "warning: shellcheck not on PATH; actionlint will skip the shell in every run: block (CI will not)" >&2
	go run $(ACTIONLINT) .github/workflows/*.yml

# Builds every platform binary and proves the npm package: the launcher answers `initialize` and
# the tarball carries all binaries. Full cross-build rather than host-only because the launcher's
# platform table and the tarball contents are exactly what a host-only run cannot catch; with a
# warm build cache the three builds cost seconds.
package: dist
	@scripts/verify_package.sh

# The whole gate, and the only definition of it: check.yml runs this target rather than restating
# the steps, so a clean run here is a green CI run. Anything added to CI belongs here instead.
check: vet fmt lint lint-actions test package
	@scripts/release.sh --self-test

# Install the pre-push hook. core.hooksPath is repo-wide config, so this covers every worktree at
# once. Opt-in by design -- cloning the repo does not silently gain a hook.
hooks:
	git config core.hooksPath .githooks
	@echo "pre-push hook installed: .githooks/pre-push runs make check"

# Cut a release: VERSION is the semver without the leading v. Only pushes the tag; release.yml does
# the rest. DRY_RUN=1 prints the plan without tagging.
#   make release VERSION=2.1.0
release:
	@scripts/release.sh --version "$(VERSION)" $(if $(DRY_RUN),--dry-run)
