package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// PublishedRecordMembership is the catalog pointer that supersedes a record's
// original logical-block membership after a published split or merge.
type PublishedRecordMembership struct {
	RecordID           string `json:"record_id"`
	RecordRevisionID   string `json:"record_revision_id"`
	BlockID            string `json:"block_id"`
	BackendPartitionID string `json:"backend_partition_id"`
	CatalogRevisionID  string `json:"catalog_revision_id"`
}

// PublishedPartitionMembership is a distinct target identity in a committed
// topology generation. It contains no record ID or canonical payload.
type PublishedPartitionMembership struct {
	BlockID            string
	CatalogRevisionID  string
	BackendPartitionID string
}

func (store *Store) PublishedPartitionMemberships(ctx context.Context) ([]PublishedPartitionMembership, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT DISTINCT block_id,catalog_revision_id,
		backend_partition_id FROM published_record_memberships
		WHERE workspace_id=? AND profile_id=? ORDER BY block_id,catalog_revision_id,
		backend_partition_id`, store.workspaceID, store.profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PublishedPartitionMembership, 0)
	for rows.Next() {
		var identity PublishedPartitionMembership
		if err := rows.Scan(&identity.BlockID, &identity.CatalogRevisionID,
			&identity.BackendPartitionID); err != nil {
			return nil, err
		}
		result = append(result, identity)
	}
	return result, rows.Err()
}

// PublishedMembershipsForRecords resolves a bounded batch of exact record
// pointers. Callers use it to remove records moved out of a granted block.
func (store *Store) PublishedMembershipsForRecords(ctx context.Context,
	recordIDs []string) (map[string]PublishedRecordMembership, error) {
	result := make(map[string]PublishedRecordMembership)
	if len(recordIDs) == 0 {
		return result, nil
	}
	for offset := 0; offset < len(recordIDs); offset += 400 {
		chunk := recordIDs[offset:min(offset+400, len(recordIDs))]
		args := make([]any, 0, len(chunk)+2)
		args = append(args, store.workspaceID, store.profileID)
		placeholders := make([]string, 0, len(chunk))
		for _, id := range chunk {
			if strings.TrimSpace(id) == "" {
				return nil, errors.New("record_id is required")
			}
			args = append(args, id)
			placeholders = append(placeholders, "?")
		}
		rows, err := store.db.QueryContext(ctx, `SELECT record_id,record_revision_id,block_id,
			backend_partition_id,catalog_revision_id FROM published_record_memberships
			WHERE workspace_id=? AND profile_id=? AND record_id IN (`+
			strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var pointer PublishedRecordMembership
			if err := rows.Scan(&pointer.RecordID, &pointer.RecordRevisionID,
				&pointer.BlockID, &pointer.BackendPartitionID,
				&pointer.CatalogRevisionID); err != nil {
				rows.Close()
				return nil, err
			}
			result[pointer.RecordID] = pointer
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// MembershipSnapshot pins one published generation for a multi-record read.
type MembershipSnapshot struct {
	store *Store
	tx    *sql.Tx
}

func (store *Store) OpenMembershipSnapshot(ctx context.Context) (*MembershipSnapshot, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT value FROM catalog_metadata
		WHERE key='catalog_revision'`).Scan(&revision); err != nil {
		tx.Rollback()
		return nil, err
	}
	return &MembershipSnapshot{store: store, tx: tx}, nil
}

func (snapshot *MembershipSnapshot) Close() error { return snapshot.tx.Rollback() }

func (snapshot *MembershipSnapshot) EffectiveRecord(ctx context.Context,
	record memory.MemoryRecord) (memory.MemoryRecord, error) {
	if record.WorkspaceID != snapshot.store.workspaceID || record.ProfileID != snapshot.store.profileID {
		return memory.MemoryRecord{}, errors.New("record scope differs from selected catalog")
	}
	if record.BlockMembership == nil {
		return record, nil
	}
	row := snapshot.tx.QueryRowContext(ctx, `SELECT record_id,record_revision_id,block_id,
		backend_partition_id,catalog_revision_id FROM published_record_memberships
		WHERE record_id=? AND workspace_id=? AND profile_id=?`,
		record.BlockID, snapshot.store.workspaceID, snapshot.store.profileID)
	var pointer PublishedRecordMembership
	if err := row.Scan(&pointer.RecordID, &pointer.RecordRevisionID,
		&pointer.BlockID, &pointer.BackendPartitionID, &pointer.CatalogRevisionID); err != nil {
		if err == sql.ErrNoRows {
			return record, nil
		}
		return memory.MemoryRecord{}, err
	}
	if pointer.RecordRevisionID != record.RevisionID {
		return record, nil
	}
	return applyPublishedMembership(record, pointer), nil
}

