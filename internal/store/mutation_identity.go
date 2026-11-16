package store

import (
	"context"
	"database/sql"
	"errors"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// MutationIdentity returns the committed or staged action behind a canonical
// successor. Callers still verify its projection state before using the row.
type MutationIdentity struct {
	Action           string
	PredecessorID    string
	RollbackTargetID string
	Status           string
}

func (index *Index) MutationIdentity(ctx context.Context, successorID string) (*MutationIdentity, error) {
	var payload string
	err := index.db.QueryRowContext(ctx, `SELECT payload_json FROM blocks
		WHERE workspace_id=? AND block_id=?`, index.workspaceID, successorID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := memory.DecodeCanonicalRecord([]byte(payload))
	if err != nil {
		return nil, err
	}
	if record.ProfileID != index.profileID {
		return nil, nil
	}
	var result MutationIdentity
	err = index.db.QueryRowContext(ctx, `SELECT mutation_action,predecessor_id,
		rollback_target_id,status FROM canonical_head_outbox
		WHERE workspace_id=? AND successor_id=?
		ORDER BY commit_revision DESC LIMIT 1`, index.workspaceID,
		successorID).Scan(&result.Action, &result.PredecessorID,
		&result.RollbackTargetID, &result.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &result, nil
}
