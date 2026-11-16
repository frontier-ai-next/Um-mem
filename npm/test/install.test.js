"use strict";

const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const test = require("node:test");
const installer = require("../scripts/install.js");

const programs = ["um-memory", "um-memory-mcp", "um-memory-service", "um-memory-hook"];

test("resolves release assets from the internal GitLab project", () => {
  const previous = process.env.UM_MEM_GITLAB_PROJECT_ID;
  try {
    delete process.env.UM_MEM_GITLAB_PROJECT_ID;
    assert.equal(installer.releaseAssetURL("v0.7.0-beta.1", "SHA256SUMS"),
      "https://gitlab.frontierai.ru/api/v4/projects/" +
      "home%2Ffrontierai%2Fteams%2Fmag%2Ffrontier-memory%2Fumbrella-memory%2Fum-mem-module" +
      "/packages/generic/um-mem-go/v0.7.0-beta.1/SHA256SUMS");
    process.env.UM_MEM_GITLAB_PROJECT_ID = "12345";
    assert.match(installer.releaseAssetURL("v0.7.0-beta.1", "SHA256SUMS"),
      /\/projects\/12345\/packages\/generic\//);
    process.env.UM_MEM_GITLAB_PROJECT_ID = "../other";
    assert.throws(() => installer.releaseAssetURL("v0.7.0-beta.1", "SHA256SUMS"),
      /project ID or path is invalid/);
  } finally {
    if (previous === undefined) delete process.env.UM_MEM_GITLAB_PROJECT_ID;
    else process.env.UM_MEM_GITLAB_PROJECT_ID = previous;
  }
});

test("installs only verified Go binaries and keeps the previous install on failure", async (context) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "um-mem-npm-go-test-"));
  const priorHome = process.env.UM_MEM_HOME;
  process.env.UM_MEM_HOME = path.join(root, "home");
  context.after(() => {
    if (priorHome === undefined) delete process.env.UM_MEM_HOME;
    else process.env.UM_MEM_HOME = priorHome;
    const resolved = path.resolve(root);
    if (resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) &&
        path.basename(resolved).startsWith("um-mem-npm-go-test-")) {
      fs.rmSync(resolved, { recursive: true, force: true });
    }
  });
  const assets = path.join(root, "assets");
  fs.mkdirSync(assets);
  const checksums = [];
  for (const program of programs) {
    const name = installer.assetName(program);
    const data = Buffer.from(`Go test binary for ${program}\n`);
    fs.writeFileSync(path.join(assets, name), data);
    checksums.push(`${crypto.createHash("sha256").update(data).digest("hex")}  ${name}`);
  }
  fs.writeFileSync(path.join(assets, "SHA256SUMS"), checksums.join("\n") + "\n");
  assert.equal(await installer.ensureEngine(["--from", assets]), 0);
  const first = installer.engineBin();
  assert.match(fs.readFileSync(first, "utf8"), /Go test binary/);
  assert.equal(JSON.parse(fs.readFileSync(path.join(process.env.UM_MEM_HOME, "npm.json"))).runtime, "go");
  assert.ok(fs.existsSync(installer.mcpBin()));
  assert.ok(fs.existsSync(installer.serviceBin()));
  assert.ok(fs.existsSync(installer.hookBin()));

  assert.equal(await installer.ensureEngine(["--from", assets]), 0);
  assert.notEqual(installer.engineBin(), first);
  assert.equal(await installer.ensureEngine(["--from", assets, "--version", "v0.7.0-beta.2"]), 0);
  const upgraded = installer.engineBin();
  assert.notEqual(upgraded, first);
  assert.equal(JSON.parse(fs.readFileSync(path.join(process.env.UM_MEM_HOME, "npm.json"))).version,
    "v0.7.0-beta.2");

  fs.writeFileSync(path.join(assets, installer.assetName("um-memory-mcp")), "tampered\n");
  await assert.rejects(installer.ensureEngine(["--from", assets, "--force"]), /checksum mismatch/);
  assert.equal(installer.engineBin(), upgraded);
  assert.match(fs.readFileSync(upgraded, "utf8"), /Go test binary/);
});

test("builds and runs the Go engine from a source checkout", {
  skip: !process.env.UM_MEM_GO_TEST_SOURCE,
}, async (context) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "um-mem-npm-go-source-test-"));
  const priorHome = process.env.UM_MEM_HOME;
  process.env.UM_MEM_HOME = path.join(root, "home");
  context.after(() => {
    if (priorHome === undefined) delete process.env.UM_MEM_HOME;
    else process.env.UM_MEM_HOME = priorHome;
    const resolved = path.resolve(root);
    if (resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) &&
        path.basename(resolved).startsWith("um-mem-npm-go-source-test-")) {
      fs.rmSync(resolved, { recursive: true, force: true });
    }
  });
  const checkout = path.resolve(__dirname, "../..");
  assert.equal(await installer.ensureEngine(["--from", checkout]), 0);
  const result = spawnSync(process.execPath,
    [path.resolve(__dirname, "../bin/um-memory.js"), "health", "--repo-root", root,
      "--workspace", "alpha", "--backend", "mnemosyne"],
    { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  const health = JSON.parse(result.stdout);
  assert.equal(health.backend, "mnemosyne");
  assert.equal(health.native_ok, true);
});
