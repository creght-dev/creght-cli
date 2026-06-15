#!/usr/bin/env node

const fs = require("node:fs");
const path = require("node:path");

const packageRoot = path.join(__dirname, "..");
const platform = process.platform;
const arch = process.arch;
const exe = platform === "win32" ? "creght.exe" : "creght";
const binary = path.join(packageRoot, "vendor", `${platform}-${arch}`, exe);
const checkOnly = process.argv.includes("--check");

if (checkOnly) {
  const exists = fs.existsSync(binary);
  console.log(`Cregh CLI binary path for this platform: ${binary}`);
  if (!exists) {
    console.log("Binary is not present in this local checkout. CI adds release binaries before npm publish.");
  }
  process.exit(0);
}

if (!fs.existsSync(binary)) {
  console.error(
    `Cregh CLI binary is missing for ${platform}-${arch}. Reinstall creght-cli and try again.`,
  );
  process.exit(1);
}
