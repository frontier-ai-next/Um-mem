package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
	"github.com/gofrs/flock"
)

// RebuildBlockDescriptorIndexes reconstructs catalog-backed descriptor nodes
// from canonical memberships. It is suitable for stores without published
// topology changes; callers with split/merge state use the snapshot variant.
func (writer *Writer) RebuildBlockDescriptorIndexes(ctx context.Context, catalog memory.SemanticCatalog) (string, error) {
	return writer.rebuildBlockDescriptorIndexes(ctx, catalog, nil)
}

type DescriptorSnapshotResolver func(context.Context, string, string) (BlockDescriptorSnapshot, error)

// RebuildBlockDescriptorIndexesWithSnapshots uses profile-aware published
// membership views supplied by the caller for split and merge coverage.
func (writer *Writer) RebuildBlockDescriptorIndexesWithSnapshots(ctx context.Context,
	catalog memory.SemanticCatalog, resolve DescriptorSnapshotResolver) (string, error) {
	if resolve == nil {
		return "", errors.New("descriptor snapshot resolver is required")
	}
	return writer.rebuildBlockDescriptorIndexes(ctx, catalog, resolve)
}

func (writer *Writer) rebuildBlockDescriptorIndexes(ctx context.Context,
	catalog memory.SemanticCatalog, resolve DescriptorSnapshotResolver) (checksum string, rebuildErr error) {
	if writer == nil || writer.db == nil || writer.path == "" {
		return "", errors.New("canonical index writer is closed")
	}
	if catalog.RevisionID == "" || len(catalog.Descriptors) == 0 {
		return "", errors.New("block descriptor rebuild requires a semantic catalog")
	}
	lock := flock.New(writer.path + ".canonical.lock")
	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return "", err
	}
	if !locked {
		return "", errors.New("canonical index rebuild gate timed out")
	}
	defer func() { rebuildErr = errors.Join(rebuildErr, lock.Unlock()) }()
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM index_nodes WHERE workspace_id=?
		AND node_type='block_descriptor'`, writer.workspaceID); err != nil {
		return "", err
	}
	byID := make(map[string]memory.BlockDescriptor, len(catalog.Descriptors))
	for _, descriptor := range catalog.Descriptors {
		byID[descriptor.BlockID] = descriptor
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM blocks
		WHERE workspace_id=? ORDER BY block_id`, writer.workspaceID)
	if err != nil {
		return "", err
	}
	firstByBlock := map[string]memory.MemoryRecord{}
	recordsByID := map[string]memory.MemoryRecord{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			rows.Close()
			return "", err
		}
		record, err := memory.DecodeCanonicalRecord([]byte(payload))
		if err != nil {
			rows.Close()
			return "", err
		}
		if record.BlockMembership == nil {
			continue
		}
		recordsByID[record.BlockID] = record
		if resolve != nil && record.ProfileID != writer.profileID {
			continue
		}
		blockID := record.BlockMembership.PrimaryBlockID
		if _, exists := firstByBlock[blockID]; !exists {
			firstByBlock[blockID] = record
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	resolved := map[string]BlockDescriptorSnapshot{}
	if resolve != nil {
		for _, descriptor := range catalog.Descriptors {
			view, err := resolve(ctx, writer.profileID, descriptor.BlockID)
			if err != nil {
				return "", err
			}
			resolved[descriptor.BlockID] = view
			if view.RecordCount == 0 {
				continue
			}
			if len(view.RelatedBlockIDs) == 0 {
				return "", errors.New("descriptor snapshot has coverage without a record sample")
			}
			if _, exists := firstByBlock[descriptor.BlockID]; !exists {
				seed, exists := recordsByID[view.RelatedBlockIDs[0]]
				if !exists || seed.ProfileID != writer.profileID {
					return "", fmt.Errorf("descriptor sample for %q is absent from canonical store", descriptor.BlockID)
				}
				firstByBlock[descriptor.BlockID] = seed
			}
		}
	}
	blockIDs := make([]string, 0, len(firstByBlock))
	for blockID := range firstByBlock {
		blockIDs = append(blockIDs, blockID)
	}
	sort.Strings(blockIDs)
	for _, blockID := range blockIDs {
		record := firstByBlock[blockID]
		descriptor, exists := byID[blockID]
		if !exists {
			if resolve != nil {
				view, err := resolve(ctx, writer.profileID, blockID)
				if err != nil {
					return "", err
				}
				if view.RecordCount == 0 {
					continue // Archived split/merge source outside the active catalog.
				}
			}
			if record.Status == "retracted" {
				continue
			}
			return "", fmt.Errorf("semantic catalog has no descriptor for logical block %q", blockID)
		}
		var snapshot blockDescriptorSnapshot
		if resolve == nil {
			snapshot, err = blockDescriptorSnapshotTx(ctx, tx, writer.workspaceID, record.ProfileID, blockID)
		} else {
			view := resolved[blockID]
			snapshot = blockDescriptorSnapshot{recordCount: view.RecordCount,
				relatedIDs: view.RelatedBlockIDs, sourceRevisionIDs: view.SourceRevisionIDs,
				latestTitle: view.LatestRecordTitle, latestUpdatedAt: view.LatestUpdatedAt,
				exactDimensions: view.ExactDimensions}
		}
		if err != nil {
			return "", err
		}
		revisionPayload := map[string]any{"block_id": blockID,
			"catalog_revision_id": catalog.RevisionID, "index_revision": 1,
			"source_revision_ids": snapshot.sourceRevisionIDs, "record_count": snapshot.recordCount}
		revisionJSON, err := jsoncompat.MarshalPython(revisionPayload, false)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(revisionJSON)
		revisionID := "block-index:sha256:" + hex.EncodeToString(sum[:])
		latestUpdatedAt := snapshot.latestUpdatedAt
		if latestUpdatedAt == "" {
			latestUpdatedAt = record.UpdatedAt
			if latestUpdatedAt == "" {
				latestUpdatedAt = record.RecordedAt
			}
		}
		latestTitle := snapshot.latestTitle
		if latestTitle == "" {
			latestTitle = record.Kind
		}
		tags := append([]string{}, descriptor.SemanticMemberships...)
		if len(tags) > 32 {
			tags = tags[:32]
		}
		createdAt := record.CreatedAt
		if createdAt == "" {
			createdAt = record.RecordedAt
		}
		node := memory.IndexNode{
			NodeID: "block_descriptor:" + writer.workspaceID + ":" + blockID,
			Title:  "Logical block: " + blockID, NodeType: "block_descriptor",
			WorkspaceID: writer.workspaceID, Tags: tags, CreatedAt: createdAt,
			UpdatedAt: latestUpdatedAt,
			Metadata: map[string]any{
				"block_id": blockID, "catalog_revision_id": catalog.RevisionID,
				"descriptor": descriptor, "related_block_ids": snapshot.relatedIDs,
				"source_revision_ids": snapshot.sourceRevisionIDs,
				"index_revision":      1, "index_revision_id": revisionID,
				"synopsis": fmt.Sprintf("%s Coverage: %d canonical revision(s); latest: %s.",
					descriptor.Guidance.Synopsis, snapshot.recordCount, latestTitle),
				"coverage": map[string]any{
					"canonical_revision_count": snapshot.recordCount,
					"canonical_record_count":   snapshot.recordCount,
					"semantic_memberships":     descriptor.SemanticMemberships,
					"exact_dimensions":         snapshot.exactDimensions,
					"record_ids_truncated":     snapshot.recordCount > len(snapshot.relatedIDs),
				},
				"latest_record_title": snapshot.latestTitle,
				"last_update_reason":  "rebuild",
			},
		}
		if err := insertBlockDescriptorTx(ctx, tx, node); err != nil {
			return "", err
		}
	}
	checksum, err = memoryIndexSHA256(ctx, tx, writer.workspaceID)
	if err != nil {
		return "", err
	}
	return checksum, tx.Commit()
}

