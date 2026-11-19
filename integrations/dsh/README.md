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

Hosts may allowlist `evidenceTools`. A successful result from one of those tools
can carry an `evidence` reference. If the agent cites that reference with
`evidence_file` during the same turn, UM-MEM stores a hash of the actual tool
result and its producer. A reference supplied only by the model remains an
agent claim.

See [isolated project tools](project-tools/README.md) for the optional bundle used
to compare memory configurations with the same source checks and CLI probes.

## Prerequisites

1. A `um-memory` binary from the same checkout or a release that supports
   `dsh-focus` and `agent-topics`. Either build it:

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
| `embedUrl` | `UM_MEM_EMBED_URL` | optional OpenAI-compatible embedding API base URL |
| `embedModel` | `UM_MEM_EMBED_MODEL` | optional model name for embedding reranking |
| `limit` | — | `8` records per recall |
| `focusLimit` | — | `12` records in the pre-turn block |
| `focusMaxChars` | — | Adaptive in compact mode, `1400` otherwise. An explicit value sets a fixed ceiling |
| `focusProofMaxChars` | — | With a fixed compact ceiling, allow up to `1740` characters for a source-backed revision that replaces a copy |
| `timeoutMs` | — | `30000` per CLI call |
| `inject` | — | `true` |
| `activeMask` | — | `true`; resolve a native per-session mask before the first model request |
| `exactReuse` | — | `false`; reuse recent identical claims in the same block and evidence class, even if titles or tags differ. Add a new source through `um_mem_amend` with `evidence_file`. |
| `projectName` | `UM_MEM_PROJECT_NAME` | working directory name; the display name of the default project block within a workspace |
| `preserveEvidence` | — | enabled in compact reuse mode by default; retain searchable user excerpts in the project block |
| `captureSources` | — | `true`; set to `false` in hosts that submit synthetic continuation prompts and keep their own event journal |
| `compact` | — | `false` |

Enable `compact` to size the pre-turn context to the relevant records.
By default it shows the top record in full within the native token envelope and
includes a second complete record only when it covers additional request terms.
It keeps tools for topic discovery,
lookup, reading, writing, linking, revision and retraction. Administrative
commands remain available in the UM-MEM CLI. The default profile exposes the
full tool set. Explicit `focusLimit` and `focusMaxChars` still apply.

If the first candidate is backed only by a copy and a current revision in the
same block references the original user turn, that revision takes its place.
Under an explicit fixed ceiling the host may spend up to `focusProofMaxChars`
to fit that revision. An original excerpt stays
an excerpt when the agent writes a new conclusion or updates a decision.
If the agent has not yet amended a note, adaptive focus can add a newer,
related user quote from the same project block with clear source attribution.

The project block holds user excerpts and the notes written without a destination.
Turn tools are attached to the excerpt as provenance. A separate topic can use
any meaning and name chosen by the agent. In compact mode, give a short
`topic_reason` that describes the independent search before creating it, or
reuse an existing block by ID. Notes can carry author-chosen topical tags and
optional `facets.purpose`, `facets.applies_when` and `facets.review_when`.
The Atlas shows these separately from system-managed status and retention.

The local Atlas Context view shows the records selected for each Harness turn,
the displayed character count and the lookups performed later in that turn. The
trace retains record references in the workspace and profile. Text is read from
the canonical record when a turn is opened, so erased records cannot be
restored from the trace.

On Mnemosyne, native BM25 search and passive recall work with the standard
installation. Setting `embedUrl` enables hybrid recall over eligible current
records. Ranking reuses cached vectors and bounds cold embedding work per
request. Embedding calls handle record and query text. Use a local endpoint
for local processing.

With `activeMask`, the host pins eligible, scoped search results in a native
mask before request assembly. Explicit recall remains passive and does not
modify the mask. Compact passive recall defaults to two items, with a shared
character budget. Each turn can perform two new searches and two exact reads.
Identical searches and reads of text already shown return a short receipt
without using that backend budget. Graph links are expanded only on request
in compact mode. Install a matching
`um-memory` binary with `dsh-focus` support.
`exactReuse` also raises the threshold for automatic topical links. With an
embedding endpoint, the adapter proposes reuse when a highly similar note
from the same source already exists. The model can use the existing record,
amend it, or confirm a separate note with `save_distinct`.

`um_mem_topics` finds the existing user-facing tree. `um_mem_remember` accepts
a writable `block` ID or an agent-authored `topic` path. One broad name is
enough for a standalone block. Intermediate names are folders. Repeated paths reuse
the same block, and `kind` is an independent, optional note label.
Set `disposition: history` for an episode, which stays out of automatic focus.
The bundled `organize-memory-topology` skill gives examples the agent can adapt.
In compact reuse mode, the host can suggest a brief evidence review during a
normal turn. The agent decides whether a durable note is warranted.

When enabled, source capture runs at the end of the turn and keeps overlapping
user-message excerpts with their provenance. Passive recall can surface the
original wording. Active focus skips the source history. Compact recall suppresses
repeated results within a turn and offers up to three searches, two exact
reads, and one topic lookup. The budgets reset for the next turn.

The compact prompt allows reuse of scoped, dated source-backed facts for repeat
questions. It asks for a fresh source check when the user requests current data
or the stored evidence does not match the question. A citation crossing the
ordinary per-record clipping boundary is kept whole when the budget allows.

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

## Change the store on a copy first

Destructive maintenance is tested against a copy of the store, never against the
live one:

```console
robocopy /MIR <store> <store>-lab      # or: cp -a
um-memory consolidation --repo-root <store>-lab ... --apply --confirm-consolidation
um-memory recall --repo-root <store>-lab --plan-file control.json
```

Compare the same control queries before and after on the copy; only then repeat
the run against the live store. A retirement is stored as a successor record
plus a `supersedes` edge, so the original keeps `status=candidate` and every
consumer must read `withdrawn` rather than the status.

## Operating the memory

| Command | What it is for |
| --- | --- |
| `memory-stats` | maturity view: live, promoted, linked, labelled, promotion candidates |
| `promotion-candidates` | records that carry two witnesses, a support edge, or a human approval |
| `approve --record-id <id>` | a human vouches for a record (`human_approval` evidence); the tribunal is still the only path to directive authority |
| `contradictions` | same-topic pairs that may disagree; nothing is rewritten |
| `consolidation [--apply --confirm-consolidation]` | duplicate plan; dry run unless both flags are present |
| `export-jsonl --output <path> [--current-only]` | the whole store as JSONL (records with `withdrawn`, plus edges) |
| `neighbors --record-id <id> [--depth 2]` | the local graph around one record, with edge type and weight |

Quality is measured, not remembered:

```console
node integrations/dsh/goldset/run.mjs <store>     # hit@1 / hit@k, exit 1 on miss
```

The current baseline is hit@1 60%, hit@k 73% over 30 questions. Re-run it after
any change to ranking, scopes or retrieval.

### Moving a store

`export-jsonl` writes records and edges; `import-jsonl --output <file>` replays
them into another store. Replay semantics, deliberately conservative:

- retired revisions (`retracted`, `superseded`, `withdrawn`) are skipped, so an
  audit export does not resurrect old revisions as live candidates;
- imported records are candidates: a file never grants directive authority;
- edges are recreated only when both endpoints were imported, because a record
  receives a new id in the target store. Full-fidelity edge migration needs the
  stable-id mapping and is not implemented yet.

Round trip verified: 354 exported records -> 51 live records, gold set 63/70
against 60/73 in the source store.
