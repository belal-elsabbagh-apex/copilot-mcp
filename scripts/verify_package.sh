#!/usr/bin/env bash
# Proves the npm package shape before a tag does: the launcher starts the host binary and the
# server answers `initialize`, and the tarball contains every platform binary and nothing the
# `files` list does not intend. Expects `make dist` to have run (`make package` does).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

PLATFORMS=(linux-amd64 darwin-arm64 windows-amd64)

# The launcher runs the host's binary, so a clean `initialize` here covers the platform table for
# this machine; the tarball check below covers the rest.
init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"verify","version":"1"}}}'
# Stdin must stay open past the response: the SDK stops reading on EOF and can exit before it has
# answered, so the sleep holds the pipe, then closing it is what tells the server to exit.
out="$({ printf '%s\n' "${init}"; sleep 2; } | timeout 20 node bin/copilot-mcp.js)"
name="$(printf '%s' "${out}" | jq -r '.result.serverInfo.name // empty')"
[ "${name}" = "copilot" ] || { echo "FAIL: initialize gave serverInfo.name='${name}'; output: ${out:0:400}" >&2; exit 1; }
# stdout must be nothing but JSON-RPC: one parseable line.
[ "$(printf '%s\n' "${out}" | wc -l)" -eq 1 ] || { echo "FAIL: stdout carried more than the initialize response" >&2; exit 1; }

# npm 12 keys the --json output by package name, older npm returns an array; jq accepts both.
files="$(npm pack --dry-run --json 2>/dev/null | jq -r '(if type == "array" then .[0] else .[] end).files[].path')"
for p in "${PLATFORMS[@]}"; do
	bin="dist/${p}/copilot-mcp"
	[ "${p%%-*}" = windows ] && bin+=".exe"
	grep -qx "${bin}" <<<"${files}" || { echo "FAIL: tarball is missing ${bin}" >&2; exit 1; }
done
unexpected="$(grep -vxE 'bin/copilot-mcp\.js|dist/[a-z0-9]+-[a-z0-9]+/copilot-mcp(\.exe)?|package\.json|README\.md|copilot-mcp\.config\.example\.json|LICENSE.*' <<<"${files}" || true)"
[ -z "${unexpected}" ] || { echo "FAIL: unexpected files in tarball:" >&2; echo "${unexpected}" >&2; exit 1; }

# The shipped path: users run `npx -y <release asset URL>`. Serve the packed tarball over HTTP and
# cold-install it (fresh npm cache) exactly that way.
work="$(mktemp -d)"
server_pid=""
trap '[ -z "${server_pid}" ] || kill "${server_pid}" 2>/dev/null; rm -rf "${work}"' EXIT
npm pack --pack-destination "${work}" >/dev/null 2>&1
cp "${work}"/*.tgz "${work}/copilot-mcp.tgz"
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
python3 -m http.server "${port}" --bind 127.0.0.1 --directory "${work}" >/dev/null 2>&1 &
server_pid=$!
for _ in $(seq 50); do curl -fs -o /dev/null "http://127.0.0.1:${port}/copilot-mcp.tgz" && break; sleep 0.1; done
# Longer hold than above: the cold install runs before the server ever sees stdin.
url_out="$({ printf '%s\n' "${init}"; sleep 8; } | npm_config_cache="${work}/npm-cache" timeout 120 npx -y "http://127.0.0.1:${port}/copilot-mcp.tgz")"
url_name="$(printf '%s' "${url_out}" | jq -r '.result.serverInfo.name // empty')"
[ "${url_name}" = "copilot" ] || { echo "FAIL: npx URL install gave serverInfo.name='${url_name}'; output: ${url_out:0:400}" >&2; exit 1; }
echo "verify_package: npx URL install ok ($(du -h "${work}/copilot-mcp.tgz" | cut -f1) tarball)" >&2

echo "verify_package: ok (serverInfo.name=${name}, ${#PLATFORMS[@]} binaries packed)" >&2
