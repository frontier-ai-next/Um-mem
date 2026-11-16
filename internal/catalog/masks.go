package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func scanMask(row interface{ Scan(...any) error }, workspaceID, profileID string) (*memory.ActiveMemoryMask, error) {
	var rowID, rowWorkspace, rowProfile, sessionID, taskID, status, payload string
	var revision int
	err := row.Scan(&rowID, &rowWorkspace, &rowProfile, &sessionID, &taskID, &revision, &status, &payload)
	if err != nil {
		return nil, err
	}
	var mask memory.ActiveMemoryMask
	if err := json.Unmarshal([]byte(payload), &mask); err != nil {
		return nil, err
	}
	if err := mask.Normalize(); err != nil {
		return nil, err
	}
	if rowWorkspace != workspaceID || rowProfile != profileID || mask.WorkspaceID != rowWorkspace ||
		mask.ProfileID != rowProfile || mask.MaskID != rowID || mask.SessionID != sessionID ||
		mask.TaskID != taskID || mask.Revision != revision || mask.Status != status {
		return nil, errors.New("stored active memory mask identity differs from row")
	}
	return &mask, nil
}

const maskColumns = `mask_id,workspace_id,profile_id,session_id,task_id,revision,status,payload_json`

func (store *Store) maskConn(ctx context.Context, conn *sql.Conn, id string) (*memory.ActiveMemoryMask, error) {
	mask, err := scanMask(conn.QueryRowContext(ctx, `SELECT `+maskColumns+` FROM active_masks WHERE workspace_id=? AND profile_id=? AND mask_id=?`,
		store.workspaceID, store.profileID, strings.TrimSpace(id)), store.workspaceID, store.profileID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return mask, err
}

func (store *Store) GetMask(ctx context.Context, id string) (*memory.ActiveMemoryMask, error) {
	mask, err := scanMask(store.db.QueryRowContext(ctx, `SELECT `+maskColumns+` FROM active_masks WHERE workspace_id=? AND profile_id=? AND mask_id=?`,
		store.workspaceID, store.profileID, strings.TrimSpace(id)), store.workspaceID, store.profileID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return mask, err
}

func (store *Store) CurrentMask(ctx context.Context, sessionID, taskID string) (*memory.ActiveMemoryMask, error) {
	mask, err := scanMask(store.db.QueryRowContext(ctx, `SELECT `+maskColumns+` FROM active_masks
		WHERE workspace_id=? AND profile_id=? AND session_id=? AND task_id=?
		ORDER BY updated_at DESC, mask_id DESC LIMIT 1`,
		store.workspaceID, store.profileID, sessionID, taskID), store.workspaceID, store.profileID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return mask, err
}

// PutMask stores a new mask when expectedRevision is nil, otherwise performs
// a compare-and-swap update. Masks do not change the topology catalog revision.
func (store *Store) PutMask(ctx context.Context, mask memory.ActiveMemoryMask, expectedRevision *int) (memory.ActiveMemoryMask, error) {
	if err := mask.Normalize(); err != nil {
		return memory.ActiveMemoryMask{}, err
	}
	if mask.WorkspaceID != store.workspaceID || mask.ProfileID != store.profileID {
		return memory.ActiveMemoryMask{}, errors.New("active memory mask workspace/profile mismatch")
	}
	payload, err := canonical(mask)
	if err != nil {
		return memory.ActiveMemoryMask{}, err
	}
	err = immediate(ctx, store.db, func(conn *sql.Conn) error {
		current, err := store.maskConn(ctx, conn, mask.MaskID)
		if err != nil {
			return err
		}
		if expectedRevision == nil {
			if current != nil {
				return errors.New("active memory mask already exists")
			}
			_, err := conn.ExecContext(ctx, `INSERT INTO active_masks(
				mask_id,workspace_id,profile_id,session_id,task_id,revision,status,payload_json,created_at,updated_at
			) VALUES(?,?,?,?,?,?,?,?,?,?)`, mask.MaskID, mask.WorkspaceID, mask.ProfileID,
				mask.SessionID, mask.TaskID, mask.Revision, mask.Status, payload, mask.CreatedAt, mask.UpdatedAt)
			return err
		}
		if current == nil || current.Revision != *expectedRevision {
			return errors.New("expected_revision does not match the current mask revision")
		}
		result, err := conn.ExecContext(ctx, `UPDATE active_masks
			SET session_id=?,task_id=?,revision=?,status=?,payload_json=?,updated_at=?
			WHERE mask_id=? AND workspace_id=? AND profile_id=? AND revision=?`,
			mask.SessionID, mask.TaskID, mask.Revision, mask.Status, payload, mask.UpdatedAt,
			mask.MaskID, store.workspaceID, store.profileID, *expectedRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("expected_revision does not match the current mask revision")
		}
		return nil
	})
	if err != nil {
		return memory.ActiveMemoryMask{}, err
	}
	return mask, nil
}

func (store *Store) liveMasks(ctx context.Context, query string, args ...any) ([]memory.ActiveMemoryMask, error) {
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]memory.ActiveMemoryMask, 0)
	for rows.Next() {
		mask, err := scanMask(rows, store.workspaceID, store.profileID)
		if err != nil {
			return nil, err
		}
		if !mask.IsExpired(time.Now().UTC()) {
			result = append(result, *mask)
		}
	}
	return result, rows.Err()
}

func (store *Store) HasLiveMaskForScope(ctx context.Context, sessionID, taskID, excludeMaskID string) (bool, error) {
	masks, err := store.liveMasks(ctx, `SELECT `+maskColumns+` FROM active_masks
		WHERE workspace_id=? AND profile_id=? AND session_id=? AND task_id=? AND status IN ('active','suspended')`,
		store.workspaceID, store.profileID, sessionID, taskID)
	if err != nil {
		return false, err
	}
	for _, mask := range masks {
		if mask.MaskID != strings.TrimSpace(excludeMaskID) {
			return true, nil
		}
	}
	return false, nil
}

func (store *Store) BlockHasActiveMaskReference(ctx context.Context, blockID string) (bool, error) {
	masks, err := store.liveMasks(ctx, `SELECT `+maskColumns+` FROM active_masks
		WHERE workspace_id=? AND profile_id=? AND status IN ('active','suspended')`,
		store.workspaceID, store.profileID)
	if err != nil {
		return false, err
	}
	for _, mask := range masks {
		for _, selected := range mask.SelectedBlockIDs {
			if selected == blockID {
				return true, nil
			}
		}
	}
	return false, nil
}

func (store *Store) ValidateMaskBlocks(ctx context.Context, mask memory.ActiveMemoryMask) error {
	if len(mask.SelectedBlockIDs) == 0 {
		return errors.New("active memory mask requires selected_block_ids")
	}
	for _, id := range mask.SelectedBlockIDs {
		state, err := store.GetBlock(ctx, id)
		if err != nil {
			return err
		}
		if state == nil {
			return fmt.Errorf("active memory mask references block %q outside this profile", id)
		}
		if state.Status != "active" {
			return errors.New("active memory mask requires active selected blocks")
		}
	}
	return nil
}

func (store *Store) hasLiveMaskForScopeConn(ctx context.Context, conn *sql.Conn, sessionID, taskID, excludeID string) (bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT `+maskColumns+` FROM active_masks
		WHERE workspace_id=? AND profile_id=? AND session_id=? AND task_id=? AND status IN ('active','suspended')`,
		store.workspaceID, store.profileID, sessionID, taskID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		mask, err := scanMask(rows, store.workspaceID, store.profileID)
		if err != nil {
			return false, err
		}
		if mask.MaskID != excludeID && !mask.IsExpired(time.Now().UTC()) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ActivateMask performs the public creation checks inside a single catalog
// transaction, including block state and one live mask per exact scope.
func (store *Store) ActivateMask(ctx context.Context, mask memory.ActiveMemoryMask) (memory.ActiveMemoryMask, error) {
	if mask.MaskID == "" {
		id, err := uuid4()
		if err != nil {
			return memory.ActiveMemoryMask{}, err
		}
		mask.MaskID = id
	}
	if mask.Lifecycle == "" {
		mask.Lifecycle = "durable"
	}
	if mask.CreatedAt == "" {
		mask.CreatedAt = now()
	}
	if mask.UpdatedAt == "" {
		mask.UpdatedAt = mask.CreatedAt
	}
	if mask.Revision != 1 || mask.Status != "active" {
		return memory.ActiveMemoryMask{}, errors.New("new active memory mask requires active status and revision one")
	}
	if err := mask.Normalize(); err != nil {
		return memory.ActiveMemoryMask{}, err
	}
	if mask.WorkspaceID != store.workspaceID || mask.ProfileID != store.profileID {
		return memory.ActiveMemoryMask{}, errors.New("active memory mask workspace/profile mismatch")
	}
	if len(mask.SelectedBlockIDs) == 0 {
		return memory.ActiveMemoryMask{}, errors.New("active memory mask requires selected_block_ids")
	}
	err := immediate(ctx, store.db, func(conn *sql.Conn) error {
		for _, blockID := range mask.SelectedBlockIDs {
			var status string
			err := conn.QueryRowContext(ctx, `SELECT status FROM topology_blocks
				WHERE workspace_id=? AND profile_id=? AND block_id=?`, store.workspaceID, store.profileID, blockID).Scan(&status)
			if err == sql.ErrNoRows {
				return errors.New("active memory mask references blocks outside this profile")
			}
			if err != nil {
				return err
			}
			if status != "active" {
				return errors.New("active memory mask requires active selected blocks")
			}
		}
		live, err := store.hasLiveMaskForScopeConn(ctx, conn, mask.SessionID, mask.TaskID, "")
		if err != nil {
			return err
		}
		if live {
			return errors.New("an active memory mask already exists for this exact scope")
		}
		mask.CatalogRevision, err = store.revisionConn(ctx, conn)
		if err != nil {
			return err
		}
		payload, err := canonical(mask)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO active_masks(
			mask_id,workspace_id,profile_id,session_id,task_id,revision,status,payload_json,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?)`, mask.MaskID, mask.WorkspaceID, mask.ProfileID,
			mask.SessionID, mask.TaskID, mask.Revision, mask.Status, payload, mask.CreatedAt, mask.UpdatedAt)
		return err
	})
	if err != nil {
		return memory.ActiveMemoryMask{}, err
	}
	return mask, nil
}

