package store

import (
	"context"
	"database/sql"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func (index *Index) isCurrentRecordTx(ctx context.Context, tx *sql.Tx, record memory.MemoryRecord) (bool, error) {
	var current int
	err := tx.QueryRowContext(ctx, `SELECT is_current FROM memory_temporal_index
		WHERE workspace_id=? AND block_id=?`, index.workspaceID, record.BlockID).Scan(&current)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil || current != 1 {
		return false, err
	}
	if record.CanonicalKey == "" {
		return true, nil
	}
	var headID string
	err = tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, index.workspaceID, sha256Hex(record.CanonicalKey)).Scan(&headID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil && headID == record.BlockID, err
}

func (index *Index) IsCurrentRecord(ctx context.Context, record memory.MemoryRecord) (bool, error) {
	if record.WorkspaceID != index.workspaceID || record.ProfileID != index.profileID {
		return false, nil
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	current, err := index.isCurrentRecordTx(ctx, tx, record)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return current, nil
}
