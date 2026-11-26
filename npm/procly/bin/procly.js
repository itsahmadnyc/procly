#!/usr/bin/env node
"use strict";

// procly is a Go binary. npm installs the one for this machine as an optional
// dependency (procly-<os>-<cpu>); this shim finds it and hands over.

const { spawn } = require("child_process");

const packages = {
  "darwin arm64": "procly-darwin-arm64",
  "darwin x64": "procly-darwin-x64",
  "linux arm64": "procly-linux-arm64",
  "linux x64": "procly-linux-x64",
};

const key = `${process.platform} ${process.arch}`;
const pkg = packages[key];
if (!pkg) {
  console.error(`procly: no prebuilt binary for ${key}. procly runs on Linux and macOS, x64 and arm64.`);
  process.exit(1);
}

let bin;
try {
  bin = require.resolve(`${pkg}/bin/procly`);
} catch {
  console.error(
    `procly: the ${pkg} package is missing. It is installed as an optional dependency;\n` +
      `reinstall without --no-optional / --omit=optional:\n\n  npm install -g procly\n`
  );
  process.exit(1);
}

const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });

// Pass signals on: Ctrl-C reaches both of us through the terminal anyway, but
// `kill <pid>`, systemd and `timeout` only signal this process.
const forwarded = ["SIGINT", "SIGTERM", "SIGHUP", "SIGQUIT", "SIGUSR1", "SIGUSR2"];
for (const sig of forwarded) {
  process.on(sig, () => {
    if (child.exitCode === null && child.signalCode === null) child.kill(sig);
  });
}

child.on("error", (err) => {
  console.error(`procly: could not run ${bin}: ${err.message}`);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  if (signal) {
    // Die the same way, so callers see the real reason.
    for (const sig of forwarded) process.removeAllListeners(sig);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code === null ? 1 : code);
});
