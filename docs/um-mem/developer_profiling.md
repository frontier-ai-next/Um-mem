# Runtime diagnostics

The Go runtime writes content-free memory events to
`<repo-root>/.UM-MEM/memory/telemetry/memory_events.jsonl`. Events have a
schema version, timestamp, event ID, workspace and run identity, operation,
status, outcome, and a hash of the serialized event. Selected record IDs are
hashed before writing. This trace is separate from the durable canonical
record and projection journals.

The event writer rotates its file at 8 MiB and retains three older segments.
It uses an interprocess lock and fsync for each accepted event. Diagnostics
must not be used as proof that a model read or followed a retrieved record;
use decision and usage receipts for that claim.

Go now records nested, content-free profile spans for context building and
backend recall. Each event includes measured duration, Go runtime CPU time,
allocation change, a parent span, concurrency peak, and bounded hashes and
sizes for the input and output. The trace never stores their source text.
Read the verified, aggregate profile report with:

```console
um-memory profile-report --repo-root /absolute/path/to/store
```

The operational SLO package defines numeric latency, throughput, recovery,
cost, and prefix-cache contracts. Hosts may report actual provider token
counts with `um-memory observe-runtime-cost --request-file cost.json`; the
report leaves unmeasured savings unknown. The profile report measures memory
operations, while provider and full agent-turn timings still require host
measurements for SLO qualification.

## Canonical journal lookup

Native recall resolves the latest projection journal revision for every
canonical record it inspects. The covering index
`idx_write_outbox_block_revision(block_id, commit_revision)` keeps that
lookup from repeatedly scanning unrelated writes. New stores include the
index; opening the transactional writer installs it idempotently on existing
version-16 stores. This is an additive index, not a payload migration. The
first writer open may take longer while SQLite builds it.

The index includes all journal statuses. Pending, failed, stale and mismatched
projections still undergo the existing fail-closed checks. Scope eligibility,
native content verification and canonical revision checks are unchanged.
The read-only index handle does not install schema objects.

Mnemosyne also reuses the already validated canonical JSON within one recall
when the native payload is byte-for-byte identical. Different JSON encodings
still receive full decoding and comparison. Published topology overlays clear
the reusable payload, so a record cannot be verified against its old membership.
Metadata, content, revision and partition checks still run for every native row;
there is no cross-request verification cache.

These changes remove the repeated journal scan and duplicate payload decoding;
they do not make whole-partition decoding and verification constant-time. Measure the
entire public recall call, including errors and deadlines, before claiming
an SLO. Successful latency percentiles must exclude timed-out requests and
be accompanied by the success count. Keep product benchmark datasets,
runners and profiles in the separate evaluation project.
