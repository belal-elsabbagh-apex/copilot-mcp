#!/usr/bin/env node
// Launches the bundled Go binary for this platform. Node builtins only: this file is the whole
// npm-side runtime. stdout belongs to the child's JSON-RPC, so every message here goes to stderr.
"use strict";

const { spawn } = require("node:child_process");
const fs = require("node:fs");
const nodeOs = require("node:os");
const path = require("node:path");

// The shipped platforms (Makefile PLATFORMS): Node's `${platform}/${arch}` -> [goos, goarch]. The
// layout is dist/<goos>-<goarch>/copilot-mcp[.exe] (Makefile `dist`).
const SUPPORTED = {
  "linux/x64": ["linux", "amd64"],
  "darwin/arm64": ["darwin", "arm64"],
  "win32/x64": ["windows", "amd64"],
};

const target = SUPPORTED[`${process.platform}/${process.arch}`];
if (!target) {
  console.error(
    `copilot-mcp: unsupported platform ${process.platform}/${process.arch} (supported: ${Object.keys(SUPPORTED).join(", ")})`,
  );
  process.exit(1);
}
const [os, arch] = target;

const exe = path.join(
  __dirname,
  "..",
  "dist",
  `${os}-${arch}`,
  os === "windows" ? "copilot-mcp.exe" : "copilot-mcp",
);
if (!fs.existsSync(exe)) {
  console.error(`copilot-mcp: no bundled binary for ${os}/${arch} at ${exe}`);
  process.exit(1);
}

const isExecutable = (p) => {
  try {
    fs.accessSync(p, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
};

// npm can drop the exec bit when it unpacks a tarball. Only chmod when it is actually missing, so a
// read-only install (a shared npx cache) still starts.
if (os !== "windows" && !isExecutable(exe)) {
  try {
    fs.chmodSync(exe, 0o755);
  } catch (err) {
    console.error(`copilot-mcp: cannot make ${exe} executable: ${err.message}`);
    process.exit(1);
  }
}

const child = spawn(exe, process.argv.slice(2), { stdio: "inherit" });
child.on("error", (err) => {
  console.error(`copilot-mcp: cannot start ${exe}: ${err.message}`);
  process.exit(1);
});

for (const sig of ["SIGINT", "SIGTERM"]) {
  process.on(sig, () => child.kill(sig));
}

child.on("exit", (code, signal) => {
  // A signal-killed child has no exit code; mirror the shell's 128+n convention.
  process.exit(code ?? 128 + (nodeOs.constants.signals[signal] ?? 0));
});
