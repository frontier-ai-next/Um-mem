#!/usr/bin/env node
"use strict";

const fs = require("fs");
const { spawnSync } = require("child_process");
const { engineBin, ensureEngine } = require("../scripts/install.js");

const args = process.argv.slice(2);

if (args[0] === "install") {
  ensureEngine(args.slice(1)).then(
    (status) => { process.exitCode = status; },
    (error) => { console.error(`UM-MEM Go install failed: ${error.message}`); process.exitCode = 1; },
  );
  return;
}

if (!fs.existsSync(engineBin())) {
  console.error("UM-MEM is installed as an npm launcher, but its Go engine is not.");
  console.error("");
  console.error("Run:");
  console.error("  um-memory install");
  console.error("");
  console.error("Then connect your host with 'um-memory install-mcp'.");
  process.exit(1);
}

const result = spawnSync(engineBin(), args, { stdio: "inherit", env: process.env });
if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}
process.exit(result.status == null ? 1 : result.status);
