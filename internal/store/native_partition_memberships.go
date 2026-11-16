package store

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// NativePartitionMembership identifies one persisted logical leaf and its
// stable backend partition key. It contains no canonical record content.
type NativePartitionMembership struct {
	LogicalBlockID     string
	CatalogRevisionID  string
	BackendPartitionID string
}

// NativePartitionMemberships reads the selected profile's native partition
// identities, including published split/merge targets. Archived source blocks
// are omitted when a topology catalog is attached.
func (index *Index) NativePartitionMemberships(ctx context.Context, blockIDs ...string) ([]NativePartitionMembership, error) {
	selected := make(map[string]bool, len(blockIDs))
	selectedIDs := make([]string, 0, len(blockIDs))
	for _, id := range blockIDs {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, errors.New("native partition lookup requires exact block IDs")
		}
		if !selected[id] {
			selectedIDs = append(selectedIDs, id)
		}
		selected[id] = true
	}
	if len(selected) > 201 {
		return nil, errors.New("native partition lookup exceeds selected page limit")
	}
	query := `SELECT DISTINCT membership.logical_block_id,
		membership.catalog_revision_id,membership.backend_partition_id
		FROM record_block_memberships AS membership
		JOIN blocks AS record ON record.block_id=membership.record_id
		WHERE membership.workspace_id=? AND membership.role='primary'
		AND json_extract(record.payload_json,'$.profile_id')=?`
	args := []any{index.workspaceID, index.profileID}
	if len(selected) > 0 {
		placeholders := make([]string, 0, len(selectedIDs))
		for _, id := range selectedIDs {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		query += ` AND membership.logical_block_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	rows, err := index.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	identities := make(map[NativePartitionMembership]bool)
	for rows.Next() {
		var membership NativePartitionMembership
		if err := rows.Scan(&membership.LogicalBlockID, &membership.CatalogRevisionID,
			&membership.BackendPartitionID); err != nil {
			rows.Close()
			return nil, err
		}
		identities[membership] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if index.topology != nil {
		states, err := index.topology.ListBlocks(ctx, true)
		if err != nil {
			return nil, err
		}
		archived := make(map[string]bool, len(states))
		for _, state := range states {
			if state.Status != "active" {
				archived[state.Block.BlockID] = true
			}
		}
		for identity := range identities {
			if archived[identity.LogicalBlockID] || len(selected) > 0 && !selected[identity.LogicalBlockID] {
				delete(identities, identity)
			}
		}
		published, err := index.topology.PublishedPartitionMemberships(ctx)
		if err != nil {
			return nil, err
		}
		for _, pointer := range published {
			if archived[pointer.BlockID] || len(selected) > 0 && !selected[pointer.BlockID] {
				continue
			}
			identities[NativePartitionMembership{LogicalBlockID: pointer.BlockID,
				CatalogRevisionID:  pointer.CatalogRevisionID,
				BackendPartitionID: pointer.BackendPartitionID}] = true
		}
	}
	result := make([]NativePartitionMembership, 0, len(identities))
	for identity := range identities {
		result = append(result, identity)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.LogicalBlockID != right.LogicalBlockID {
			return left.LogicalBlockID < right.LogicalBlockID
		}
		if left.CatalogRevisionID != right.CatalogRevisionID {
			return left.CatalogRevisionID < right.CatalogRevisionID
		}
		return left.BackendPartitionID < right.BackendPartitionID
	})
	return result, nil
}
