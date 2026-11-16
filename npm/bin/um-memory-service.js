#!/usr/bin/env node
"use strict";
process.exitCode = require("../scripts/run.js").run("um-memory-service", process.argv.slice(2));
