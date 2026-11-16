package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// CanonicalHistory reads immutable revision payloads in commit order. A
// committed history row with a corrupt payload fails closed.
func (index *Index) CanonicalHistory(ctx context.Context, canonicalKey string) ([]memory.MemoryRecord, error) {
	if canonicalKey == "" || strings.TrimSpace(canonicalKey) != canonicalKey {
		return nil, errors.New("canonical history requires an exact key")
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	keyHash := sha256Hex(canonicalKey)
	rows, err := tx.QueryContext(ctx, `SELECT m.successor_id,m.record_payload_id,
		m.record_payload_sha256,p.payload_json,p.payload_sha256,p.payload_kind,
		h.status
		FROM canonical_mutations m
		LEFT JOIN canonical_payloads p ON p.payload_id=m.record_payload_id
		LEFT JOIN canonical_head_outbox h ON h.commit_revision=m.commit_revision
		WHERE m.workspace_id=? AND m.canonical_key_sha256=?
		ORDER BY m.commit_revision`, index.workspaceID, keyHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := make([]memory.MemoryRecord, 0)
	rowCount := 0
	for rows.Next() {
		rowCount++
		var successorID, payloadID, checksum string
		var payload, storedChecksum, kind, status sql.NullString
		if err := rows.Scan(&successorID, &payloadID, &checksum, &payload, &storedChecksum, &kind, &status); err != nil {
			return nil, err
		}
		if !payload.Valid || !storedChecksum.Valid || !kind.Valid || !status.Valid ||
			kind.String != "record" || status.String != "committed" || storedChecksum.String != checksum ||
			payloadID != "record:"+checksum || checksum != sha256Hex("record\x00"+payload.String) {
			return nil, errors.New("canonical history payload identity is invalid")
		}
		record, err := memory.DecodeCanonicalRecord([]byte(payload.String))
		if err != nil {
			return nil, err
		}
		if record.BlockID != successorID || record.WorkspaceID != index.workspaceID ||
			record.CanonicalKey != canonicalKey {
			return nil, fmt.Errorf("canonical history record identity is invalid: %s", successorID)
		}
		projected, _, _, _, _, err := index.eventWindowTx(ctx, tx, record, true)
		if err != nil {
			return nil, err
		}
		if record.ProfileID == index.profileID {
			history = append(history, projected)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if rowCount == 0 {
		var headID string
		err := tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
			WHERE workspace_id=? AND canonical_key_sha256=?`, index.workspaceID, keyHash).Scan(&headID)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == nil {
			return nil, fmt.Errorf("canonical head %s has no mutation history", headID)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return history, nil
}