type MaskAmendment struct {
	MaskID            string
	ExpectedRevision  int
	SessionID         string
	TaskID            string
	ActivationReason  string
	SelectedBlockIDs  *[]string
	ContextRefs       *[]string
	PinnedRecordIDs   *[]string
	Selectors         *[]memory.ActiveMemorySelector
	Priorities        *[]memory.ActiveMemoryPriority
	TokenBudget       *int
	BlockTokenBudgets *[]memory.ActiveMemoryBlockBudget
	ExpiresAt         *string
}

// AmendMaskForScope checks identity, block state and revision in one catalog
// transaction so a concurrent owner cannot move a mask across sessions.
func (store *Store) AmendMaskForScope(ctx context.Context, change MaskAmendment) (memory.ActiveMemoryMask, error) {
	if change.MaskID == "" || change.ExpectedRevision < 1 || strings.TrimSpace(change.ActivationReason) == "" {
		return memory.ActiveMemoryMask{}, errors.New("active mask amendment requires ID, expected revision and reason")
	}
	var updated memory.ActiveMemoryMask
	err := immediate(ctx, store.db, func(conn *sql.Conn) error {
		current, err := store.maskConn(ctx, conn, change.MaskID)
		if err != nil {
			return err
		}
		if current == nil {
			return errors.New("active memory mask was not found")
		}
		if current.SessionID != change.SessionID || current.TaskID != change.TaskID {
			return errors.New("active memory mask scope differs from caller lineage")
		}
		if current.Revision != change.ExpectedRevision {
			return errors.New("expected_revision does not match the current mask revision")
		}
		updated = *current
		updated.Revision++
		updated.ActivationReason = strings.TrimSpace(change.ActivationReason)
		updated.UpdatedAt = now()
		if change.SelectedBlockIDs != nil {
			updated.SelectedBlockIDs = *change.SelectedBlockIDs
		}
		if change.ContextRefs != nil {
			updated.ContextRefs = *change.ContextRefs
		}
		if change.PinnedRecordIDs != nil {
			updated.PinnedRecordIDs = *change.PinnedRecordIDs
		}
		if change.Selectors != nil {
			updated.Selectors = *change.Selectors
		}
		if change.Priorities != nil {
			updated.Priorities = *change.Priorities
		}
		if change.TokenBudget != nil {
			updated.TokenBudget = *change.TokenBudget
		}
		if change.BlockTokenBudgets != nil {
			updated.BlockTokenBudgets = *change.BlockTokenBudgets
		}
		if change.ExpiresAt != nil {
			updated.ExpiresAt = *change.ExpiresAt
		}
		if err := updated.Normalize(); err != nil {
			return err
		}
		if len(updated.SelectedBlockIDs) == 0 {
			return errors.New("active memory mask requires selected_block_ids")
		}
		for _, blockID := range updated.SelectedBlockIDs {
			var status string
			err := conn.QueryRowContext(ctx, `SELECT status FROM topology_blocks
				WHERE workspace_id=? AND profile_id=? AND block_id=?`, store.workspaceID, store.profileID, blockID).Scan(&status)
			if err == sql.ErrNoRows {
				return errors.New("active memory mask references blocks outside this profile")
			}
			if err != nil {
				return err
			}
			if status != "active" {
				return errors.New("active memory mask requires active selected blocks")
			}
		}
		updated.CatalogRevision, err = store.revisionConn(ctx, conn)
		if err != nil {
			return err
		}
		payload, err := canonical(updated)
		if err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `UPDATE active_masks SET revision=?,status=?,payload_json=?,updated_at=?
			WHERE workspace_id=? AND profile_id=? AND mask_id=? AND revision=?`,
			updated.Revision, updated.Status, payload, updated.UpdatedAt,
			store.workspaceID, store.profileID, updated.MaskID, change.ExpectedRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("expected_revision does not match the current mask revision")
		}
		return nil
	})
	if err != nil {
		return memory.ActiveMemoryMask{}, err
	}
	return updated, nil
}

