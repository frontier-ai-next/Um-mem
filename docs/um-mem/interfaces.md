# Interfaces and host integration

UM-MEM has one canonical memory model exposed through a Go client, a CLI,
stdio MCP tools, a loopback service, and host hooks. The OpenCode adapter is
TypeScript. Choose the interface that matches the host lifecycle you control.

## Host contract

An autonomous host integration must bind a store, workspace, profile, selected
backend, and trusted session identity. It should offer a bounded read before
work when that host exposes a reliable boundary, then give the working agent a
chance to save, correct, or save nothing. A lifecycle signal is not itself a
semantic memory record. When exact source bytes or outcomes are available, a
trusted host may register them so the record's fidelity can be checked.

Report which capabilities actually ran. Saved configuration, an available MCP
tool, a passing backend probe, and a completed host review are separate facts.
The selected native backend must remain available; UM-MEM does not silently
substitute its canonical SQLite journal for failed native retrieval.

## MCP server

`um-memory-mcp` is a Go stdio program. Supply all four identity flags:

```console
um-memory-mcp --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne
```

The installer writes this command to Codex, Claude Code, or Hermes settings:

```console
um-memory install-mcp --host codex --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne
um-memory mcp-status --host codex
```

Use `--host claude` or `--host hermes` for those hosts. The installer preserves
unrelated settings and refuses to overwrite an unmanaged UM-MEM entry. Restart
the host and verify that it loads the server.

The MCP process binds `UM_MEM_SESSION_ID`, `UM_MEM_PROJECT_ID`,
`UM_MEM_TASK_ID`, `UM_MEM_SUBTASK_ID`, `UM_MEM_PARENT_SESSION_ID`, and
`UM_MEM_USER_ID` from its trusted environment. If the host supplies no stable
session ID, the server generates one for its lifetime and uses it for both
writes and reads. Model-generated arguments cannot change that identity.
`UM_MEM_HINDSIGHT_BASE_URL`, `UM_MEM_HINDSIGHT_API_KEY`,
`UM_MEM_MINILM_MODEL_DIR`, and `UM_MEM_ZVEC_MODEL_DIR` configure selected
backends when required.

The tools cover inspection (`health`, `block_map`, `expand_block`,
`navigation_envelope`), scoped writing (`remember`, `amend`, `forget`), retrieval
(`recall`, `build_context`, exact reads), active focus, topology evolution,
consolidation, and export. Tool schemas and revision guards enforce the
request shape and identity boundary; they do not judge whether the agent's
interpretation of the work is useful. MCP alone does not inject context before
each model round.

## Lifecycle hooks

The Go `um-memory-hook` installs host-specific lifecycle commands separately
from MCP. Codex and Claude Code hooks can capture host events and request one
bounded review at the end of a response. The Hermes integration uses external
shell hooks, since Hermes loads native memory providers in its own runtime.
The hook never turns every event into a memory record.

```console
um-memory-hook --install --host codex --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne --project-dir /absolute/path/to/project
um-memory-hook --status --host codex --project-dir /absolute/path/to/project
```

For Hermes, `--project-dir` identifies its configuration home. Check an actual
host prompt, review boundary, and later recall after installation. The
[installation guide](install.md) covers each connection path.

## Go API

Import `github.com/frontier-ai-next/Um-mem` as `ummem`. `Open` returns a
`Client` that owns the selected backend and canonical store. Close it when
finished. Its methods expose the same engine operations used by the CLI and
MCP server:

```go
client, err := ummem.Open(ummem.Config{
    RepoRoot: storePath, WorkspaceID: "alpha",
    ProfileID: "default", BackendID: "mnemosyne",
})
if err != nil { return err }
defer client.Close()

plan := ummem.NewRecallPlan("parser decision")
plan.WorkspaceID, plan.ProfileID = "alpha", "default"
plan.ScopeChain = []string{"workspace:alpha"}
plan.LogicalBlockIDs = []string{"decisions_and_contracts"}
hits, err := client.Recall(ctx, plan)
```

Callers must bind the scope chain and run ID from their own authenticated host
context. Records and packed context are advisory; host task authority remains
with the host. The package also exposes `BuildContext`, `BuildActivationLease`, `ExplainRecall`,
`DeepRead`, `ValidateDependencies`, exact record inspection, and exact
`PinRecord` / `SnapshotWorkingRecord` reads for owner-bound candidates.
`BlockHealth` reads stored metrics for selected leaves without probing the
provider. Request types cover candidate writes, amendment, correction,
promotion, topology, active masks, and erase. `ContextualUtilitySummary`
aggregates verified outcome credits for one exact task context; its score is
shadow-only and cannot grant host authority.
`CompensatePromotion` retracts a pinned candidate or the direct publication
owned by one exact operation; for an approved correction, it restores the
pinned accepted target before retracting the proposal.
`QualifyProcedureReuse` permits the original task signature or an accepted,
evidence-backed transfer envelope naming the exact memory and target task. Its
decision grants neither promotion nor a host action.
`ExpireDueBlocks` is an explicit maintenance call: it archives blocks with a
declared expiry only after checking children, graph edges, active masks and
current record coverage, and returns blockers for blocks it leaves active.

## CLI and service

`um-memory` exposes explicit operations and diagnostics. For example:

```console
um-memory health --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne
um-memory reconcile --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne --dry-run
um-memory list-records --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne --scope workspace --scope-id alpha
um-memory expire-due-blocks --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne --as-of 2026-10-24T00:00:00Z
```

`reconcile --apply` retries pending native projections after you inspect the
dry-run report. The Go loopback service provides local policy, queue, health,
and host-event operations; it is not a network-facing multi-tenant service.
