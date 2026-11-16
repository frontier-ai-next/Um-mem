"use strict";

const fs = require("fs");
const { spawnSync } = require("child_process");
const installer = require("./install.js");

function run(name, args) {
  const paths = { "um-memory-mcp": installer.mcpBin,
    "um-memory-service": installer.serviceBin, "um-memory-hook": installer.hookBin };
  const binary = paths[name]();
  if (!fs.existsSync(binary)) {
    console.error("UM-MEM Go binaries are missing. Run 'um-memory install'.");
    return 1;
  }
  const result = spawnSync(binary, args, { stdio: "inherit", env: process.env });
  if (result.error) {
    console.error(result.error.message);
    return 1;
  }
  return result.status == null ? 1 : result.status;
}

module.exports = { run };
