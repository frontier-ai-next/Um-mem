package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var blockEdgeTypes = map[string]bool{
	"related_to": true, "supports": true, "contradicts": true,
	"derived_from": true, "supersedes": true, "applies_to": true,
	"learned_from": true, "validated_by": true,
}

// ListEdges returns the bounded, content-free workspace graph in catalog order.
func (store *Store) ListEdges(ctx context.Context, limit int) ([]BlockGraphEdge, error) {
	if limit < 1 || limit > 129 {
		return nil, errors.New("catalog edge list limit must be 1-129")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT edge_id,workspace_id,profile_id,src_id,dst_id,edge_type,
		reason,revision,created_at FROM topology_edges
		WHERE workspace_id=? AND profile_id=? ORDER BY created_at,edge_id LIMIT ?`,
		store.workspaceID, store.profileID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edges := make([]BlockGraphEdge, 0)
	for rows.Next() {
		var edge BlockGraphEdge
		if err := rows.Scan(&edge.EdgeID, &edge.WorkspaceID, &edge.ProfileID,
			&edge.SrcID, &edge.DstID, &edge.EdgeType, &edge.Reason,
			&edge.Revision, &edge.CreatedAt); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

func (store *Store) HasIncomingBlockEdge(ctx context.Context, blockID string) (bool, error) {
	var found int
	err := store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM topology_edges
		WHERE workspace_id=? AND profile_id=? AND dst_id=?)`,
		store.workspaceID, store.profileID, blockID).Scan(&found)
	return found == 1, err
}

func (store *Store) blockExistsConn(ctx context.Context, conn *sql.Conn, id string) (bool, error) {
	var found int
	err := conn.QueryRowContext(ctx, `SELECT 1 FROM topology_blocks WHERE workspace_id=? AND profile_id=? AND block_id=?`,
		store.workspaceID, store.profileID, id).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (store *Store) AddBlockEdge(ctx context.Context, srcID, dstID, edgeType, reason, actor string) (BlockGraphEdge, error) {
	srcID, dstID = strings.TrimSpace(srcID), strings.TrimSpace(dstID)
	if srcID == "" || dstID == "" || srcID == dstID {
		return BlockGraphEdge{}, errors.New("block edge requires distinct nonempty endpoints")
	}
	if !blockEdgeTypes[edgeType] {
		return BlockGraphEdge{}, fmt.Errorf("unsupported block edge type %q", edgeType)
	}
	if strings.TrimSpace(reason) == "" || strings.TrimSpace(actor) == "" {
		return BlockGraphEdge{}, errors.New("block edge requires reason and actor")
	}
	id, err := uuid4()
	if err != nil {
		return BlockGraphEdge{}, err
	}
	edge := BlockGraphEdge{EdgeID: id, WorkspaceID: store.workspaceID, ProfileID: store.profileID,
		SrcID: srcID, DstID: dstID, EdgeType: edgeType, Reason: strings.TrimSpace(reason),
		Revision: 1, CreatedAt: now()}
	err = immediate(ctx, store.db, func(conn *sql.Conn) error {
		for _, endpoint := range []string{srcID, dstID} {
			found, err := store.blockExistsConn(ctx, conn, endpoint)
			if err != nil {
				return err
			}
			if !found {
				return errors.New("block edge endpoints must exist in the same workspace and profile")
			}
		}
		previous, err := store.revisionConn(ctx, conn)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO topology_edges(
			edge_id,workspace_id,profile_id,src_id,dst_id,edge_type,reason,revision,created_at
		) VALUES(?,?,?,?,?,?,?,?,?)`, edge.EdgeID, edge.WorkspaceID, edge.ProfileID,
			edge.SrcID, edge.DstID, edge.EdgeType, edge.Reason, edge.Revision, edge.CreatedAt); err != nil {
			return fmt.Errorf("block edge already exists or could not be saved: %w", err)
		}
		return store.mutationTx(ctx, conn, previous, "add_block_edge", actor, reason,
			[]string{srcID, dstID, id}, map[string]string{"src_id": srcID, "dst_id": dstID}, edge)
	})
	if err != nil {
		return BlockGraphEdge{}, err
	}
	return edge, nil
}

func (store *Store) RemoveBlockEdge(ctx context.Context, edgeID string, expectedRevision int, reason, actor string) (BlockGraphEdge, error) {
	if strings.TrimSpace(edgeID) == "" || expectedRevision < 1 {
		return BlockGraphEdge{}, errors.New("block edge ID and expected revision are required")
	}
	var removed BlockGraphEdge
	err := immediate(ctx, store.db, func(conn *sql.Conn) error {
		row := conn.QueryRowContext(ctx, `SELECT edge_id,workspace_id,profile_id,src_id,dst_id,edge_type,
			reason,revision,created_at FROM topology_edges WHERE workspace_id=? AND profile_id=? AND edge_id=?`,
			store.workspaceID, store.profileID, edgeID)
		if err := row.Scan(&removed.EdgeID, &removed.WorkspaceID, &removed.ProfileID, &removed.SrcID,
			&removed.DstID, &removed.EdgeType, &removed.Reason, &removed.Revision, &removed.CreatedAt); err != nil {
			if err == sql.ErrNoRows {
				return errors.New("block edge was not found")
			}
			return err
		}
		if removed.Revision != expectedRevision {
			return errors.New("expected_revision does not match current edge revision")
		}
		previous, err := store.revisionConn(ctx, conn)
		if err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `DELETE FROM topology_edges WHERE edge_id=? AND workspace_id=? AND profile_id=? AND revision=?`,
			edgeID, store.workspaceID, store.profileID, expectedRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("expected_revision does not match current edge revision")
		}
		return store.mutationTx(ctx, conn, previous, "remove_block_edge", actor, reason,
			[]string{removed.SrcID, removed.DstID, removed.EdgeID}, removed, map[string]bool{"removed": true})
	})
	if err != nil {
		return BlockGraphEdge{}, err
	}
	return removed, nil
}
