package store

import (
	"context"
	"database/sql"
	"fmt"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// CurrentKindRecords reads every committed current record of one kind from
// the selected profile and backend. Procedure lookup uses this instead of a
// recent window so older still-current procedures remain discoverable.
func (index *Index) CurrentKindRecords(ctx context.Context, backendID, kind string) ([]memory.MemoryRecord, error) {
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT b.payload_json FROM blocks b
		JOIN memory_temporal_index t ON t.workspace_id=b.workspace_id AND t.block_id=b.block_id
		JOIN write_outbox o ON o.commit_revision=(
			SELECT max(commit_revision) FROM write_outbox WHERE block_id=b.block_id)
		WHERE b.workspace_id=? AND t.is_current=1 AND o.status='committed'
		AND o.store=? AND json_extract(b.payload_json,'$.profile_id')=?
		AND json_extract(b.payload_json,'$.kind')=? ORDER BY b.block_id`,
		index.workspaceID, backendID, index.profileID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]memory.MemoryRecord, 0)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		record, err := memory.DecodeCanonicalRecord([]byte(payload))
		if err != nil {
			return nil, fmt.Errorf("decode current kind record: %w", err)
		}
		if record.WorkspaceID != index.workspaceID || record.ProfileID != index.profileID || record.Kind != kind {
			return nil, fmt.Errorf("current kind record escaped selected scope")
		}
		result = append(result, record)
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
	return result, nil
}
