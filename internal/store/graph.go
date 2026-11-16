package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

var allowedEdgeTypes = map[string]bool{
	"contains": true, "derived_from": true, "cites": true, "tests": true,
	"implements": true, "supersedes": true, "supports": true, "verified_by": true,
	"verifies": true, "refutes": true, "revises": true, "contradicts": true,
	"observed_in": true, "produced_by": true, "produced_in": true, "related_to": true,
	"references_file": true, "from_subtask": true, "triggered_by_error": true,
	"flagged_by": true, "blocks": true, "applied_reflection": true, "mentions_symbol": true,
}

type EdgeWriteReceipt struct {
	SrcID    string
	DstID    string
	EdgeType string
	Replayed bool
}

func (writer *Writer) edgeEndpointTx(ctx context.Context, tx *sql.Tx, id string) (*memory.MemoryRecord, bool, error) {
	index := &Index{workspaceID: writer.workspaceID, profileID: writer.profileID}
	record, err := index.readStoredRecordTx(ctx, tx, id)
	if err != nil || record != nil {
		return record, false, err
	}
	var workspaceID string
	err = tx.QueryRowContext(ctx, `SELECT workspace_id FROM index_nodes WHERE node_id=?`, id).Scan(&workspaceID)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	return nil, err == nil && workspaceID == writer.workspaceID, err
}

func (writer *Writer) wouldCycleTx(ctx context.Context, tx *sql.Tx, src, dst string) (bool, error) {
	pending := []string{dst}
	visited := make(map[string]bool)
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == src {
			return true, nil
		}
		if visited[current] {
			continue
		}
		visited[current] = true
		rows, err := tx.QueryContext(ctx, `SELECT dst_id FROM edges WHERE workspace_id=? AND src_id=?
			AND edge_type IN ('derived_from','supersedes','revises')`, writer.workspaceID, current)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var target string
			if err := rows.Scan(&target); err != nil {
				rows.Close()
				return false, err
			}
			pending = append(pending, target)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, err
		}
		if err := rows.Close(); err != nil {
			return false, err
		}
	}
	return false, nil
}

func sameEdgeIgnoringCreatedAt(existing, proposed string) bool {
	var a, b map[string]any
	if json.Unmarshal([]byte(existing), &a) != nil || json.Unmarshal([]byte(proposed), &b) != nil {
		return false
	}
	delete(a, "created_at")
	delete(b, "created_at")
	return reflect.DeepEqual(a, b)
}

// AddEdge persists a scoped graph link after endpoint, replay and cycle checks.
func (writer *Writer) AddEdge(ctx context.Context, edge memory.EdgeRecord) (EdgeWriteReceipt, error) {
	edge.SrcID = strings.TrimSpace(edge.SrcID)
	edge.DstID = strings.TrimSpace(edge.DstID)
	if edge.SrcID == "" || edge.DstID == "" || edge.SrcID == edge.DstID {
		return EdgeWriteReceipt{}, errors.New("graph edge requires distinct nonempty endpoints")
	}
	if !allowedEdgeTypes[edge.EdgeType] {
		return EdgeWriteReceipt{}, fmt.Errorf("unknown graph edge type %q", edge.EdgeType)
	}
	if edge.WorkspaceID != "" && edge.WorkspaceID != writer.workspaceID {
		return EdgeWriteReceipt{}, errors.New("graph edge workspace differs from selected writer")
	}
	edge.WorkspaceID = writer.workspaceID
	if edge.Weight == "" {
		edge.Weight = json.Number("1.0")
	}
	if _, err := edge.Weight.Float64(); err != nil {
		return EdgeWriteReceipt{}, fmt.Errorf("invalid graph edge weight: %w", err)
	}
	if edge.EvidenceRefs == nil {
		edge.EvidenceRefs = []memory.EvidenceRef{}
	}
	for _, reference := range edge.EvidenceRefs {
		if err := reference.Validate(); err != nil {
			return EdgeWriteReceipt{}, err
		}
	}
	if edge.Metadata == nil {
		edge.Metadata = map[string]any{}
	}
	if edge.CreatedAt == "" {
		edge.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		return EdgeWriteReceipt{}, err
	}
	defer tx.Rollback()
	srcRecord, srcNode, err := writer.edgeEndpointTx(ctx, tx, edge.SrcID)
	if err != nil {
		return EdgeWriteReceipt{}, err
	}
	dstRecord, dstNode, err := writer.edgeEndpointTx(ctx, tx, edge.DstID)
	if err != nil {
		return EdgeWriteReceipt{}, err
	}
	if srcRecord == nil && !srcNode || dstRecord == nil && !dstNode {
		return EdgeWriteReceipt{}, errors.New("graph edge endpoints must exist in selected workspace and profile")
	}
	if edge.EdgeType == "supersedes" || edge.EdgeType == "revises" {
		if srcRecord == nil || dstRecord == nil || srcRecord.CanonicalKey != dstRecord.CanonicalKey ||
			srcRecord.BlockMembership == nil || dstRecord.BlockMembership == nil {
			return EdgeWriteReceipt{}, errors.New("revision edges require canonical records under the same key")
		}
	}
	if edge.EdgeType == "derived_from" || edge.EdgeType == "supersedes" || edge.EdgeType == "revises" {
		cycle, err := writer.wouldCycleTx(ctx, tx, edge.SrcID, edge.DstID)
		if err != nil {
			return EdgeWriteReceipt{}, err
		}
		if cycle {
			return EdgeWriteReceipt{}, errors.New("memory graph provenance cycle rejected")
		}
	}
	payload, err := jsoncompat.MarshalPython(edge, false)
	if err != nil {
		return EdgeWriteReceipt{}, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT payload_json FROM edges
		WHERE workspace_id=? AND src_id=? AND dst_id=? AND edge_type=?`,
		writer.workspaceID, edge.SrcID, edge.DstID, edge.EdgeType).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return EdgeWriteReceipt{}, err
	}
	receipt := EdgeWriteReceipt{SrcID: edge.SrcID, DstID: edge.DstID, EdgeType: edge.EdgeType}
	if err == nil {
		if !sameEdgeIgnoringCreatedAt(existing, string(payload)) {
			return EdgeWriteReceipt{}, errors.New("memory graph edges are immutable")
		}
		receipt.Replayed = true
		return receipt, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO edges(src_id,dst_id,edge_type,workspace_id,payload_json)
		VALUES(?,?,?,?,?)`, edge.SrcID, edge.DstID, edge.EdgeType, writer.workspaceID, string(payload)); err != nil {
		return EdgeWriteReceipt{}, err
	}
	return receipt, tx.Commit()
}
