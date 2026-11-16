CREATE TABLE catalog_metadata (
    key TEXT PRIMARY KEY,
    value INTEGER NOT NULL
);
CREATE TABLE topology_blocks (
    block_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    parent_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('active', 'archived')),
    revision INTEGER NOT NULL CHECK(revision >= 1),
    protected INTEGER NOT NULL CHECK(protected IN (0, 1)),
    backend_partition_id TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_topology_blocks_parent
ON topology_blocks(workspace_id, profile_id, parent_id, status, block_id);
CREATE TABLE topology_edges (
    edge_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    src_id TEXT NOT NULL,
    dst_id TEXT NOT NULL,
    edge_type TEXT NOT NULL,
    reason TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK(revision >= 1),
    created_at TEXT NOT NULL,
    UNIQUE(workspace_id, profile_id, src_id, dst_id, edge_type)
);
CREATE INDEX idx_topology_edges_src
ON topology_edges(workspace_id, profile_id, src_id, edge_type);
CREATE INDEX idx_topology_edges_dst
ON topology_edges(workspace_id, profile_id, dst_id, edge_type);
CREATE TABLE active_masks (
    mask_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK(revision >= 1),
    status TEXT NOT NULL CHECK(status IN ('active', 'suspended', 'expired')),
    payload_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_active_masks_scope
ON active_masks(workspace_id, profile_id, session_id, task_id, updated_at, mask_id);
CREATE TABLE maintenance_observations (
    observation_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    block_id TEXT NOT NULL,
    observation_type TEXT NOT NULL,
    values_json TEXT NOT NULL,
    threshold_revision TEXT NOT NULL,
    evidence_ids_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_maintenance_observations_block
ON maintenance_observations(workspace_id, profile_id, block_id, created_at);
CREATE TABLE catalog_mutations (
    mutation_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    previous_revision INTEGER NOT NULL,
    next_revision INTEGER NOT NULL,
    actor TEXT NOT NULL,
    reason TEXT NOT NULL,
    affected_ids_json TEXT NOT NULL,
    operation TEXT NOT NULL,
    input_fingerprint TEXT NOT NULL,
    output_fingerprint TEXT NOT NULL,
    validator_result TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_catalog_mutations_revision
ON catalog_mutations(workspace_id, profile_id, next_revision);
CREATE TABLE topology_tombstones (
    block_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    deleted_at TEXT NOT NULL
);
CREATE TABLE topology_evolutions (
    operation_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('prepared', 'projections_ready', 'published', 'aborted')),
    payload_json TEXT NOT NULL,
    payload_sha256 TEXT NOT NULL,
    abort_reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, profile_id, operation_id)
);
CREATE INDEX idx_topology_evolutions_state
ON topology_evolutions(workspace_id, profile_id, state, created_at);
CREATE TABLE published_record_memberships (
    record_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    record_revision_id TEXT NOT NULL,
    block_id TEXT NOT NULL,
    backend_partition_id TEXT NOT NULL,
    catalog_revision_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    published_at TEXT NOT NULL,
    FOREIGN KEY(workspace_id, profile_id, operation_id)
        REFERENCES topology_evolutions(workspace_id, profile_id, operation_id)
);
CREATE INDEX idx_published_memberships_block
ON published_record_memberships(workspace_id, profile_id, block_id, record_id);
