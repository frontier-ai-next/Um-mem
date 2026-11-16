package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Tombstone struct {
	BlockID     string `json:"block_id"`
	WorkspaceID string `json:"workspace_id"`
	ProfileID   string `json:"profile_id"`
	Revision    int    `json:"revision"`
	Reason      string `json:"reason"`
	DeletedAt   string `json:"deleted_at"`
	Fingerprint string `json:"fingerprint"`
}

func (store *Store) ProtectBlock(ctx context.Context, state BlockState, expectedRevision int, actor, reason string) (BlockState, error) {
	if state.WorkspaceID != store.workspaceID || state.ProfileID != store.profileID {
		return BlockState{}, errors.New("catalog block state does not belong to selected scope")
	}
	if state.Revision != expectedRevision || expectedRevision < 1 {
		return BlockState{}, errors.New("expected_revision does not match the current block revision")
	}
	if state.Protected {
		return state, nil
	}
	input, err := blockStateMapping(state)
	if err != nil {
		return BlockState{}, err
	}
	output, err := blockStateMapping(state)
	if err != nil {
		return BlockState{}, err
	}
	output["protected"] = true
	err = immediate(ctx, store.db, func(conn *sql.Conn) error {
		previous, err := store.revisionConn(ctx, conn)
		if err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `UPDATE topology_blocks
			SET protected=1,revision=?,updated_at=?
			WHERE block_id=? AND workspace_id=? AND profile_id=? AND revision=?`,
			state.Revision+1, now(), state.Block.BlockID, store.workspaceID, store.profileID, expectedRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("expected_revision does not match the current block revision")
		}
		return store.mutationTx(ctx, conn, previous, "protect_block", actor, reason,
			[]string{state.Block.BlockID}, input, output)
	})
	if err != nil {
		return BlockState{}, err
	}
	protected, err := store.GetBlock(ctx, state.Block.BlockID)
	if err != nil {
		return BlockState{}, err
	}
	if protected == nil {
		return BlockState{}, errors.New("protected topology block is missing")
	}
	return *protected, nil
}

// DeleteBlock follows the catalog's low-level tombstone contract. The public
// lifecycle must first check children, edges, active masks and current records.
func (store *Store) DeleteBlock(ctx context.Context, state BlockState, expectedRevision int, actor, reason string) (Tombstone, error) {
	if state.WorkspaceID != store.workspaceID || state.ProfileID != store.profileID {
		return Tombstone{}, errors.New("catalog block state does not belong to selected scope")
	}
	if state.Protected {
		return Tombstone{}, errors.New("protected root cannot be deleted")
	}
	if state.Status != "archived" {
		return Tombstone{}, errors.New("hard delete requires an archived block")
	}
	if state.Revision != expectedRevision || expectedRevision < 1 {
		return Tombstone{}, errors.New("expected_revision does not match the current block revision")
	}
	if strings.TrimSpace(reason) == "" || strings.TrimSpace(actor) == "" {
		return Tombstone{}, errors.New("catalog mutation requires actor and reason")
	}
	input, err := blockStateMapping(state)
	if err != nil {
		return Tombstone{}, err
	}
	hash, err := fingerprint(input)
	if err != nil {
		return Tombstone{}, err
	}
	tombstone := Tombstone{BlockID: state.Block.BlockID, WorkspaceID: store.workspaceID,
		ProfileID: store.profileID, Revision: state.Revision, Reason: reason,
		DeletedAt: now(), Fingerprint: hash}
	payload, err := canonical(tombstone)
	if err != nil {
		return Tombstone{}, err
	}
	err = immediate(ctx, store.db, func(conn *sql.Conn) error {
		previous, err := store.revisionConn(ctx, conn)
		if err != nil {
			return err
		}
		if err := store.deleteGuardsConn(ctx, conn, state.Block.BlockID); err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `DELETE FROM topology_blocks
			WHERE block_id=? AND workspace_id=? AND profile_id=? AND revision=? AND status='archived' AND protected=0`,
			state.Block.BlockID, store.workspaceID, store.profileID, expectedRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("expected_revision does not match the current block revision")
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO topology_tombstones(
			block_id,workspace_id,profile_id,payload_json,deleted_at
		) VALUES(?,?,?,?,?)`, state.Block.BlockID, store.workspaceID, store.profileID, payload, tombstone.DeletedAt); err != nil {
			return err
		}
		return store.mutationTx(ctx, conn, previous, "delete_block", actor, reason,
			[]string{state.Block.BlockID}, input, tombstone)
	})
	if err != nil {
		return Tombstone{}, err
	}
	return tombstone, nil
}

func (store *Store) deleteGuardsConn(ctx context.Context, conn *sql.Conn, blockID string) error {
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_blocks WHERE
		workspace_id=? AND profile_id=? AND parent_id=?`, store.workspaceID,
		store.profileID, blockID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("hard delete requires no children")
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_edges WHERE
		workspace_id=? AND profile_id=? AND (src_id=? OR dst_id=?)`,
		store.workspaceID, store.profileID, blockID, blockID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("hard delete requires no graph edges")
	}
	rows, err := conn.QueryContext(ctx, `SELECT `+maskColumns+` FROM active_masks WHERE
		workspace_id=? AND profile_id=? AND status IN ('active','suspended')`,
		store.workspaceID, store.profileID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		mask, err := scanMask(rows, store.workspaceID, store.profileID)
		if err != nil {
			return err
		}
		if mask.IsExpired(time.Now().UTC()) {
			continue
		}
		for _, selected := range mask.SelectedBlockIDs {
			if selected == blockID {
				return errors.New("hard delete requires no active mask references")
			}
		}
	}
	return rows.Err()
}

// CheckDeleteGuards lets the public cascade path reject known catalog
// blockers before it retracts any canonical records. DeleteBlock rechecks the
// same guards in its own write transaction.
func (store *Store) CheckDeleteGuards(ctx context.Context, blockID string) error {
	return immediate(ctx, store.db, func(conn *sql.Conn) error {
		return store.deleteGuardsConn(ctx, conn, blockID)
	})
}

func (store *Store) GetTombstone(ctx context.Context, blockID string) (*Tombstone, error) {
	var payload string
	err := store.db.QueryRowContext(ctx, `SELECT payload_json FROM topology_tombstones
		WHERE workspace_id=? AND profile_id=? AND block_id=?`, store.workspaceID, store.profileID, strings.TrimSpace(blockID)).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tombstone Tombstone
	if err := json.Unmarshal([]byte(payload), &tombstone); err != nil {
		return nil, err
	}
	if tombstone.BlockID != strings.TrimSpace(blockID) || tombstone.WorkspaceID != store.workspaceID || tombstone.ProfileID != store.profileID {
		return nil, errors.New("stored topology tombstone identity differs from row")
	}
	return &tombstone, nil
}