type blockDescriptorSnapshot struct {
	recordCount       int
	relatedIDs        []string
	sourceRevisionIDs []string
	latestTitle       string
	latestUpdatedAt   string
	latestID          string
	exactDimensions   []string
}

func blockDescriptorSnapshotTx(ctx context.Context, tx *sql.Tx, workspaceID, profileID, blockID string) (blockDescriptorSnapshot, error) {
	result := blockDescriptorSnapshot{relatedIDs: []string{}, sourceRevisionIDs: []string{}, exactDimensions: []string{}}
	const criteria = `FROM record_block_memberships AS membership
		JOIN blocks AS record ON record.block_id=membership.record_id
		WHERE membership.workspace_id=? AND membership.logical_block_id=?
		AND membership.role='primary' AND membership.is_current=1
		AND record.status NOT IN ('superseded','deprecated','retracted','quarantined','expired')
		AND json_extract(record.payload_json,'$.profile_id')=?
		AND NOT EXISTS (SELECT 1 FROM canonical_head_outbox AS head
			WHERE head.successor_id=membership.record_id AND head.status<>'committed')`
	rows, err := tx.QueryContext(ctx, `SELECT membership.record_id,membership.record_revision_id,
		COALESCE(json_extract(record.payload_json,'$.title'),''),
		COALESCE(record.updated_at,'') `+criteria+
		` ORDER BY membership.record_id`, workspaceID, blockID, profileID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var id, revision, title, updated string
		if err := rows.Scan(&id, &revision, &title, &updated); err != nil {
			rows.Close()
			return result, err
		}
		result.recordCount++
		if len(result.relatedIDs) < 64 {
			result.relatedIDs = append(result.relatedIDs, id)
			result.sourceRevisionIDs = append(result.sourceRevisionIDs, revision)
		}
		if result.latestID == "" || updated > result.latestUpdatedAt ||
			(updated == result.latestUpdatedAt && id > result.latestID) {
			result.latestTitle, result.latestUpdatedAt, result.latestID = title, updated, id
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	sort.Strings(result.relatedIDs)
	sort.Strings(result.sourceRevisionIDs)
	result.sourceRevisionIDs = dedupSortedStrings(result.sourceRevisionIDs)
	termRows, err := tx.QueryContext(ctx, `SELECT DISTINCT term.term_type
		FROM record_block_memberships AS membership
		JOIN blocks AS record ON record.block_id=membership.record_id
		JOIN memory_exact_terms AS term ON term.block_id=membership.record_id
			AND term.workspace_id=membership.workspace_id
		WHERE membership.workspace_id=? AND membership.logical_block_id=?
		AND membership.role='primary' AND membership.is_current=1
		AND record.status NOT IN ('superseded','deprecated','retracted','quarantined','expired')
		AND json_extract(record.payload_json,'$.profile_id')=?
		AND NOT EXISTS (SELECT 1 FROM canonical_head_outbox AS head
			WHERE head.successor_id=membership.record_id AND head.status<>'committed')
		AND term.term_type IN ('path','symbol','entity','config_key','endpoint','artifact_hash')`,
		workspaceID, blockID, profileID)
	if err != nil {
		return result, err
	}
	dimension := map[string]string{"path": "paths", "symbol": "symbols", "entity": "entities",
		"config_key": "config_keys", "endpoint": "endpoints", "artifact_hash": "artifact_hashes"}
	for termRows.Next() {
		var kind string
		if err := termRows.Scan(&kind); err != nil {
			termRows.Close()
			return result, err
		}
		result.exactDimensions = append(result.exactDimensions, dimension[kind])
	}
	if err := termRows.Err(); err != nil {
		termRows.Close()
		return result, err
	}
	if err := termRows.Close(); err != nil {
		return result, err
	}
	sort.Strings(result.exactDimensions)
	return result, nil
}

func dedupSortedStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func insertBlockDescriptorTx(ctx context.Context, tx *sql.Tx, node memory.IndexNode) error {
	payload, err := jsoncompat.MarshalPythonDefault(node, false)
	if err != nil {
		return err
	}
	tags, err := jsoncompat.MarshalPythonDefault(node.Tags, false)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO index_nodes(
		node_id,workspace_id,node_type,title,tags_json,payload_json,updated_at)
		VALUES(?,?,?,?,?,?,?)`, node.NodeID, node.WorkspaceID, node.NodeType,
		node.Title, string(tags), string(payload), node.UpdatedAt)
	return err
}
