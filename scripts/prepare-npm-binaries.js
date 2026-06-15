#!/usr/bin/env node

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const packageRoot = path.join(__dirname, "..");
const distDir = path.join(packageRoot, "dist");
const vendorDir = path.join(packageRoot, "vendor");
const version = JSON.parse(fs.readFileSync(path.join(packageRoot, "package.json"), "utf8")).version;

const targets = [
  { goos: "darwin", goarch: "amd64", npm: "darwin-x64", ext: "tar.gz", exe: "creght" },
  { goos: "darwin", goarch: "arm64", npm: "darwin-arm64", ext: "tar.gz", exe: "creght" },
  { goos: "linux", goarch: "amd64", npm: "linux-x64", ext: "tar.gz", exe: "creght" },
  { goos: "linux", goarch: "arm64", npm: "linux-arm64", ext: "tar.gz", exe: "creght" },
  { goos: "windows", goarch: "amd64", npm: "win32-x64", ext: "zip", exe: "creght.exe" },
  { goos: "windows", goarch: "arm64", npm: "win32-arm64", ext: "zip", exe: "creght.exe" },
];

fs.rmSync(vendorDir, { recursive: true, force: true });
fs.mkdirSync(vendorDir, { recursive: true });

for (const target of targets) {
  const archive = path.join(distDir, `creght_${version}_${target.goos}_${target.goarch}.${target.ext}`);
  if (!fs.existsSync(archive)) {
    throw new Error(`missing release archive: ${archive}`);
  }

  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "creght-npm-"));
  try {
    extract(archive, tmpDir, target.ext);
    const source = path.join(tmpDir, target.exe);
    if (!fs.existsSync(source)) {
      throw new Error(`archive did not contain ${target.exe}: ${archive}`);
    }

    const targetDir = path.join(vendorDir, target.npm);
    fs.mkdirSync(targetDir, { recursive: true });
    const destination = path.join(targetDir, target.exe);
    fs.copyFileSync(source, destination);
    if (target.exe === "creght") {
      fs.chmodSync(destination, 0o755);
    }
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
}

function extract(archive, destination, ext) {
  const command = ext === "zip" ? "unzip" : "tar";
  const args = ext === "zip" ? ["-q", archive, "-d", destination] : ["-xzf", archive, "-C", destination];
  const result = spawnSync(command, args, { stdio: "inherit" });

  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(`${command} exited with status ${result.status}`);
  }
}
