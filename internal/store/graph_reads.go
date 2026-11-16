package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func (index *Index) graphEndpointVisibleTx(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	record, err := index.readStoredRecordTx(ctx, tx, id)
	if err != nil || record != nil {
		return record != nil, err
	}
	return index.dependencyNodeExistsTx(ctx, tx, id)
}

func decodeGraphRows(rows *sql.Rows, workspaceID string) ([]memory.EdgeRecord, error) {
	result := make([]memory.EdgeRecord, 0)
	for rows.Next() {
		var src, dst, kind, payload string
		if err := rows.Scan(&src, &dst, &kind, &payload); err != nil {
			return nil, err
		}
		var edge memory.EdgeRecord
		if err := json.Unmarshal([]byte(payload), &edge); err != nil {
			return nil, err
		}
		if edge.SrcID != src || edge.DstID != dst || edge.EdgeType != kind || edge.WorkspaceID != workspaceID {
			return nil, errors.New("stored graph edge identity differs from index")
		}
		result = append(result, edge)
	}
	return result, rows.Err()
}

// EdgesFor returns scoped links touching a visible record or index node.
func (index *Index) EdgesFor(ctx context.Context, id string) ([]memory.EdgeRecord, error) {
	if strings.TrimSpace(id) == "" {
		return []memory.EdgeRecord{}, nil
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	visible, err := index.graphEndpointVisibleTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !visible {
		return []memory.EdgeRecord{}, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT src_id,dst_id,edge_type,payload_json FROM edges
		WHERE workspace_id=? AND (src_id=? OR dst_id=?) ORDER BY rowid`, index.workspaceID, id, id)
	if err != nil {
		return nil, err
	}
	all, err := decodeGraphRows(rows, index.workspaceID)
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]memory.EdgeRecord, 0, len(all))
	for _, edge := range all {
		other := edge.SrcID
		if other == id {
			other = edge.DstID
		}
		visible, err := index.graphEndpointVisibleTx(ctx, tx, other)
		if err != nil {
			return nil, err
		}
		if visible {
			result = append(result, edge)
		}
	}
	return result, tx.Commit()
}

// EdgesBetween returns evidence-bearing edges among at most 64 visible IDs.
func (index *Index) EdgesBetween(ctx context.Context, recordIDs []string, limit int) ([]memory.EdgeRecord, error) {
	if limit <= 0 || len(recordIDs) == 0 {
		return []memory.EdgeRecord{}, nil
	}
	ids := make([]string, 0, len(recordIDs))
	seen := make(map[string]bool)
	for _, id := range recordIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > 64 {
		return nil, errors.New("edges_between supports at most 64 visible record IDs")
	}
	if len(ids) == 0 {
		return []memory.EdgeRecord{}, nil
	}
	slices.Sort(ids)
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	visible := make([]string, 0, len(ids))
	for _, id := range ids {
		allowed, err := index.graphEndpointVisibleTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if allowed {
			visible = append(visible, id)
		}
	}
	if len(visible) == 0 {
		return []memory.EdgeRecord{}, tx.Commit()
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(visible)), ",")
	args := make([]any, 0, 2*len(visible)+2)
	args = append(args, index.workspaceID)
	for _, id := range visible {
		args = append(args, id)
	}
	for _, id := range visible {
		args = append(args, id)
	}
	args = append(args, min(limit, 129))
	rows, err := tx.QueryContext(ctx, `SELECT src_id,dst_id,edge_type,payload_json FROM edges
		WHERE workspace_id=? AND src_id IN (`+placeholders+`) AND dst_id IN (`+placeholders+`)
		AND json_array_length(payload_json,'$.evidence_refs')>0
		ORDER BY src_id,dst_id,edge_type LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	result, err := decodeGraphRows(rows, index.workspaceID)
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}
