# Operator guide

Pin one absolute store path, workspace, profile, and native backend for each
host connection. Keep unrelated projects in separate workspaces when they need
physical isolation. Check `um-memory health` before a long session and use
`reconcile --dry-run` to inspect pending native projections.

Navigate the content-free root map first, expand the relevant branch, and pass
exact leaf IDs to recall. Use `search_all=true` only when the agent explicitly
chooses a broad search. A request with neither selected leaves nor an explicit
broad search should not be treated as a routed recall.

Topology operations require explicit reasons and expected revisions. Review
the navigation envelope and exact record mapping before split or merge. A
native target copy must be verified before the catalog publishes its new
membership. An unfinished generation remains visible through
`pending-topology-evolutions` and can be resumed or aborted deliberately.

Active masks belong to an exact session and task. Resolve the mask when the
host has a reliable prompt or round boundary and inspect its lease trace for
selected and rendered records, exclusions, catalog/backend revisions, and
token budget. Temporary recall does not change the active mask.

`forget` retracts an exact current revision; it does not physically delete the
canonical history or raw evidence. Archive retains its records. `delete_block`
requires an archived, unreferenced block without current records and preserves
a tombstone. Protected host roots need their dedicated guarded operations.

Physical erase requires a durable host authorization event and stable request
ID. The current exact-ID native purge is implemented for MemPalace. Verify the
returned certificate and account for every external native store; retraction
or deleting a local folder is not proof of complete erase. Use a copied store
to qualify recovery and backend migrations before production cutover.

The loopback service may report `ready`, `ready_read_only`, or `not_ready`. A queue
failure makes mutation unavailable; a native or policy failure leaves the
service not ready. Do not override these states with a canonical sidecar read
and claim native memory is healthy.

Run ordinary quality trials sequentially. Use separate stress tests for
concurrency because they change scheduling and session causality.
