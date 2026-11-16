package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type ProjectionRow struct {
	Record   memory.MemoryRecord
	Backend  string
	Status   string
	NativeID string
	Revision int64
	Current  bool
	// CanonicalJSON is the already decoded and validated payload for Record.
	// It is cleared when a published topology overlay changes that record.
	CanonicalJSON string
}

func (index *Index) Scope() (workspaceID, profileID string) {
	return index.workspaceID, index.profileID
}

const projectionRowsQuery = `SELECT b.payload_json, COALESCE(o.store,''),
		COALESCE(o.status,''), COALESCE(n.native_id,''), COALESCE(o.commit_revision,0),
		COALESCE(t.is_current,0)
		FROM blocks b
		LEFT JOIN memory_temporal_index t ON t.workspace_id=b.workspace_id AND t.block_id=b.block_id
		LEFT JOIN write_outbox o ON o.commit_revision=(
			SELECT max(commit_revision) FROM write_outbox WHERE block_id=b.block_id)
		LEFT JOIN write_outbox_native_refs n ON n.commit_revision=o.commit_revision
		WHERE b.workspace_id=? ORDER BY b.block_id`

// ProjectionRows supplies selected-backend recall with its canonical identity
// and journal state. Pending rows are deliberately included so native recall
// can fail closed for an affected logical block.
func (index *Index) ProjectionRows(ctx context.Context) ([]ProjectionRow, error) {
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, projectionRowsQuery, index.workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ProjectionRow, 0)
	for rows.Next() {
		var payload, backend, status, nativeID string
		var revision int64
		var current int
		if err := rows.Scan(&payload, &backend, &status, &nativeID, &revision, &current); err != nil {
			return nil, err
		}
		record, err := memory.DecodeCanonicalRecord([]byte(payload))
		if err != nil {
			return nil, fmt.Errorf("decode projected record: %w", err)
		}
		if record.WorkspaceID != index.workspaceID {
			return nil, fmt.Errorf("projected record workspace differs from canonical index")
		}
		if record.ProfileID != index.profileID {
			continue
		}
		result = append(result, ProjectionRow{Record: record, Backend: backend, Status: status, NativeID: nativeID, Revision: revision, Current: current == 1, CanonicalJSON: payload})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if index.topology != nil && len(result) > 0 {
		records := make([]memory.MemoryRecord, len(result))
		for i := range result {
			records[i] = result[i].Record
		}
		effective, err := index.topology.EffectiveRecords(ctx, records)
		if err != nil {
			return nil, err
		}
		for i := range result {
			if !reflect.DeepEqual(result[i].Record, effective[i]) {
				result[i].CanonicalJSON = ""
			}
			result[i].Record = effective[i]
		}
	}
	return result, nil
}
