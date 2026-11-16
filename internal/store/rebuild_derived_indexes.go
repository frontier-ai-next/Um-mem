package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"github.com/gofrs/flock"
)

type DerivedIndexRebuild struct {
	WorkspaceID string `json:"workspace_id"`
	RecordCount int    `json:"record_count"`
	IndexSHA256 string `json:"index_sha256"`
}

// RebuildDerivedRecordIndexes reconstructs only canonical-record-derived
// tables. Logical block descriptors need a catalog-aware pass before this can
// be exposed as the public rebuild_indexes operation.
func (writer *Writer) RebuildDerivedRecordIndexes(ctx context.Context) (result DerivedIndexRebuild, rebuildErr error) {
	if writer == nil || writer.db == nil || writer.path == "" {
		return result, errors.New("canonical index writer is closed")
	}
	lock := flock.New(writer.path + ".canonical.lock")
	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return result, err
	}
	if !locked {
		return result, errors.New("canonical index rebuild gate timed out")
	}
	defer func() { rebuildErr = errors.Join(rebuildErr, lock.Unlock()) }()
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	for _, table := range []string{"memory_exact_terms", "memory_temporal_index",
		"memory_provenance_links", "record_block_memberships", "memory_fts"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE workspace_id=?", writer.workspaceID); err != nil {
			return result, fmt.Errorf("clear %s: %w", table, err)
		}
	}
	// Preserve block descriptors until the catalog-aware pass can recreate them.
	if _, err := tx.ExecContext(ctx, `DELETE FROM index_nodes WHERE workspace_id=?
		AND node_type IN ('project_structure','exact_facet')`, writer.workspaceID); err != nil {
		return result, err
	}
	heads, err := rebuildCanonicalHeads(ctx, tx, writer.workspaceID)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM blocks
		WHERE workspace_id=? ORDER BY block_id`, writer.workspaceID)
	if err != nil {
		return result, err
	}
	records := make([]memory.MemoryRecord, 0)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			rows.Close()
			return result, err
		}
		record, err := memory.DecodeCanonicalRecord([]byte(payload))
		if err != nil {
			rows.Close()
			return result, fmt.Errorf("decode canonical record for index rebuild: %w", err)
		}
		if record.WorkspaceID != writer.workspaceID {
			rows.Close()
			return result, errors.New("canonical record escaped selected workspace")
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	boundaryReader := &Index{workspaceID: writer.workspaceID}
	for _, record := range records {
		keyHash := ""
		if record.CanonicalKey != "" {
			keyHash = sha256Hex(record.CanonicalKey)
		}
		current := !terminalIndexStatus(record.Status)
		if headID, exists := heads[keyHash]; exists && keyHash != "" && headID != record.BlockID {
			current = false
		}
		_, boundary, hasBoundary, err := boundaryReader.successorBoundaryTx(ctx, tx, record)
		if err != nil {
			return result, err
		}
		projected := record
		if hasBoundary && record.ValidFrom != "" {
			boundaryTime, _, err := parseTemporalTime(boundary)
			if err != nil {
				return result, err
			}
			explicitTime, hasExplicit, err := parseTemporalTime(record.ValidTo)
			if err != nil {
				return result, err
			}
			if !hasExplicit || boundaryTime.Before(explicitTime) {
				projected.ValidTo = boundary
			}
		}
		projectedValidTo := projected.ValidTo
		if projectedValidTo == "" && !current && hasBoundary {
			projectedValidTo = boundary
		}
		if err := rebuildRecordIndexesTx(ctx, tx, projected, keyHash,
			projectedValidTo, current); err != nil {
			return result, err
		}
	}
	checksum, err := memoryIndexSHA256(ctx, tx, writer.workspaceID)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return DerivedIndexRebuild{WorkspaceID: writer.workspaceID,
		RecordCount: len(records), IndexSHA256: checksum}, nil
}

func rebuildCanonicalHeads(ctx context.Context, tx *sql.Tx, workspaceID string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT canonical_key_sha256,block_id FROM canonical_heads
		WHERE workspace_id=?`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	heads := make(map[string]string)
	for rows.Next() {
		var keyHash, id string
		if err := rows.Scan(&keyHash, &id); err != nil {
			return nil, err
		}
		heads[keyHash] = id
	}
	return heads, rows.Err()
}

func terminalIndexStatus(status string) bool {
	switch status {
	case "quarantined", "superseded", "deprecated", "retracted", "expired":
		return true
	default:
		return false
	}
}

func rebuildRecordIndexesTx(ctx context.Context, tx *sql.Tx, record memory.MemoryRecord,
	keyHash, projectedValidTo string, current bool) error {
	isCurrent := 0
	if current {
		isCurrent = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_temporal_index(
		workspace_id,block_id,canonical_key_sha256,revision_id,valid_from,valid_to,
		recorded_at,is_current) VALUES(?,?,?,?,?,?,?,?)`, record.WorkspaceID,
		record.BlockID, keyHash, record.RevisionID, record.ValidFrom,
		projectedValidTo, record.RecordedAt, isCurrent); err != nil {
		return err
	}
	if membership := record.BlockMembership; membership != nil {
		if membership.RecordID != record.BlockID ||
			membership.RecordRevisionID != record.RevisionID ||
			membership.WorkspaceID != record.WorkspaceID ||
			membership.MembershipRevisionID == "" {
			return errors.New("canonical record block membership identity is invalid")
		}
		blocks := append([]string{membership.PrimaryBlockID}, membership.SecondaryBlockIDs...)
		for ordinal, blockID := range blocks {
			role := "secondary"
			if ordinal == 0 {
				role = "primary"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO record_block_memberships(
				workspace_id,record_id,record_revision_id,membership_revision_id,logical_block_id,
				role,ordinal,catalog_revision_id,backend_partition_id,valid_from,valid_to,is_current
			) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, record.WorkspaceID, record.BlockID,
				record.RevisionID, membership.MembershipRevisionID, blockID, role, ordinal,
				membership.CatalogRevisionID, membership.BackendPartitionID,
				membership.ValidFrom, projectedValidTo, isCurrent); err != nil {
				return err
			}
		}
	}
	document, err := memory.CompileIndexDocument(record)
	if err != nil {
		return err
	}
	for _, term := range document.ExactTerms {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_exact_terms(
			workspace_id,term_type,term_value,block_id) VALUES(?,?,?,?)`,
			record.WorkspaceID, term.Type, term.Value, record.BlockID); err != nil {
			return err
		}
	}
	for _, link := range document.ProvenanceLinks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_provenance_links(
			workspace_id,derived_id,raw_source_ref,block_id) VALUES(?,?,?,?)`,
			record.WorkspaceID, link.DerivedID, link.RawSourceRef, record.BlockID); err != nil {
			return err
		}
	}
	for _, node := range document.StructureNodes {
		if err := upsertIndexNodeTx(ctx, tx, node); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_fts(
		block_id,workspace_id,title,content,tags) VALUES(?,?,?,?,?)`, record.BlockID,
		record.WorkspaceID, record.Title, record.Content, strings.Join(record.Tags, " ")); err != nil {
		return err
	}
	return nil
}
