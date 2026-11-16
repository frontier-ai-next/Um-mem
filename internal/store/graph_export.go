package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// ExportGraphSnapshot reads records, links and index nodes from one scoped
// SQLite snapshot. Pending writes are excluded by the canonical read barrier.
func (index *Index) ExportGraphSnapshot(ctx context.Context) ([]memory.MemoryRecord, []memory.EdgeRecord, []string, error) {
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT block_id FROM blocks WHERE workspace_id=? ORDER BY block_id`, index.workspaceID)
	if err != nil {
		return nil, nil, nil, err
	}
	ids := make([]string, 0)
	allRecordIDs := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		ids = append(ids, id)
		allRecordIDs[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, err
	}
	records := make([]memory.MemoryRecord, 0, len(ids))
	visibleRecords := make(map[string]bool)
	for _, id := range ids {
		record, err := index.readStoredRecordTx(ctx, tx, id)
		if err != nil {
			return nil, nil, nil, err
		}
		if record != nil {
			records = append(records, *record)
			visibleRecords[id] = true
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT node_id,payload_json FROM index_nodes
		WHERE workspace_id=? ORDER BY node_id`, index.workspaceID)
	if err != nil {
		return nil, nil, nil, err
	}
	nodes := make(map[string]bool)
	allNodes := make(map[string]bool)
	for rows.Next() {
		var id, payload string
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		var node memory.IndexNode
		if err := json.Unmarshal([]byte(payload), &node); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		if node.NodeID != id || node.WorkspaceID != index.workspaceID {
			rows.Close()
			return nil, nil, nil, errors.New("stored index node identity differs from row")
		}
		allNodes[id] = true
		var related []string
		if raw, exists := node.Metadata["related_record_ids"]; exists {
			related, err = nodeRecordIDs(raw)
			if err != nil {
				rows.Close()
				return nil, nil, nil, err
			}
		}
		for _, recordID := range related {
			if visibleRecords[recordID] {
				nodes[id] = true
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT src_id,dst_id,edge_type,payload_json FROM edges
		WHERE workspace_id=? ORDER BY src_id,dst_id,edge_type`, index.workspaceID)
	if err != nil {
		return nil, nil, nil, err
	}
	allEdges, err := decodeGraphRows(rows, index.workspaceID)
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}
	edges := make([]memory.EdgeRecord, 0, len(allEdges))
	for _, edge := range allEdges {
		selectedSource := visibleRecords[edge.SrcID] || nodes[edge.SrcID]
		selectedTarget := visibleRecords[edge.DstID] || nodes[edge.DstID]
		if !selectedSource && !selectedTarget {
			continue
		}
		// Do not disclose a record or node belonging only to another profile.
		foreign := false
		for _, id := range []string{edge.SrcID, edge.DstID} {
			if allNodes[id] && !nodes[id] {
				foreign = true
			}
			if allRecordIDs[id] && !visibleRecords[id] {
				foreign = true
			}
		}
		if !foreign {
			edges = append(edges, edge)
		}
	}
	endpointNodes := make([]string, 0)
	seen := make(map[string]bool)
	for _, edge := range edges {
		for _, id := range []string{edge.SrcID, edge.DstID} {
			if nodes[id] && !seen[id] {
				seen[id] = true
				endpointNodes = append(endpointNodes, id)
			}
		}
	}
	sort.Strings(endpointNodes)
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, err
	}
	return records, edges, endpointNodes, nil
}
