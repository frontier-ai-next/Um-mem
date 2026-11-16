# Install and connect a host

UM-MEM stores scoped advisory memory in a local canonical store and one selected
native backend. The CLI, MCP server, local service and lifecycle hooks are Go
programs. The OpenCode plugin and the browser panel remain JavaScript/TypeScript.

## Install the Go programs

The npm launcher requires Node.js 18 or newer. A published package needs a
matching `v<package version>` release containing verified binaries for your
operating system and architecture:

```console
npm install -g https://github.com/frontier-ai-next/Um-mem/releases/download/v0.7.0/um-mem-cli-0.7.0.tgz
um-memory install
```

From a source checkout, install with Go 1.27 or newer:

```console
cd /absolute/path/to/UM-MEM-checkout
npm install -g ./npm
um-memory install --from .
```

Set `UM_MEM_GO` to an absolute Go executable path if `go` is not on `PATH`.
`um-memory install --from /path/to/release-directory` accepts a directory with
the four platform binaries and `SHA256SUMS`. The installer checks every hash
before activating the new version. `--force` replaces the active installation.
`UM_MEM_HOME` selects its home (default `~/.um-mem`).

To build without npm, compile `./cmd/um-memory`, `./cmd/um-memory-mcp`,
`./cmd/um-memory-service` and `./cmd/um-memory-hook` with `go build -tags purego`.
Place all four executables in one directory so the CLI and MCP server can start
their sibling service. Windows executable names end in `.exe`.

## Choose a store and backend

Use an absolute store path and a stable workspace name. Profiles separate teams
or experiments inside a workspace; `default` is fine for one profile. Every
command and host adapter must use the same store, workspace, profile and
backend.

| Backend | Go implementation | Additional requirement |
| --- | --- | --- |
| `mnemosyne` | Local BEAM-compatible SQLite memory | Local disk; selected native projection must pass `health` |
| `mempalace` | Embedded Chroma-compatible palace files | Local MiniLM model files for semantic projection |
| `hindsight` | Native Hindsight HTTP API | A reachable Hindsight service; set `UM_MEM_HINDSIGHT_BASE_URL` and its API key if needed |
| `zvec` | Zvec Go SDK and native library | Zvec library/model assets; the Go runtime checks their availability |

Start with `mnemosyne` for a local store. The four backends are separate native
implementations. Choosing a different backend for an existing workspace does
not migrate its projections. Keep its selected backend and verify it before
using the same records.

```console
um-memory health --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne
```

`native_ok: true` means the selected backend probe passed. It does not prove a
host has loaded its hooks or that a completed task produced a memory write.

## Connect Codex, Claude Code or Hermes

The MCP installer writes the Go server entry to the host's configuration:

```console
um-memory install-mcp --host codex --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne
um-memory mcp-status --host codex
```

Use `--host claude` or `--host hermes` for the other supported hosts. The
installer preserves unrelated entries and refuses to overwrite an unmanaged
entry with the same server name. A configured MCP server exposes tools; it does
not by itself capture prompt or work-boundary events.

Install the Go lifecycle hook separately in the project or Hermes home:

```console
um-memory-hook --install --host codex --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne --project-dir /absolute/path/to/project
um-memory-hook --status --host codex --project-dir /absolute/path/to/project
```

Use `--host claude` for a Claude Code project. For `--host hermes`, pass the
Hermes configuration home as `--project-dir`; the hook installer adds external
Go shell hooks to `config.yaml`. Restart the host after changing its settings.

The optional OpenCode V2 plugin in `integrations/opencode/README.md` is installed
separately. Its TypeScript adapter needs an absolute path to the Go
`um-memory-mcp` executable and explicit store, workspace, profile and backend
arguments. Do not load a second `um_mem` MCP entry alongside that plugin.

## Verify a real host journey

Use a fresh test workspace. Ask the host to complete a task that reveals one
reusable fact or decision, then inspect records with `um-memory list-records`
and ask a later session to recall it. Also complete a trivial task that should
save nothing. `mcp-status` confirms configuration only; it cannot attest that a
host ran a hook. A failed native probe or missing selected backend must remain
visible instead of falling back to the sidecar.

The live panel opens with `um-memory dashboard --repo-root ... --workspace ...
--backend ...`. Use `--no-browser` to print its URL, or `--stop` to stop its
frontend capability. `um-memory memory-map` writes a static map to a path chosen
with `--output-path`.

## Keep an existing store

Pass the existing absolute store path and its original workspace, profile and
backend to the Go commands. The Go store reads the existing canonical SQLite
format and native partitions. Run `health` and `reconcile --dry-run` before
allowing new writes. Take an ordinary backup of the store before a production
cutover, and verify write, amend, recall and retract on a copy first.
