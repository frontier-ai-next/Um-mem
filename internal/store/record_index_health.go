package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type indexPair struct{ first, second string }

func readIndexPairs(ctx context.Context, tx *sql.Tx, query, workspaceID, recordID string) ([]indexPair, error) {
	rows, err := tx.QueryContext(ctx, query, workspaceID, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []indexPair{}
	for rows.Next() {
		var item indexPair
		if err := rows.Scan(&item.first, &item.second); err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, rows.Err()
}

func sortedPairs(values []indexPair) []indexPair {
	slices.SortFunc(values, func(a, b indexPair) int {
		if order := strings.Compare(a.first, b.first); order != 0 {
			return order
		}
		return strings.Compare(a.second, b.second)
	})
	return values
}

// RecordDerivedIndexesComplete checks the rows that the atomic canonical
// writer publishes for one committed record. It does not rewrite or return
// content. A missing row triggers the existing full rebuild recovery path.
func (index *Index) RecordDerivedIndexesComplete(ctx context.Context, recordID string) (bool, error) {
	if strings.TrimSpace(recordID) == "" {
		return false, errors.New("memory index refresh requires a record ID")
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	record, err := index.readStoredRecordTx(ctx, tx, recordID)
	if err != nil {
		return false, err
	}
	if record == nil {
		return false, errors.New("memory index refresh requires a visible canonical record")
	}
	document, err := memory.CompileIndexDocument(*record)
	if err != nil {
		return false, err
	}
	var revisionID, recordedAt, validFrom string
	err = tx.QueryRowContext(ctx, `SELECT revision_id,recorded_at,valid_from FROM memory_temporal_index
 WHERE workspace_id=? AND block_id=?`, index.workspaceID, recordID).Scan(
		&revisionID, &recordedAt, &validFrom)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if revisionID != record.RevisionID || recordedAt != record.RecordedAt || validFrom != record.ValidFrom {
		return false, nil
	}
	var title, content, tags string
	err = tx.QueryRowContext(ctx, `SELECT title,content,tags FROM memory_fts
 WHERE workspace_id=? AND block_id=?`, index.workspaceID, recordID).Scan(&title, &content, &tags)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if title != record.Title || content != record.Content || tags != strings.Join(record.Tags, " ") {
		return false, nil
	}
	exact, err := readIndexPairs(ctx, tx, `SELECT term_type,term_value FROM memory_exact_terms
 WHERE workspace_id=? AND block_id=?`, index.workspaceID, recordID)
	if err != nil {
		return false, err
	}
	wantExact := make([]indexPair, 0, len(document.ExactTerms))
	for _, term := range document.ExactTerms {
		wantExact = append(wantExact, indexPair{term.Type, term.Value})
	}
	if !slices.Equal(sortedPairs(exact), sortedPairs(wantExact)) {
		return false, nil
	}
	provenance, err := readIndexPairs(ctx, tx, `SELECT derived_id,raw_source_ref FROM memory_provenance_links
 WHERE workspace_id=? AND block_id=?`, index.workspaceID, recordID)
	if err != nil {
		return false, err
	}
	wantProvenance := make([]indexPair, 0, len(document.ProvenanceLinks))
	for _, link := range document.ProvenanceLinks {
		wantProvenance = append(wantProvenance, indexPair{link.DerivedID, link.RawSourceRef})
	}
	if !slices.Equal(sortedPairs(provenance), sortedPairs(wantProvenance)) {
		return false, nil
	}
	var membershipCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM record_block_memberships
 WHERE workspace_id=? AND record_id=?`, index.workspaceID, recordID).Scan(&membershipCount); err != nil {
		return false, err
	}
	wantMemberships := 0
	if record.BlockMembership != nil {
		wantMemberships = 1 + len(record.BlockMembership.SecondaryBlockIDs)
	}
	if membershipCount != wantMemberships {
		return false, nil
	}
	for _, node := range document.StructureNodes {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM index_nodes
 WHERE workspace_id=? AND node_id=?`, index.workspaceID, node.NodeID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}
