#!/usr/bin/env node
"use strict";
process.exitCode = require("../scripts/run.js").run("um-memory-mcp", process.argv.slice(2));
