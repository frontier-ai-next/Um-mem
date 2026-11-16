package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

type EdgePage struct {
	Edges           []BlockGraphEdge `json:"edges"`
	Total           int              `json:"total"`
	NextCursor      string           `json:"next_cursor"`
	CatalogRevision int              `json:"catalog_revision"`
}

// PageEdges reads both directions of one block's graph in the same order and
// cursor format as the Python catalog. A cursor belongs to one catalog revision.
func (store *Store) PageEdges(ctx context.Context, blockID string, limit int, cursor string,
	expectedRevision *int) (EdgePage, error) {
	if blockID == "" || limit < 1 || limit > 200 {
		return EdgePage{}, errors.New("related block limit must be an integer from 1 to 200")
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EdgePage{}, err
	}
	defer tx.Rollback()
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT value FROM catalog_metadata WHERE key='catalog_revision'`).Scan(&revision); err != nil {
		return EdgePage{}, err
	}
	if expectedRevision != nil && *expectedRevision != revision {
		return EdgePage{}, errors.New("catalog changed while reading navigation")
	}
	revisionText := strconv.Itoa(revision)
	afterTime, afterID, err := readCursor(cursor, store.workspaceID, store.profileID,
		"graph:"+blockID, &revisionText)
	if err != nil {
		return EdgePage{}, err
	}
	page := EdgePage{Edges: []BlockGraphEdge{}, CatalogRevision: revision}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_edges
		WHERE workspace_id=? AND profile_id=? AND (src_id=? OR dst_id=?)`,
		store.workspaceID, store.profileID, blockID, blockID).Scan(&page.Total); err != nil {
		return EdgePage{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT edge_id,workspace_id,profile_id,src_id,dst_id,edge_type,
		reason,revision,created_at FROM topology_edges
		WHERE workspace_id=? AND profile_id=? AND (src_id=? OR dst_id=?)
		AND (created_at,edge_id)>(?,?) ORDER BY created_at,edge_id LIMIT ?`,
		store.workspaceID, store.profileID, blockID, blockID, afterTime, afterID, limit+1)
	if err != nil {
		return EdgePage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var edge BlockGraphEdge
		if err := rows.Scan(&edge.EdgeID, &edge.WorkspaceID, &edge.ProfileID, &edge.SrcID,
			&edge.DstID, &edge.EdgeType, &edge.Reason, &edge.Revision, &edge.CreatedAt); err != nil {
			return EdgePage{}, err
		}
		if edge.WorkspaceID != store.workspaceID || edge.ProfileID != store.profileID ||
			(edge.SrcID != blockID && edge.DstID != blockID) || edge.SrcID == edge.DstID ||
			edge.Revision < 1 || !blockEdgeTypes[edge.EdgeType] {
			return EdgePage{}, errors.New("stored catalog edge identity is invalid")
		}
		if len(page.Edges) == limit {
			last := page.Edges[len(page.Edges)-1]
			page.NextCursor, err = NavigationCursor(store.workspaceID, store.profileID,
				"graph:"+blockID, revisionText, last.CreatedAt, last.EdgeID)
			if err != nil {
				return EdgePage{}, err
			}
			break
		}
		page.Edges = append(page.Edges, edge)
	}
	if err := rows.Err(); err != nil {
		return EdgePage{}, err
	}
	if err := rows.Close(); err != nil {
		return EdgePage{}, err
	}
	if err := tx.Commit(); err != nil {
		return EdgePage{}, err
	}
	return page, nil
}
