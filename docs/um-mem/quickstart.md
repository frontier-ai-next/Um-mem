# Quickstart

UM-MEM runs as four Go programs: `um-memory`, `um-memory-mcp`,
`um-memory-service`, and `um-memory-hook`. The OpenCode plugin is TypeScript.

## Build and check a local workspace

From a checkout with Go 1.27 or newer, build all four programs or install them
through the npm launcher as described in the [installation guide](install.md).
Keep the programs in one directory. Choose an absolute store path and a
workspace name, then check the selected native backend:

```console
um-memory health --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne
um-memory catalog --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne
```

The catalog shows the logical blocks available to the agent. `native_ok: true`
in `health` confirms that the selected backend can be reached. It does not
confirm that a host has loaded its MCP server or hook.

## Connect a host

For Codex, Claude Code, or Hermes, install the MCP entry with the same store,
workspace, profile, and backend:

```console
um-memory install-mcp --host codex --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne
um-memory mcp-status --host codex
```

Change `--host codex` to `claude` or `hermes` as needed. If the host should
capture lifecycle events or provide bounded context at a prompt boundary,
install its Go hook too:

```console
um-memory-hook --install --host codex --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne --project-dir /absolute/path/to/project
um-memory-hook --status --host codex --project-dir /absolute/path/to/project
```

Restart the host after changing its configuration. For OpenCode, use the
TypeScript plugin guide in `integrations/opencode/README.md`; it connects
to the Go MCP server and binds the host's project identity to a session.

## Verify the memory loop

In the connected host, complete a task with one reusable fact or decision.
Inspect the records, then ask a later session to recall the fact:

```console
um-memory list-records --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne --scope workspace --scope-id my-project
```

Complete a trivial task that should save nothing as a second check. The agent
may correctly decide to write no record. MCP tools, hooks, and host
instructions must all be exercised in the actual host to establish automatic
behavior.

## Continue

- [Mental model](memory_architecture.md) explains records, blocks, scope, and
  advisory delivery.
- [Interfaces](interfaces.md) describes the Go API, MCP tools, CLI, and host
  adapters.
- [Backends](backends.md) covers native provider requirements and recovery.
