# UM-MEM

UM-MEM gives an agent durable, scoped memory across tasks and sessions. It
stores records in a canonical SQLite journal, projects them into one selected
native backend, and returns bounded advisory evidence when a host asks for it.
The agent decides what is worth saving and how records relate; the runtime
checks scope, revisions, provenance, and delivery limits.

The runtime, CLI, MCP server, service, and lifecycle hooks are written in Go.
The OpenCode adapter remains TypeScript, and the local panel remains
JavaScript. Each `(workspace, profile)` has its own logical tree and records.
Sessions and tasks further narrow candidate recall and active focus.

## Install from a checkout

Go 1.27 or newer builds the four programs. Node.js 18 or newer is needed for
the npm launchers and the OpenCode plugin.

```console
go build -tags purego -o ./bin/um-memory ./cmd/um-memory
go build -tags purego -o ./bin/um-memory-mcp ./cmd/um-memory-mcp
go build -tags purego -o ./bin/um-memory-service ./cmd/um-memory-service
go build -tags purego -o ./bin/um-memory-hook ./cmd/um-memory-hook
```

On Windows, give each output an `.exe` suffix. Keep all four programs in one
directory so the CLI and MCP server can start their sibling service.
Alternatively, from this checkout, run `npm install -g ./npm` followed by
`um-memory install --from .`. A published npm version requires a matching
GitLab tag and generic-package binary release with `SHA256SUMS`. The first
remote tagged release has not yet been run.

## Select a store and backend

Use an absolute store path and a stable workspace ID. Keep the same profile
and backend for every command and host connection to that workspace.

```console
um-memory health --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne
```

`mnemosyne` uses local BEAM-compatible SQLite partitions. `mempalace` uses
local palace files and a MiniLM model. `hindsight` talks to a selected
Hindsight service. `zvec` uses its native Go library and model assets. The
selected backend must pass `health`; the canonical sidecar is not a silent
fallback if its native projection is unavailable.

Open an existing store with its original path, workspace, profile, and
backend. Check `health` and `reconcile --dry-run` on a copy before allowing
new writes.

## Connect a host

The MCP installer supports Codex, Claude Code, and Hermes:

```console
um-memory install-mcp --host codex --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne
um-memory mcp-status --host codex
```

Use `--host claude` or `--host hermes` for those hosts. MCP provides explicit
memory tools. To capture lifecycle events and deliver bounded context at host
boundaries, also install the Go hook:

```console
um-memory-hook --install --host codex --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne --project-dir /absolute/path/to/project
um-memory-hook --status --host codex --project-dir /absolute/path/to/project
```

The [OpenCode TypeScript plugin](integrations/opencode/README.md) connects to
the Go MCP server separately. Do not configure a second `um_mem` MCP entry
beside the plugin. Configuration proves only that a host can load tools;
verify a write, recall, correction, and no-write task in the actual host.

## Use Go directly

The root `ummem` package embeds the same engine used by the CLI and MCP
server. This example writes a candidate and recalls it from one logical
block:

```go
client, err := ummem.Open(ummem.Config{
    RepoRoot: storePath, WorkspaceID: "alpha",
    ProfileID: "default", BackendID: "mnemosyne",
})
if err != nil { return err }
defer client.Close()

saved, err := client.RememberCandidate(ctx, ummem.CandidateRequest{
    Content: "The release rule is R7Q.",
    RequestedBlockID: "stable_project_facts",
    EvidenceKind: "user_statement",
})
if err != nil { return err }

plan := ummem.NewRecallPlan("release rule")
plan.WorkspaceID, plan.ProfileID = "alpha", "default"
plan.ScopeChain = []string{"workspace:alpha"}
plan.LogicalBlockIDs = []string{"stable_project_facts"}
plan.IncludeCandidates = true
hits, err := client.Recall(ctx, plan)
_ = saved
_ = hits
return err
```

Candidates remain advisory. Promotion to directive authority requires
evidence and review. A retrieved record does not authorize a host action or
prove that an agent completed its task.

## Development

```console
go test ./...
go vet ./...
```

The npm installer tests run from `npm/` with `npm test`; set
`UM_MEM_GO_TEST_SOURCE=1` to include a source build. The OpenCode tests run
from `integrations/opencode/` with `npm test`; set
`UM_MEM_OPENCODE_NATIVE_TEST=1` to include its native Go journey.

See the [installation guide](docs/um-mem/install.md),
[quickstart](docs/um-mem/quickstart.md), and
[architecture](docs/um-mem/memory_architecture.md) for more detail.
