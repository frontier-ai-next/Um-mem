package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func nodeRecordIDs(raw any) ([]string, error) {
	values, ok := raw.([]any)
	if !ok {
		if typed, good := raw.([]string); good {
			return typed, nil
		}
		return nil, errors.New("index node has invalid related_record_ids")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		id, ok := value.(string)
		if !ok || id == "" {
			return nil, errors.New("index node has invalid related record ID")
		}
		result = append(result, id)
	}
	return result, nil
}

func upsertIndexNodeTx(ctx context.Context, tx *sql.Tx, node memory.IndexNode) error {
	var payload string
	err := tx.QueryRowContext(ctx, `SELECT payload_json FROM index_nodes WHERE node_id=?`, node.NodeID).Scan(&payload)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		var existing memory.IndexNode
		if err := json.Unmarshal([]byte(payload), &existing); err != nil {
			return fmt.Errorf("decode existing index node: %w", err)
		}
		if existing.NodeID != node.NodeID || existing.WorkspaceID != node.WorkspaceID || existing.NodeType != node.NodeType {
			return errors.New("index node identity conflict")
		}
		ids, err := nodeRecordIDs(existing.Metadata["related_record_ids"])
		if err != nil {
			return err
		}
		newIDs, err := nodeRecordIDs(node.Metadata["related_record_ids"])
		if err != nil {
			return err
		}
		seen := make(map[string]bool, len(ids)+len(newIDs))
		merged := make([]string, 0, len(ids)+len(newIDs))
		for _, id := range append(ids, newIDs...) {
			if !seen[id] {
				seen[id] = true
				merged = append(merged, id)
			}
		}
		node.CreatedAt = existing.CreatedAt
		node.Metadata["related_record_ids"] = merged
	}
	encoded, err := jsoncompat.MarshalPythonDefault(node, false)
	if err != nil {
		return err
	}
	tags, err := jsoncompat.MarshalPythonDefault(node.Tags, false)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO index_nodes(
		node_id,workspace_id,node_type,title,tags_json,payload_json,updated_at
	) VALUES(?,?,?,?,?,?,?)`, node.NodeID, node.WorkspaceID, node.NodeType, node.Title,
		string(tags), string(encoded), node.UpdatedAt)
	return err
}

// ExactIndexTerms returns indexed values for a visible record in this scope.
func (index *Index) ExactIndexTerms(ctx context.Context, recordID string) (map[string][]string, error) {
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	record, err := index.readStoredRecordTx(ctx, tx, recordID)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]string)
	if record == nil {
		return result, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT term_type,term_value FROM memory_exact_terms
		WHERE workspace_id=? AND block_id=? ORDER BY term_type,term_value`, index.workspaceID, recordID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			rows.Close()
			return nil, err
		}
		result[kind] = append(result[kind], value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

// ProjectStructureNodes returns the project hierarchy containing a visible record.
func (index *Index) ProjectStructureNodes(ctx context.Context, recordID string) ([]memory.IndexNode, error) {
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	record, err := index.readStoredRecordTx(ctx, tx, recordID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return []memory.IndexNode{}, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM index_nodes
		WHERE workspace_id=? AND node_type='project_structure' ORDER BY node_id`, index.workspaceID)
	if err != nil {
		return nil, err
	}
	result := make([]memory.IndexNode, 0)
	order := map[string]int{"repository": 0, "package": 1, "component": 2, "file": 3, "symbol": 4}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			rows.Close()
			return nil, err
		}
		var node memory.IndexNode
		if err := json.Unmarshal([]byte(payload), &node); err != nil {
			rows.Close()
			return nil, err
		}
		if node.WorkspaceID != index.workspaceID || node.NodeType != "project_structure" {
			rows.Close()
			return nil, errors.New("stored project structure node identity differs from index")
		}
		kind, ok := node.Metadata["structure_kind"].(string)
		if !ok {
			rows.Close()
			return nil, errors.New("stored project structure node has invalid kind")
		}
		if _, known := order[kind]; !known {
			rows.Close()
			return nil, errors.New("stored project structure node has unknown kind")
		}
		ids, err := nodeRecordIDs(node.Metadata["related_record_ids"])
		if err != nil {
			rows.Close()
			return nil, err
		}
		if slices.Contains(ids, recordID) {
			result = append(result, node)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	visibleIDs := map[string]bool{recordID: true}
	for i := range result {
		ids, _ := nodeRecordIDs(result[i].Metadata["related_record_ids"])
		filtered := make([]string, 0, len(ids))
		for _, id := range ids {
			visible, known := visibleIDs[id]
			if !known {
				member, err := index.readStoredRecordTx(ctx, tx, id)
				if err != nil {
					return nil, err
				}
				visible = member != nil
				visibleIDs[id] = visible
			}
			if visible {
				filtered = append(filtered, id)
			}
		}
		result[i].Metadata["related_record_ids"] = filtered
	}
	slices.SortFunc(result, func(a, b memory.IndexNode) int {
		return order[a.Metadata["structure_kind"].(string)] - order[b.Metadata["structure_kind"].(string)]
	})
	return result, tx.Commit()
}

// ReverseProvenanceSources returns raw references linked to a derived claim in
// records visible to the selected workspace and profile.
func (index *Index) ReverseProvenanceSources(ctx context.Context, derivedID string) ([]string, error) {
	if derivedID == "" {
		return []string{}, nil
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT raw_source_ref,block_id FROM memory_provenance_links
		WHERE workspace_id=? AND derived_id=? ORDER BY raw_source_ref,block_id`, index.workspaceID, derivedID)
	if err != nil {
		return nil, err
	}
	type link struct{ rawRef, recordID string }
	links := make([]link, 0)
	for rows.Next() {
		var item link
		if err := rows.Scan(&item.rawRef, &item.recordID); err != nil {
			rows.Close()
			return nil, err
		}
		links = append(links, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	result := make([]string, 0)
	for _, item := range links {
		if seen[item.rawRef] {
			continue
		}
		record, err := index.readStoredRecordTx(ctx, tx, item.recordID)
		if err != nil {
			return nil, err
		}
		if record != nil {
			result = append(result, item.rawRef)
			seen[item.rawRef] = true
		}
	}
	return result, tx.Commit()
}