// TransitionMask applies the public suspend, resume and deactivate lifecycle.
func (store *Store) TransitionMask(ctx context.Context, maskID string, expectedRevision int, status, reason string) (memory.ActiveMemoryMask, error) {
	return store.transitionMask(ctx, maskID, expectedRevision, status, reason, nil)
}

// TransitionMaskForScope enforces the caller's session/task identity within
// the same transaction that advances the mask revision.
func (store *Store) TransitionMaskForScope(ctx context.Context, maskID string, expectedRevision int,
	status, reason, sessionID, taskID string) (memory.ActiveMemoryMask, error) {
	return store.transitionMask(ctx, maskID, expectedRevision, status, reason, &struct{ session, task string }{sessionID, taskID})
}

func (store *Store) transitionMask(ctx context.Context, maskID string, expectedRevision int, status, reason string,
	scope *struct{ session, task string }) (memory.ActiveMemoryMask, error) {
	if expectedRevision < 1 || strings.TrimSpace(reason) == "" {
		return memory.ActiveMemoryMask{}, errors.New("mask transition requires expected revision and reason")
	}
	var updated memory.ActiveMemoryMask
	err := immediate(ctx, store.db, func(conn *sql.Conn) error {
		current, err := store.maskConn(ctx, conn, maskID)
		if err != nil {
			return err
		}
		if current == nil {
			return errors.New("active memory mask was not found")
		}
		if scope != nil && (current.SessionID != scope.session || current.TaskID != scope.task) {
			return errors.New("active memory mask scope differs from caller lineage")
		}
		if current.Revision != expectedRevision {
			return errors.New("expected_revision does not match the current mask revision")
		}
		allowed := current.Status == "active" && (status == "suspended" || status == "expired") ||
			current.Status == "suspended" && (status == "active" || status == "expired")
		if !allowed {
			return errors.New("active memory mask lifecycle transition is invalid")
		}
		if status == "active" {
			if current.IsExpired(time.Now().UTC()) {
				return errors.New("an elapsed active memory mask cannot be resumed")
			}
			live, err := store.hasLiveMaskForScopeConn(ctx, conn, current.SessionID, current.TaskID, current.MaskID)
			if err != nil {
				return err
			}
			if live {
				return errors.New("another active memory mask already exists for this exact scope")
			}
		}
		updated = *current
		updated.Revision++
		updated.Status = status
		updated.ActivationReason = strings.TrimSpace(reason)
		updated.UpdatedAt = now()
		payload, err := canonical(updated)
		if err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `UPDATE active_masks SET revision=?,status=?,payload_json=?,updated_at=?
			WHERE workspace_id=? AND profile_id=? AND mask_id=? AND revision=?`, updated.Revision,
			updated.Status, payload, updated.UpdatedAt, store.workspaceID, store.profileID,
			updated.MaskID, expectedRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("expected_revision does not match the current mask revision")
		}
		return nil
	})
	if err != nil {
		return memory.ActiveMemoryMask{}, err
	}
	return updated, nil
}
