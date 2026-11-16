-- UM-MEM canonical SQLite schema version 16.
CREATE TABLE schema_metadata (
    key TEXT PRIMARY KEY,
    value INTEGER NOT NULL
);
CREATE TABLE blocks (
    block_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    stage_id TEXT NOT NULL,
    subtask_id TEXT NOT NULL,
    scope TEXT NOT NULL,
    status TEXT NOT NULL,
    retention TEXT NOT NULL,
    delivery_policy TEXT NOT NULL,
    temperature TEXT NOT NULL,
    authority TEXT NOT NULL,
    trust_level TEXT NOT NULL,
    verification_status TEXT NOT NULL,
    promotion_decision TEXT NOT NULL,
    content TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    committed_at TEXT NOT NULL
);
CREATE INDEX idx_blocks_workspace ON blocks(workspace_id, status, authority);
CREATE INDEX idx_blocks_scope ON blocks(workspace_id, scope, run_id, stage_id, subtask_id);
CREATE INDEX idx_blocks_committed ON blocks(workspace_id, committed_at, block_id);
CREATE TABLE index_nodes (
    node_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    node_type TEXT NOT NULL,
    title TEXT NOT NULL,
    tags_json TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_index_nodes_workspace_type ON index_nodes(workspace_id, node_type);
CREATE INDEX idx_index_nodes_workspace_updated ON index_nodes(workspace_id, updated_at);
CREATE TABLE edges (
    src_id TEXT NOT NULL,
    dst_id TEXT NOT NULL,
    edge_type TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    PRIMARY KEY(workspace_id, src_id, dst_id, edge_type)
);
CREATE INDEX idx_edges_src ON edges(workspace_id, src_id);
CREATE INDEX idx_edges_dst ON edges(workspace_id, dst_id);
CREATE TABLE usage (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    block_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    outcome TEXT NOT NULL,
    payload_json TEXT NOT NULL
);
CREATE TABLE canonical_payloads (
    payload_id TEXT PRIMARY KEY,
    payload_sha256 TEXT NOT NULL UNIQUE,
    payload_kind TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE write_outbox (
    commit_revision INTEGER PRIMARY KEY AUTOINCREMENT,
    block_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    store TEXT NOT NULL,
    payload_id TEXT NOT NULL,
    payload_sha256 TEXT NOT NULL,
    status TEXT NOT NULL,
    error TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_write_outbox_pending
ON write_outbox(block_id) WHERE status='pending';
CREATE INDEX idx_write_outbox_workspace_status
ON write_outbox(workspace_id, status, commit_revision);
-- Latest projection lookup must include committed/failed as well as pending
-- writes. The partial pending index cannot serve per-record revision queries.
CREATE INDEX idx_write_outbox_block_revision
ON write_outbox(block_id, commit_revision);
CREATE TABLE write_outbox_native_refs (
    commit_revision INTEGER PRIMARY KEY,
    native_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(commit_revision) REFERENCES write_outbox(commit_revision)
);
CREATE TABLE canonical_heads (
    workspace_id TEXT NOT NULL,
    canonical_key_sha256 TEXT NOT NULL,
    block_id TEXT NOT NULL,
    commit_revision INTEGER NOT NULL,
    PRIMARY KEY(workspace_id, canonical_key_sha256)
);
CREATE TABLE canonical_head_outbox (
    commit_revision INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL,
    canonical_key_sha256 TEXT NOT NULL,
    predecessor_id TEXT NOT NULL,
    successor_id TEXT NOT NULL,
    successor_status TEXT NOT NULL,
    successor_authority TEXT NOT NULL,
    mutation_action TEXT NOT NULL,
    rollback_target_id TEXT NOT NULL,
    payload_id TEXT NOT NULL,
    payload_sha256 TEXT NOT NULL,
    status TEXT NOT NULL,
    error TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_canonical_head_outbox_pending
ON canonical_head_outbox(workspace_id, canonical_key_sha256) WHERE status='pending';
CREATE INDEX idx_canonical_head_outbox_successor
ON canonical_head_outbox(successor_id, status, commit_revision);
CREATE TABLE canonical_mutations (
    commit_revision INTEGER PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    canonical_key_sha256 TEXT NOT NULL,
    predecessor_id TEXT NOT NULL,
    successor_id TEXT NOT NULL UNIQUE,
    mutation_action TEXT NOT NULL,
    rollback_target_id TEXT NOT NULL,
    record_payload_id TEXT NOT NULL,
    record_payload_sha256 TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    committed_at TEXT NOT NULL,
    FOREIGN KEY(commit_revision) REFERENCES canonical_head_outbox(commit_revision)
);
CREATE INDEX idx_canonical_mutations_lookup
ON canonical_mutations(workspace_id, canonical_key_sha256, committed_at, commit_revision);
CREATE TABLE canonical_mutation_receipts (
    commit_revision INTEGER NOT NULL,
    receipt_id TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    PRIMARY KEY(commit_revision, receipt_id),
    FOREIGN KEY(commit_revision) REFERENCES canonical_mutations(commit_revision)
);
CREATE TABLE canonical_mutation_invalidations (
    commit_revision INTEGER NOT NULL,
    view_id TEXT NOT NULL,
    PRIMARY KEY(commit_revision, view_id),
    FOREIGN KEY(commit_revision) REFERENCES canonical_mutations(commit_revision)
);
CREATE TABLE prospective_obligations (
    obligation_id TEXT PRIMARY KEY,
    scope TEXT NOT NULL,
    status TEXT NOT NULL,
    version INTEGER NOT NULL,
    payload_id TEXT NOT NULL,
    payload_sha256 TEXT NOT NULL,
    receipt_id TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE prospective_obligation_events (
    obligation_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    expected_version INTEGER NOT NULL,
    from_status TEXT NOT NULL,
    to_status TEXT NOT NULL,
    payload_id TEXT NOT NULL,
    payload_sha256 TEXT NOT NULL,
    receipt_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(obligation_id, event_id),
    UNIQUE(obligation_id, idempotency_key)
);
CREATE TABLE memory_exact_terms (
    workspace_id TEXT NOT NULL,
    term_type TEXT NOT NULL,
    term_value TEXT NOT NULL,
    block_id TEXT NOT NULL,
    PRIMARY KEY(workspace_id, term_type, term_value, block_id)
);
CREATE INDEX idx_memory_exact_lookup
ON memory_exact_terms(workspace_id, term_type, term_value, block_id);
CREATE INDEX idx_memory_exact_block
ON memory_exact_terms(block_id, term_type, term_value);
CREATE TABLE memory_temporal_index (
    workspace_id TEXT NOT NULL,
    block_id TEXT PRIMARY KEY,
    canonical_key_sha256 TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    valid_from TEXT NOT NULL,
    valid_to TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    is_current INTEGER NOT NULL CHECK(is_current IN (0, 1))
);
CREATE INDEX idx_memory_temporal_current
ON memory_temporal_index(workspace_id, is_current, canonical_key_sha256, valid_from, valid_to);
CREATE INDEX idx_memory_temporal_revision
ON memory_temporal_index(workspace_id, revision_id, block_id);
CREATE TABLE memory_provenance_links (
    workspace_id TEXT NOT NULL,
    derived_id TEXT NOT NULL,
    raw_source_ref TEXT NOT NULL,
    block_id TEXT NOT NULL,
    PRIMARY KEY(workspace_id, derived_id, raw_source_ref, block_id)
);
CREATE INDEX idx_memory_provenance_reverse
ON memory_provenance_links(workspace_id, derived_id, raw_source_ref);
CREATE TABLE record_block_memberships (
    workspace_id TEXT NOT NULL,
    record_id TEXT NOT NULL,
    record_revision_id TEXT NOT NULL,
    membership_revision_id TEXT NOT NULL,
    logical_block_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK(role IN ('primary', 'secondary')),
    ordinal INTEGER NOT NULL CHECK(ordinal >= 0),
    catalog_revision_id TEXT NOT NULL,
    backend_partition_id TEXT NOT NULL,
    valid_from TEXT NOT NULL,
    valid_to TEXT NOT NULL,
    is_current INTEGER NOT NULL CHECK(is_current IN (0, 1)),
    PRIMARY KEY(record_id, logical_block_id)
);
CREATE UNIQUE INDEX idx_record_block_membership_primary
ON record_block_memberships(record_id) WHERE role='primary';
CREATE INDEX idx_record_block_membership_lookup
ON record_block_memberships(workspace_id, logical_block_id, is_current, record_id);
CREATE TABLE native_partition_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL,
    primary_block_id TEXT NOT NULL,
    catalog_revision_id TEXT NOT NULL,
    partition_id TEXT NOT NULL,
    isolation_class TEXT NOT NULL,
    partition_mode TEXT NOT NULL,
    operation TEXT NOT NULL,
    latency_ms REAL NOT NULL,
    hit_count INTEGER NOT NULL,
    record_bytes INTEGER,
    rendered_tokens INTEGER NOT NULL,
    success INTEGER NOT NULL CHECK(success IN (0, 1)),
    search_mode TEXT NOT NULL,
    recorded_at TEXT NOT NULL
);
CREATE INDEX idx_native_partition_events_identity
ON native_partition_events(
    workspace_id, primary_block_id, catalog_revision_id, partition_id, id
);
CREATE INDEX idx_native_partition_events_stable_partition
ON native_partition_events(workspace_id, primary_block_id, partition_id, id);
CREATE INDEX idx_native_partition_events_partition_latency
ON native_partition_events(workspace_id, primary_block_id, partition_id, latency_ms, id);
CREATE VIRTUAL TABLE memory_fts USING fts5(
    block_id UNINDEXED,
    workspace_id UNINDEXED,
    title,
    content,
    tags,
    tokenize='unicode61'
);
