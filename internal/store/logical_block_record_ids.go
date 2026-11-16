package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// RecordIDsForLogicalBlocks resolves current primary memberships for exact
// granted blocks. Published split/merge pointers replace their stored source
// memberships without loading canonical record content.
func (index *Index) RecordIDsForLogicalBlocks(ctx context.Context, blockIDs []string) ([]string, error) {
	selected := make([]string, 0, len(blockIDs))
	wanted := make(map[string]bool, len(blockIDs))
	for _, id := range blockIDs {
		id = strings.TrimSpace(id)
		if id != "" && !wanted[id] {
			wanted[id] = true
			selected = append(selected, id)
		}
	}
	if len(selected) == 0 {
		return []string{}, nil
	}
	ids := make(map[string]bool)
	for offset := 0; offset < len(selected); offset += 400 {
		chunk := selected[offset:min(offset+400, len(selected))]
		args := make([]any, 0, len(chunk)+2)
		args = append(args, index.workspaceID)
		placeholders := make([]string, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
			placeholders = append(placeholders, "?")
		}
		args = append(args, index.profileID)
		rows, err := index.db.QueryContext(ctx, `SELECT DISTINCT membership.record_id
			FROM record_block_memberships AS membership
			JOIN blocks AS record ON record.block_id=membership.record_id
			WHERE membership.workspace_id=? AND membership.role='primary'
			AND membership.is_current=1 AND membership.logical_block_id IN (`+
			strings.Join(placeholders, ",")+`)
			AND json_extract(record.payload_json,'$.profile_id')=?
			AND NOT EXISTS (SELECT 1 FROM canonical_head_outbox AS pending
				WHERE pending.successor_id=membership.record_id
				AND pending.status<>'committed')`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	if index.topology != nil {
		baseIDs := make([]string, 0, len(ids))
		for id := range ids {
			baseIDs = append(baseIDs, id)
		}
		moved, err := index.topology.PublishedMembershipsForRecords(ctx, baseIDs)
		if err != nil {
			return nil, err
		}
		for id, pointer := range moved {
			if !wanted[pointer.BlockID] {
				delete(ids, id)
			}
		}
		assigned, err := index.topology.PublishedMembershipsForBlocks(ctx, selected)
		if err != nil {
			return nil, err
		}
		assignedIDs := make([]string, 0, len(assigned))
		for id := range assigned {
			assignedIDs = append(assignedIDs, id)
		}
		for offset := 0; offset < len(assignedIDs); offset += 400 {
			chunk := assignedIDs[offset:min(offset+400, len(assignedIDs))]
			args := make([]any, 0, len(chunk)+2)
			args = append(args, index.workspaceID, index.profileID)
			placeholders := make([]string, 0, len(chunk))
			for _, id := range chunk {
				args = append(args, id)
				placeholders = append(placeholders, "?")
			}
			rows, err := index.db.QueryContext(ctx, `SELECT temporal.block_id
				FROM memory_temporal_index AS temporal
				JOIN blocks AS record ON record.block_id=temporal.block_id
				WHERE temporal.workspace_id=? AND temporal.is_current=1
				AND json_extract(record.payload_json,'$.profile_id')=?
				AND temporal.block_id IN (`+strings.Join(placeholders, ",")+`)
				AND NOT EXISTS (SELECT 1 FROM canonical_head_outbox AS pending
					WHERE pending.successor_id=temporal.block_id
					AND pending.status<>'committed')`, args...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				ids[id] = true
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		if id == "" {
			return nil, fmt.Errorf("canonical index returned an empty record ID")
		}
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}
