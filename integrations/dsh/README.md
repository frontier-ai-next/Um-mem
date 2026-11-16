# UM-MEM for DeepSeek Harness

A DSH host plugin (`dsh-um-mem`) that puts UM-MEM underneath a DeepSeek Harness
profile: the agent gets explicit memory tools plus a bounded pre-turn memory
block, and every write keeps UM-MEM's scope, revision, provenance and retraction
semantics.

The plugin is a thin adapter. It does not re-implement memory: it calls the
`um-memory` CLI and shapes the JSON into something a model can read.

## What the host gets

| Surface | Behaviour |
| --- | --- |
| `um_mem_recall` | semantic recall inside one workspace scope, compact records with `id` / `rev` |
| `um_mem_remember` | writes an advisory candidate into a logical block (`metadata` takes a JSON object; reserved governance keys are refused) |
| `um_mem_link` | typed edge between two records (`derived_from`, `supports`, `implements`, `cites`, `related_to`, `verified_by`, `tests`, `contains`), evidence supplied automatically |
| `um_mem_amend` | new revision of an existing record (`expected_revision` guards the race) |
| `um_mem_forget` | retracts a wrong record |
| `um_mem_read` | one full record by id |
| `um_mem_recent` | last records of the workspace, no query needed |
| `um_mem_health` | backend, workspace/profile, projection health |
| `systemPrompt.section` | static rules (byte-stable, so it does not break the prefix cache) |
| `systemPrompt.context` | per-turn focus block from the last user message, bounded and deduplicated |
| `GET /api/um-mem/health` | loopback JSON diagnostics for the local panel |

Records stay **advisory**: the plugin never promotes a candidate to directive
authority and never treats a recall hit as permission to act.

## Prerequisites

1. A `um-memory` binary. Either build it:

   ```console
   go build -tags purego -o bin/um-memory ./cmd/um-memory
   go build -tags purego -o bin/um-memory-mcp ./cmd/um-memory-mcp
   ```

   (Windows: append `.exe`) or install a release with `um-memory install`.

2. A store that passes `health`:

   ```console
   um-memory health --repo-root /absolute/path/to/store --workspace deepseek-harness --profile default --backend mnemosyne
   ```

## Install into a DSH profile

The package declares `dsh.bundle.patch`, so a normal bundle install works:

```console
dsh plugin add file:/absolute/path/to/Um-mem/integrations/dsh
```

or add it by hand to the profile `package.json` (both places, like the other
bundles):

```jsonc
{
  "dependencies": { "dsh-um-mem": "file:C:/path/to/Um-mem/integrations/dsh" },
  "dsh": { "profile": { "bundles": ["dsh-um-mem"] } }
}
```

`integrations/dsh/cordis.patch.yml` inserts the plugin row `um-mem`. To point the
plugin at a specific store and binary, give the row a `config` block:

```yaml
- insert:
    - id: um-mem
      name: dsh-um-mem
      config:
        binary: C:/Users/me/.um-mem/bin/um-memory.exe
        store: C:/Users/me/.um-mem/store
        workspace: deepseek-harness
        profile: default
        backend: mnemosyne
```

Restart the host after changing profile settings.

## Configuration

Every field is optional. Resolution order: plugin row `config` → environment →
default.

| Field | Environment | Default |
| --- | --- | --- |
| `binary` | `UM_MEM_BIN` (file or directory) | `~/.um-mem/bin/um-memory[.exe]` |
| `store` | `UM_MEM_STORE` | `~/.um-mem/store` |
| `workspace` | `UM_MEM_WORKSPACE` | slug of the host working directory |
| `profile` | `UM_MEM_PROFILE` | `default` |
| `backend` | `UM_MEM_BACKEND` | `mnemosyne` |
| `limit` | — | `8` records per recall |
| `focusLimit` | — | `12` records in the pre-turn block |
| `focusMaxChars` | — | `1400` characters for the pre-turn block |
| `timeoutMs` | — | `30000` per CLI call |
| `inject` | — | `true` |

The pre-turn block is dropped when it is older than 10 minutes, so a dead store
degrades to "no injection" instead of stale facts.

`userId` (environment `UM_MEM_USER_ID`) enables the `user_global` scope: the
recall scope chain becomes `workspace:<id>` + `user_global:<id>`, and the focus
block starts with user-level preferences. Without it the plugin stays
workspace-only, which is the right default for a shared machine.

## Verify a real journey

```console
curl http://127.0.0.1:<gui-port>/api/um-mem/health
```

Then, inside the host: ask something that requires a past decision and confirm
the agent calls `um_mem_recall`; state one durable fact and confirm
`um_mem_remember` returns `saved | record=mem_… | authority=advisory`; change
that fact with `um_mem_amend` and check the old revision is superseded.

## Uninstall

Remove the bundle from the profile `package.json` and delete the row that
`cordis.patch.yml` inserted. The store is untouched: it is plain SQLite plus
native partitions, and it stays readable by the CLI.
