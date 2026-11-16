# Hermes integration

UM-MEM connects Hermes to the Go runtime through a stdio MCP server and
external shell hooks. Hermes keeps its own built-in memory configuration;
installing UM-MEM does not replace or delete it. The Go hook captures bounded,
content-free lifecycle facts and can return advisory context at
`pre_llm_call`. The working agent remains responsible for semantic writes,
corrections, and a valid no-write decision.

## Install

Build or install the four Go programs as described in [Install](install.md),
then use the same absolute store, workspace, profile, and selected backend in
both commands:

```console
um-memory install-mcp --host hermes --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne
um-memory-hook --install --host hermes --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne --project-dir /absolute/path/to/hermes-home
```

The hook installer edits Hermes' `config.yaml`, preserving other settings and
unmanaged hooks. `--project-dir` is the Hermes configuration home. Restart
Hermes, then inspect both saved entries:

```console
um-memory mcp-status --host hermes
um-memory-hook --status --host hermes --project-dir /absolute/path/to/hermes-home
```

Status reads configuration. Qualify an actual `pre_llm_call` hook invocation,
an MCP tool call, a write, a later recall, and a task that should save nothing
before relying on automatic memory in a Hermes profile.

## Lifecycle and scope

The Go hook recognizes Hermes session, turn, delegation, tool, and
`pre_llm_call` events. It sends host-owned lifecycle facts to the local Go
service without treating event text as a verified semantic record. At a
pre-model boundary it may return a bounded advisory context for the exact
session. A hook event or prepared context is not proof that the model read or
used it.

The MCP process binds its configured workspace and profile. Trusted Hermes
session and project identifiers should be supplied by the host; model
arguments cannot claim another scope. A server process without a stable host
session ID generates one for its own lifetime. To share session-scoped
candidates across process restarts, configure a stable trusted session ID.

Use `block_map` and `expand_block` to navigate before a scoped `recall`. A
temporary recall does not mutate the active mask. A new session's empty mask
does not mean the durable tree is empty. When the chosen leaves do not contain
the evidence, the agent may deliberately broaden the search.

## Current integration boundary

Hermes loads native memory-provider plugins inside Hermes' own runtime. The
Go integration uses its external hook and MCP surfaces instead. These
surfaces expose memory tools and the supported host boundaries, but do not
implement every callback of the old in-process provider. In particular, a
configured MCP entry alone does not supply a native provider's per-round
prefetch or compaction callback. Qualify those lifecycle requirements in the
target Hermes version before replacing an existing provider deployment.
