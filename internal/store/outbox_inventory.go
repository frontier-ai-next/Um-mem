package store

import (
	"context"
	"fmt"

	"github.com/frontier-ai-next/Um-mem/memory"
)

type UnresolvedProjection struct {
	RecordID string
	Backend  string
	Status   string
	Revision int64
	Record   memory.MemoryRecord
}

// UnresolvedOutbox starts from the journal, not the blocks table, so a
// dangling pending write cannot disappear from reconciliation inventory.
func (index *Index) UnresolvedOutbox(ctx context.Context) ([]UnresolvedProjection, error) {
	rows, err := index.db.QueryContext(ctx, `SELECT o.commit_revision,o.block_id,o.store,o.status,
		COALESCE(b.payload_json,'') FROM write_outbox o
		LEFT JOIN blocks b ON b.block_id=o.block_id AND b.workspace_id=o.workspace_id
		WHERE o.workspace_id=? AND o.status IN ('pending','failed')
		ORDER BY o.commit_revision`, index.workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]UnresolvedProjection, 0)
	for rows.Next() {
		var entry UnresolvedProjection
		var payload string
		if err := rows.Scan(&entry.Revision, &entry.RecordID, &entry.Backend, &entry.Status, &payload); err != nil {
			return nil, err
		}
		record, err := memory.DecodeCanonicalRecord([]byte(payload))
		if err != nil || record.BlockID != entry.RecordID || record.WorkspaceID != index.workspaceID {
			return nil, fmt.Errorf("unresolved native journal contains invalid record %q", entry.RecordID)
		}
		if record.ProfileID != index.profileID {
			continue
		}
		entry.Record = record
		result = append(result, entry)
	}
	return result, rows.Err()
}
