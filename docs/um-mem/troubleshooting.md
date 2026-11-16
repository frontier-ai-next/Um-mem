# Troubleshooting

Start with the exact store, workspace, profile, backend, and host settings used
by the failing session. Keep the store intact while identifying the layer that
failed.

## MCP tools are missing

Run `um-memory mcp-status --host codex` (or `claude` or `hermes`) and inspect
the host's own MCP listing after a restart. The status command reads saved
configuration; it does not prove that the host launched the server. The Go
`um-memory-mcp` program requires `--repo-root`, `--workspace`, `--profile`, and
`--backend`. Keep it beside `um-memory-service` so local service startup works.

## Tools work, but context is not automatic

MCP exposes explicit `recall`, `build_context`, and active-mask tools. It does
not inject context on every model request. Install the Go host hook separately
with `um-memory-hook --install`, restart the host, and check its native hook
status. Codex can receive bounded context at a prompt boundary; review hooks
can ask the same agent whether completed work produced a durable fact. A
correct no-write decision leaves the record count unchanged.

## Hermes integration is inactive

Hermes runs the Go external shell hooks and Go MCP server. Check
`um-memory-hook --status --host hermes --project-dir /absolute/path/to/hermes-home`
and `um-memory mcp-status --host hermes`, then run a real Hermes hook or prompt.
Confirm that both the external hook and Go MCP entry appear in Hermes' active
configuration; each supplies a different part of the integration.

## Native backend is unavailable

```console
um-memory health --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne
um-memory reconcile --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne --dry-run
```

Check the selected provider's service or model path and pending projections.
Use `reconcile --apply` only after understanding the dry-run report. A passing
canonical SQLite read does not establish native health.

## Recall returns no records

Check the workspace and profile, then the requested logical leaf or exact
record ID. Confirm that the record is current and that candidate visibility,
run ID, scope chain, temporal filters, token budget, and native partition all
permit the read. A managed MCP server without a host session ID generates one
for its own lifetime; a new server process has a different session. Use a
stable trusted host session ID when cross-process candidate continuity is
required. `search_all` is an explicit diagnostic or agent choice, not a
permanent substitute for block navigation.

## Active mask says `none` or `unavailable`

`none` means no mask is selected for this exact session and task. Navigate the
tree or use temporary recall. `unavailable` means the selected mask cannot be
materialized; inspect the lease and service diagnostics for a stale catalog
revision, inactive block, scope mismatch, backend failure, or timeout. Amend
the mask explicitly after fixing the cause.

## Existing Mnemosyne database needs rebuilding

A legacy shared native database can be incompatible with per-leaf partitioning.
The Go runtime preserves it and reports a rebuild requirement. Qualify any
reprojection on a copied store before changing a production workspace.
