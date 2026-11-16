#!/usr/bin/env node
"use strict";
process.exitCode = require("../scripts/run.js").run("um-memory-hook", process.argv.slice(2));
