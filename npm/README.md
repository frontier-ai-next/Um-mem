# @um-mem/cli

Node launchers for the UM-MEM Go engine, MCP server, local service and Hermes
hook. The npm package contains no Python runtime.

## Install

Node.js 18 or newer is required for the launchers. The public release contains
four prebuilt Go binaries for Windows, Linux and macOS on amd64 and arm64.
The installer verifies their SHA-256 checksums before activating them.

```console
npm install -g https://github.com/frontier-ai-next/Um-mem/releases/download/v0.7.0/um-mem-cli-0.7.0.tgz
um-memory install
```

Assets are downloaded from the matching GitHub release declared by the package's
repository and version metadata. Public downloads require no access token and
no Go compiler.

To install from a Go checkout instead, use Go 1.27 or newer:

```console
cd /absolute/path/to/UM-MEM-checkout
npm install -g ./npm
um-memory install --from .
```

Set `UM_MEM_GO` to the path of a Go executable if it is not on `PATH`. To install
from a downloaded release directory instead, pass `--from` with the directory
containing `SHA256SUMS` and the four binaries for your system. Use `--force` to
replace an existing same-version release installation. An explicit `--from`
rebuilds or recopies the source, and a new requested version replaces an old
binary automatically. `UM_MEM_HOME` selects the installation home
(default: `~/.um-mem`). The active binary directory is recorded in
`UM_MEM_HOME/npm.json`.

## Connect a host

Choose an absolute store path, workspace, profile and native backend. For
example, with the local Mnemosyne backend:

```console
um-memory health --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne
um-memory install-mcp --host codex --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne
um-memory mcp-status --host codex --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne
```

`install-mcp` also accepts `--host claude` and `--host hermes`. This command
connects the Go MCP server. Install the Go lifecycle hook separately when
prompt-time recall or work-boundary capture is needed:

```console
um-memory-hook --install --host codex --repo-root /absolute/path/to/memory-store --workspace my-project --profile default --backend mnemosyne --project-dir /absolute/path/to/project
um-memory-hook --status --host codex --project-dir /absolute/path/to/project
```

The hook command also accepts `--host claude` and `--host hermes`. For Hermes,
`--project-dir` is its configuration home rather than a project directory.
The direct binaries `um-memory-mcp`, `um-memory-service` and `um-memory-hook`
are available for manual host configuration. The OpenCode TypeScript plugin is
kept in `integrations/opencode` and connects to the Go MCP server.

Run `um-memory` without arguments to see its current command list. Native
backends may require their own service or model files. The Go engine checks the
selected backend with `health` before use.

Hosts can record measured provider tokens with `um-memory observe-runtime-cost
--repo-root ... --workspace ... --backend ... --request-file cost.json`. The JSON
file contains a stable `observation_id` and optional `input_tokens`,
`cached_input_tokens`, and `total_tokens` integers. Replaying the same values is
idempotent; a changed observation is rejected. Reports leave unmeasured latency
and cache savings unknown.

## Development

From the repository root, run `go test ./...` and `go vet ./...`. From `npm`,
run `npm test`; set `UM_MEM_GO_TEST_SOURCE=1` to also build and run all four Go
binaries through the installer.
