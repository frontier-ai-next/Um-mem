# Hermes Go integration qualification

Hermes uses the Go MCP server for explicit memory tools and Go external shell
hooks for supported lifecycle boundaries. Each profile must bind one store,
workspace, profile, and native backend. Cross-workspace manager reads require
an explicit request and a trusted user identity.

## Current surfaces

- `um-memory install-mcp --host hermes` writes the Go stdio MCP entry.
- `um-memory-hook --install --host hermes` adds managed commands to Hermes'
  `config.yaml` while preserving unrelated hooks.
- `pre_llm_call` can return bounded advisory context. Session, turn, tool,
  compaction, delegation, and finalize hooks send content-free host state.
- The Go client and local service enforce scope, native backend health,
  projection status, and durable mutation receipts.

## Qualification before use as primary memory

1. Run a fresh Hermes profile against a clean store and a selected backend.
   Confirm native health and an actual MCP tool call.
2. Invoke the installed hooks in Hermes. Confirm a bounded pre-model context
   and a content-free event receipt without logging prompt text.
3. Complete a task with one supported reusable fact. Check the record's
   operation receipt, native projection, and later-session recall.
4. Complete a task where nothing should be saved. Check that no semantic
   record was created solely by lifecycle events.
5. Correct and retract a record, switch projects, and verify isolation and
   current-only recall.
6. Measure token use and latency for the whole host journey. Compare against
   the same tasks without UM-MEM; a passing contract test alone is not a
   performance or answer-quality result.

## Remaining native-provider gap

Hermes' in-process memory-provider callback API loads a module in Hermes'
runtime. The Go MCP and external shell-hook surfaces do not yet reproduce all
of the old provider's compaction and per-round callbacks. Keep that gap visible
in release qualification. A profile that requires those callbacks must not
be described as fully migrated until an equivalent Go-accessible host surface
is proven in the target Hermes version.
