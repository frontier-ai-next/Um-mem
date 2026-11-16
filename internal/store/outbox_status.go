package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// ProjectionState is the latest native journal observation for one canonical
// record. An empty Status means the record exists but has no outbox entry.
type ProjectionState struct {
	Status         string
	CommitRevision int64
	RetryExhausted bool
}

// ReadProjectionState is scoped to this index's workspace and profile. A
// missing or out-of-profile record is invisible even when its outbox is present.
func (index *Index) ReadProjectionState(ctx context.Context, recordID string) (*ProjectionState, error) {
	var payload string
	var status, projectionError sql.NullString
	var revision sql.NullInt64
	err := index.db.QueryRowContext(ctx, `SELECT b.payload_json,o.status,o.commit_revision,o.error
		FROM blocks b LEFT JOIN write_outbox o ON o.commit_revision=(
			SELECT commit_revision FROM write_outbox
			WHERE block_id=b.block_id AND workspace_id=b.workspace_id
			ORDER BY commit_revision DESC LIMIT 1)
		WHERE b.block_id=? AND b.workspace_id=?`, recordID, index.workspaceID).
		Scan(&payload, &status, &revision, &projectionError)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := memory.DecodeCanonicalRecord([]byte(payload))
	if err != nil {
		return nil, err
	}
	if record.WorkspaceID != index.workspaceID || record.BlockID != recordID {
		return nil, errors.New("projection status record identity mismatch")
	}
	if record.ProfileID != index.profileID {
		return nil, nil
	}
	state := &ProjectionState{Status: status.String, CommitRevision: revision.Int64}
	if !status.Valid {
		return state, nil
	}
	if state.Status != "pending" && state.Status != "committed" && state.Status != "failed" {
		return nil, errors.New("projection status is invalid")
	}
	if state.Status == "pending" && strings.TrimSpace(projectionError.String) != "" {
		var retry struct {
			Kind     string      `json:"kind"`
			Attempts json.Number `json:"attempts"`
		}
		if json.Unmarshal([]byte(projectionError.String), &retry) == nil && retry.Kind == "native_projection" {
			attempts, err := retry.Attempts.Int64()
			state.RetryExhausted = err == nil && attempts >= 3
		}
	}
	return state, nil
}

// ProjectionStatus preserves the compact status/revision read used by receipts.
func (index *Index) ProjectionStatus(ctx context.Context, recordID string) (string, int64, error) {
	state, err := index.ReadProjectionState(ctx, recordID)
	if err != nil || state == nil {
		return "", 0, err
	}
	return state.Status, state.CommitRevision, nil
}