func (store *Store) PublishedRecordMembership(ctx context.Context,
	recordID, recordRevisionID string) (*PublishedRecordMembership, error) {
	if strings.TrimSpace(recordID) == "" {
		return nil, errors.New("record_id is required")
	}
	row := store.db.QueryRowContext(ctx, `SELECT record_id,record_revision_id,block_id,
		backend_partition_id,catalog_revision_id FROM published_record_memberships
		WHERE record_id=? AND workspace_id=? AND profile_id=?`,
		recordID, store.workspaceID, store.profileID)
	var membership PublishedRecordMembership
	if err := row.Scan(&membership.RecordID, &membership.RecordRevisionID,
		&membership.BlockID, &membership.BackendPartitionID,
		&membership.CatalogRevisionID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if recordRevisionID != "" && membership.RecordRevisionID != recordRevisionID {
		return nil, nil
	}
	return &membership, nil
}

func (store *Store) PublishedMembershipsForBlocks(ctx context.Context,
	blockIDs []string) (map[string]PublishedRecordMembership, error) {
	result := make(map[string]PublishedRecordMembership)
	if len(blockIDs) == 0 {
		return result, nil
	}
	selected := make(map[string]bool, len(blockIDs))
	selectedIDs := make([]string, 0, len(blockIDs))
	for _, id := range blockIDs {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("block_id is required")
		}
		if !selected[id] {
			selectedIDs = append(selectedIDs, id)
			selected[id] = true
		}
	}
	arguments := make([]any, 0, len(selectedIDs)+2)
	arguments = append(arguments, store.workspaceID, store.profileID)
	placeholders := make([]string, 0, len(selectedIDs))
	for _, id := range selectedIDs {
		arguments = append(arguments, id)
		placeholders = append(placeholders, "?")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT record_id,record_revision_id,block_id,
		backend_partition_id,catalog_revision_id FROM published_record_memberships
		WHERE workspace_id=? AND profile_id=? AND block_id IN (`+
		strings.Join(placeholders, ",")+`)`, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var membership PublishedRecordMembership
		if err := rows.Scan(&membership.RecordID, &membership.RecordRevisionID,
			&membership.BlockID, &membership.BackendPartitionID,
			&membership.CatalogRevisionID); err != nil {
			return nil, err
		}
		result[membership.RecordID] = membership
	}
	return result, rows.Err()
}

// EffectiveRecord applies only a published pointer for the exact stored
// record revision. The canonical record remains immutable on disk.
func (store *Store) EffectiveRecord(ctx context.Context,
	record memory.MemoryRecord) (memory.MemoryRecord, error) {
	if record.WorkspaceID != store.workspaceID || record.ProfileID != store.profileID {
		return memory.MemoryRecord{}, errors.New("record scope differs from selected catalog")
	}
	if record.BlockMembership == nil {
		return record, nil
	}
	pointer, err := store.PublishedRecordMembership(ctx, record.BlockID, record.RevisionID)
	if err != nil || pointer == nil {
		return record, err
	}
	return applyPublishedMembership(record, *pointer), nil
}

func applyPublishedMembership(record memory.MemoryRecord,
	pointer PublishedRecordMembership) memory.MemoryRecord {
	membership := *record.BlockMembership
	membership.PrimaryBlockID = pointer.BlockID
	membership.BackendPartitionID = pointer.BackendPartitionID
	membership.CatalogRevisionID = pointer.CatalogRevisionID
	secondary := make([]string, 0, len(membership.SecondaryBlockIDs))
	for _, id := range membership.SecondaryBlockIDs {
		if id != pointer.BlockID {
			secondary = append(secondary, id)
		}
	}
	membership.SecondaryBlockIDs = secondary
	membership.MembershipRevisionID = membership.RevisionHash()
	record.BlockMembership = &membership
	return record
}

// EffectiveRecords pins one catalog snapshot while overlaying a batch of
// canonical records, so a single result cannot mix two published generations.
func (store *Store) EffectiveRecords(ctx context.Context,
	records []memory.MemoryRecord) ([]memory.MemoryRecord, error) {
	result := append([]memory.MemoryRecord(nil), records...)
	if len(result) == 0 {
		return result, nil
	}
	ids := make([]string, 0, len(result))
	seen := make(map[string]bool, len(result))
	for _, record := range result {
		if record.WorkspaceID != store.workspaceID || record.ProfileID != store.profileID {
			return nil, errors.New("record scope differs from selected catalog")
		}
		if record.BlockMembership != nil && !seen[record.BlockID] {
			ids = append(ids, record.BlockID)
			seen[record.BlockID] = true
		}
	}
	if len(ids) == 0 {
		return result, nil
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	pointers := make(map[string]PublishedRecordMembership)
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		arguments := make([]any, 0, end-start+2)
		arguments = append(arguments, store.workspaceID, store.profileID)
		placeholders := make([]string, 0, end-start)
		for _, id := range ids[start:end] {
			arguments = append(arguments, id)
			placeholders = append(placeholders, "?")
		}
		rows, err := tx.QueryContext(ctx, `SELECT record_id,record_revision_id,block_id,
			backend_partition_id,catalog_revision_id FROM published_record_memberships
			WHERE workspace_id=? AND profile_id=? AND record_id IN (`+
			strings.Join(placeholders, ",")+`)`, arguments...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var pointer PublishedRecordMembership
			if err := rows.Scan(&pointer.RecordID, &pointer.RecordRevisionID,
				&pointer.BlockID, &pointer.BackendPartitionID,
				&pointer.CatalogRevisionID); err != nil {
				rows.Close()
				return nil, err
			}
			pointers[pointer.RecordID] = pointer
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for i, record := range result {
		if pointer, found := pointers[record.BlockID]; found &&
			record.BlockMembership != nil && pointer.RecordRevisionID == record.RevisionID {
			result[i] = applyPublishedMembership(record, pointer)
		}
	}
	return result, nil
}
