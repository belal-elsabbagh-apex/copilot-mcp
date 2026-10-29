#!/usr/bin/env bash
# Cuts a release: creates an annotated vX.Y.Z tag on origin/main and pushes ONLY that tag.
# release.yml does the rest (check, build, stamp, pack, GitHub release).
#
#   scripts/release.sh --version 2.1.0 [--dry-run]
#
# The tag is generated rather than typed so every release passes the same guards: it never moves an
# existing tag, never ships a version with no changelog entry, and never tags anything but the tip
# of origin/main. A tag containing '-' (2.1.0-rc.1) becomes a GitHub prerelease.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

SEMVER='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'

# Keep-a-Changelog heading for this exact version, e.g. "## [2.1.0] - 2026-11-01". Fixed-string
# match so the dots in the version are not regex wildcards.
changelog_has() {
	grep -qF "## [$1]" "${2:-CHANGELOG.md}"
}

# The only parsing logic here, so it leaves a check behind. `make check` runs this; needs no git.
self_test() {
	local dir
	dir="$(mktemp -d)"
	printf '## [Unreleased]\n\n## [2.1.0] - 2026-11-01\n' > "${dir}/c"
	changelog_has 2.1.0 "${dir}/c" || { echo "self-test: 2.1.0 should be found" >&2; exit 1; }
	! changelog_has 2.1 "${dir}/c" || { echo "self-test: 2.1 must not match 2.1.0" >&2; exit 1; }
	! changelog_has 2.1.1 "${dir}/c" || { echo "self-test: 2.1.1 must be absent" >&2; exit 1; }
	! changelog_has 2x1x0 "${dir}/c" || { echo "self-test: dots must be literal" >&2; exit 1; }
	for v in 2.1.0 0.0.1 2.1.0-rc.1; do
		[[ "${v}" =~ ${SEMVER} ]] || { echo "self-test: ${v} should be valid" >&2; exit 1; }
	done
	for v in "" 2.1 v2.1.0 2.1.0- 2.1.0.1; do
		[[ ! "${v}" =~ ${SEMVER} ]] || { echo "self-test: '${v}' should be invalid" >&2; exit 1; }
	done
	rm -rf "${dir}"
	echo "self-test: ok" >&2
}

VERSION=""
DRY_RUN=0
while [ $# -gt 0 ]; do
	case "$1" in
		--version) VERSION="${2:?--version needs a value}"; shift 2 ;;
		--dry-run) DRY_RUN=1; shift ;;
		--self-test) self_test; exit 0 ;;
		-h|--help) sed -n '2,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done

[[ "${VERSION}" =~ ${SEMVER} ]] || { echo "FAIL: --version must be X.Y.Z (no leading v), got '${VERSION}'" >&2; exit 2; }
TAG="v${VERSION}"

# Fetch first so every check below judges the remote, not a stale local view.
git fetch --quiet --tags origin main

if git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null; then
	echo "FAIL: tag ${TAG} already exists. Bump the version rather than moving a published tag." >&2
	exit 1
fi
changelog_has "${VERSION}" || { echo "FAIL: CHANGELOG.md has no '## [${VERSION}]' entry." >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "FAIL: working tree is dirty." >&2; exit 1; }
if [ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]; then
	echo "FAIL: HEAD is not origin/main; release from an up-to-date main." >&2
	exit 1
fi

echo "--- plan ---" >&2
echo "tag:      ${TAG}$([[ "${VERSION}" == *-* ]] && echo ' (prerelease)')" >&2
echo "commit:   $(git log -1 --format='%h %s' HEAD)" >&2
if [ "${DRY_RUN}" -eq 1 ]; then
	echo "--- dry run, nothing tagged ---" >&2
	exit 0
fi

git tag -a "${TAG}" -m "Release ${TAG}"
# Push only the tag, by full ref: a bare `git push --tags` would also publish any other local tag.
git push origin "refs/tags/${TAG}"
echo "--- pushed ${TAG}; release.yml takes it from here ---" >&2
