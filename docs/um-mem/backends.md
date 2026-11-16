# Native backends

One runtime selects one native backend for its workspace and profile. The
canonical SQLite journal holds records, revisions, receipts, and the projection
outbox. Native recall reads the selected backend's leaf partition. If it is
unavailable or a projection is pending, UM-MEM reports that state instead of
claiming a successful sidecar recall.

| Backend | Native leaf partition | External service |
| --- | --- | --- |
| `mnemosyne` | BEAM-compatible SQLite database and session per leaf | No |
| `mempalace` | Chroma-compatible collection family | No |
| `hindsight` | Hindsight bank | Yes |
| `zvec` | Zvec collection | No |

All four implementations are in Go. Build with `go build -tags purego` or use
the Go binaries installed by `@um-mem/cli`. Check the selected provider before
starting a host session:

```console
um-memory health --repo-root /absolute/path/to/store --workspace alpha --profile default --backend mnemosyne
```

The same absolute store path, workspace, profile, and backend must be used by
the CLI, MCP server, service, and hooks.

## Mnemosyne

Mnemosyne is a local starting point. Each logical leaf receives a stable
native partition derived from its workspace, block, and backend partition key.
Its database lives below the workspace's native `partitions/` directory, so
FTS ranking stays inside that leaf. Native rows are verified against current
canonical projections before they can enter semantic recall; historical rows
left after correction do not displace current results.

A legacy shared `mnemosyne.db` in the native data directory is preserved and
reported as requiring a projection rebuild. UM-MEM does not silently convert
or discard it. Use a copy of an existing store when qualifying a migration.

## MemPalace

MemPalace uses local collection families and a MiniLM model. Set
`UM_MEM_MINILM_MODEL_DIR` to the model files or pass `--minilm-model-dir` to
the Go CLI or MCP server. A new logical leaf is materialized when its first
eligible record is projected. Long canonical content is retained in full;
embedding input and source windows are bounded separately. Physical erase is
implemented for this backend and verified by exact canonical ID.

The bundled Linux amd64 Chroma shim currently requires glibc 2.38 or newer.
The full Go test suite therefore runs on Debian Trixie in CI. Debian Bookworm
can build the Go commands and run Mnemosyne, but its glibc 2.36 cannot load
that Chroma shim, including with `-tags purego`.

## Hindsight

Configure `UM_MEM_HINDSIGHT_BASE_URL` and, when required,
`UM_MEM_HINDSIGHT_API_KEY`; the CLI and MCP server also accept `--base-url`.
Each leaf maps to a Hindsight bank. Health requires the configured service's
native operations to be available. The Go runtime does not replace a failed
service read with a canonical SQLite search.

## Zvec

Zvec uses its native Go library and a separate collection per logical leaf.
Set `UM_MEM_ZVEC_MODEL_DIR` or pass `--zvec-model-dir` for the required model
assets. Projection divides long sources into overlapping windows with exact
offsets, writes bounded batches, and verifies them before the outbox commit.
Those windows are retrieval aids, not independent memory facts. Exact record
reads still return the complete canonical content.

## Topology and recovery

Split and merge operations stage native copies in target leaves before
publishing new membership. A failed stage keeps the source readable and the
generation pending for recovery. Use `pending-topology-evolutions`,
`resume-topology-evolution`, and `reconcile --dry-run` to inspect state before
retrying pending work. A different backend does not inherit old native
projections; switching backends requires a deliberate migration or a clean
store.
