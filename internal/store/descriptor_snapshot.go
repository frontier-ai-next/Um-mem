package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// LogicalBlockDescriptorSnapshot reads current coverage through published
// topology pointers. It samples at most 64 identities and never decodes a
// canonical record payload into the Go memory model.
func (index *Index) LogicalBlockDescriptorSnapshot(ctx context.Context, blockID string) (BlockDescriptorSnapshot, error) {
	result := BlockDescriptorSnapshot{RelatedBlockIDs: []string{}, SourceRevisionIDs: []string{}, ExactDimensions: []string{}}
	if index == nil || index.db == nil || strings.TrimSpace(blockID) == "" ||
		index.workspaceID == "" || index.profileID == "" {
		return result, fmt.Errorf("descriptor snapshot requires an exact block and scope")
	}
	ids, err := index.RecordIDsForLogicalBlocks(ctx, []string{blockID})
	if err != nil {
		return result, err
	}
	type row struct{ id, revision, title, updated string }
	visible := make([]row, 0, len(ids))
	for offset := 0; offset < len(ids); offset += 400 {
		chunk := ids[offset:min(offset+400, len(ids))]
		args := make([]any, 0, len(chunk)+2)
		args = append(args, index.workspaceID, index.profileID)
		placeholders := make([]string, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
			placeholders = append(placeholders, "?")
		}
		rows, err := index.db.QueryContext(ctx, `SELECT block_id,
			json_extract(payload_json,'$.revision_id'),
			COALESCE(json_extract(payload_json,'$.title'),''),updated_at
			FROM blocks WHERE workspace_id=?
			AND json_extract(payload_json,'$.profile_id')=?
			AND status NOT IN ('superseded','deprecated','retracted','quarantined','expired')
			AND block_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var item row
			if err := rows.Scan(&item.id, &item.revision, &item.title, &item.updated); err != nil {
				rows.Close()
				return result, err
			}
			visible = append(visible, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return result, err
		}
		if err := rows.Close(); err != nil {
			return result, err
		}
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].id < visible[j].id })
	result.RecordCount = len(visible)
	result.Truncated = len(visible) > 64
	for position, item := range visible {
		if position < 64 {
			result.RelatedBlockIDs = append(result.RelatedBlockIDs, item.id)
			result.SourceRevisionIDs = append(result.SourceRevisionIDs, item.revision)
		}
		if result.LatestUpdatedAt == "" || item.updated > result.LatestUpdatedAt ||
			(item.updated == result.LatestUpdatedAt && item.id > result.latestID) {
			result.LatestRecordTitle, result.LatestUpdatedAt, result.latestID = item.title, item.updated, item.id
		}
	}
	sort.Strings(result.SourceRevisionIDs)
	result.SourceRevisionIDs = dedupSortedStrings(result.SourceRevisionIDs)
	for offset := 0; offset < len(visible); offset += 400 {
		chunk := visible[offset:min(offset+400, len(visible))]
		args := make([]any, 0, len(chunk)+1)
		args = append(args, index.workspaceID)
		placeholders := make([]string, 0, len(chunk))
		for _, item := range chunk {
			args = append(args, item.id)
			placeholders = append(placeholders, "?")
		}
		rows, err := index.db.QueryContext(ctx, `SELECT DISTINCT term_type FROM memory_exact_terms
			WHERE workspace_id=? AND block_id IN (`+strings.Join(placeholders, ",")+`)
			AND term_type IN ('path','symbol','entity','config_key','endpoint','artifact_hash')`, args...)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var kind string
			if err := rows.Scan(&kind); err != nil {
				rows.Close()
				return result, err
			}
			result.ExactDimensions = append(result.ExactDimensions, exactDimensionName(kind))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return result, err
		}
		if err := rows.Close(); err != nil {
			return result, err
		}
	}
	sort.Strings(result.ExactDimensions)
	result.ExactDimensions = dedupSortedStrings(result.ExactDimensions)
	return result, nil
}

type BlockDescriptorSnapshot struct {
	RecordCount       int
	RelatedBlockIDs   []string
	SourceRevisionIDs []string
	Truncated         bool
	LatestRecordTitle string
	LatestUpdatedAt   string
	ExactDimensions   []string
	latestID          string
}

func exactDimensionName(kind string) string {
	switch kind {
	case "path":
		return "paths"
	case "symbol":
		return "symbols"
	case "entity":
		return "entities"
	case "config_key":
		return "config_keys"
	case "endpoint":
		return "endpoints"
	case "artifact_hash":
		return "artifact_hashes"
	default:
		return ""
	}
}
