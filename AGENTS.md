# UM-MEM development guide

UM-MEM is a Go memory runtime. The public module is `github.com/frontier-ai-next/Um-mem`.
The CLI, MCP server, service and lifecycle hooks live under `cmd/`; OpenCode is an optional TypeScript integration.

- Memory is advisory. Hosts own routing, permissions, proof validity and task completion.
- Durable writes use the canonical journal and exactly one selected native backend.
- Scope, provenance, promotion and lifecycle checks must stay enforced.
- Keep local stores, credentials, environment files, logs and generated binaries out of Git and release packages.
- Keep public installation independent of private repositories and package tokens.
- Verify Go tests and vet with and without `purego`, npm installation, OpenCode and panel tests.
- Releases must come from `main`, match `npm/package.json`, and include verified binaries and `SHA256SUMS`.
