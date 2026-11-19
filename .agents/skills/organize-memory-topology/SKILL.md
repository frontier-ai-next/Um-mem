---
name: organize-memory-topology
description: Organize durable memory during ordinary work. Use when a subject recurs, a fact or preference changes, or topics, links and active context need review.
---

# Organize Memory Topology

## During ordinary work

Save a short, sourced note when it can help in a later session. Choose stable topic names that describe which questions the notes answer. Start with a small number of broad blocks. A writable block owns a native storage partition and can hold many notes. Use note titles and tags for details. Create a subtopic when a body of records needs to be searched independently and that boundary is likely to last.

Give an emerging topic one clear name based on the work itself. A checkout path or run ID belongs in provenance, even if it identifies where you did the work.

Keep the source and history of ordinary project work together with that project. Use your own descriptive tags on notes for distinctions such as the subject, decision, or experiment. A difference in note format alone does not justify another storage partition. Describe the recurring questions that would be searched separately before opening a new topic.

- A small ventilation pilot might start with one block for calibration, trial results and field changes. If the pilot later grows, calibration could merit its own block for separately finding calibration history and procedures.
- A recurring preference for tabular status updates can sit under a topic about working with the user. A rule for a single report belongs with that report.
- A changed decision gets a new revision with its date and evidence. Separate records can be linked when one supports or explains the other.

If the host offers a topic path, one name is enough for a standalone block. Intermediate entries in a longer path are folders, and the last holds notes. In DeepSeek Harness, `um_mem_remember` defaults to the project block. Reuse an existing `block` from `um_mem_topics`, or supply a new `topic` with `topic_reason`. With standard UM-MEM tools, inspect `block_map` or `navigation_envelope`, use `create_block` where needed, then pass the leaf ID to `remember`.

## When revising the tree

Use the following review for deliberate topology changes. The repository's `docs/um-mem/memory_architecture.md` and `docs/um-mem/data_model.md` describe scope and active context.

Inspect the existing tree, synopses, graph links, active mask, and advisory maintenance observations before changing anything.

- Reuse an existing block when its meaning fits.
- Compare each new source with the synopsis, not only the writable flag. Update stale descriptions and topical tags at meaningful work boundaries.
- Split mixed subjects needing independent retrieval; merge redundant boundaries when the meanings fit. A deliberate no-change decision is valid, and more leaves is not a quality metric.
- Before a full review, save an evidence-grounded keep/split/merge plan. Compare boundaries across leaves, not just descriptions independently. Execute the chosen plan with current revisions, verify published receipts, and report genuinely deferred decisions separately. A wider synopsis alone does not complete a chosen structural change. Do not manufacture split/merge activity for its own sake.
- Enumerate every membership page with `list_records` and inspect exact records before mapping a split/merge. A navigation preview or top-k recall is not a complete record set.
- Create a block only for a stable area that can evolve independently. Keep its synopsis short and let the memory service generate its opaque ID.
- Preserve one ownership parent. Use typed graph links for cross-branch relationships instead of duplicating records.
- Treat machine observations as facts only. Decide from the actual memory content whether to amend, move, split, merge, archive, consolidate, or relink, then use an explicit typed operation.
- Require exact record mappings and revisions for structural mutations. Preserve sources and provenance.
- For large chosen splits, author every record's destination in bounded batches and persist the full mapping before native publication. Unassigned records never receive an automatic default. Distinguish exact record IDs from leaf IDs; the native service checks record revisions.
- Put automatically recurring context in the active mask. Use recall for temporary access to passive memory.
- Treat session and task IDs as scope and provenance. Do not create a visible session or project block unless its meaning warrants one.
- Keep memory advisory: a failed recall or mask materialization must not block the host task.
- Do not require standard branch names, a project level, or a domain taxonomy.
- Do not infer semantic structure, lifecycle, errors, or outcomes from keywords, substrings, regular expressions, prompts, or tool-output prose.
