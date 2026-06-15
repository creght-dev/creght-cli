#!/usr/bin/env node

const { spawnSync } = require("node:child_process");
const path = require("node:path");

const platform = process.platform;
const arch = process.arch;
const exe = process.platform === "win32" ? "creght.exe" : "creght";
const binary = path.join(__dirname, "..", "vendor", `${platform}-${arch}`, exe);

const result = spawnSync(binary, process.argv.slice(2), {
  stdio: "inherit",
});

if (result.error) {
  if (result.error.code === "ENOENT") {
    console.error(
      "Cregh CLI binary is missing. Reinstall creght-cli and try again.",
    );
  } else {
    console.error(result.error.message);
  }
  process.exit(1);
}

if (typeof result.status === "number") {
  process.exit(result.status);
}

process.exit(result.signal ? 1 : 0);
