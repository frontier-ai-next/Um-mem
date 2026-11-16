package catalog

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

type BlockGraphEdge struct {
	EdgeID      string `json:"edge_id"`
	WorkspaceID string `json:"workspace_id"`
	ProfileID   string `json:"profile_id"`
	SrcID       string `json:"src_id"`
	DstID       string `json:"dst_id"`
	EdgeType    string `json:"edge_type"`
	Reason      string `json:"reason"`
	Revision    int    `json:"revision"`
	CreatedAt   string `json:"created_at"`
}

type Navigation struct {
	States          []BlockState                `json:"states"`
	PathIDs         []string                    `json:"path_ids"`
	ChildIDs        map[string][]string         `json:"child_ids"`
	Edges           map[string][]BlockGraphEdge `json:"edges"`
	CatalogRevision int                         `json:"catalog_revision"`
	ChildCounts     map[string]int              `json:"child_counts"`
	NextCursors     map[string]string           `json:"next_cursors"`
	EdgesTruncated  bool                        `json:"edges_truncated"`
}

type NavigateOptions struct {
	BlockID      string
	Depth        int
	IncludeEdges bool
	ChildLimit   int
	Cursor       string
}

func NavigationCursor(workspaceID, profileID, blockID, revision, createdAt, afterID string) (string, error) {
	encoded, err := jsoncompat.MarshalPython([]string{workspaceID, profileID, blockID, revision, createdAt, afterID}, true)
	if err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(encoded)
	if len(token) > 4096 {
		return "", errors.New("navigation cursor exceeds size limit")
	}
	return token, nil
}

func readCursor(cursor, workspaceID, profileID, blockID string, revision *string) (string, string, error) {
	if len(cursor) > 4096 {
		return "", "", errors.New("invalid navigation cursor")
	}
	if cursor == "" {
		return "", "", nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return "", "", errors.New("invalid navigation cursor")
	}
	var payload []string
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload) != 6 {
		return "", "", errors.New("invalid navigation cursor")
	}
	if payload[0] != workspaceID || payload[1] != profileID || payload[2] != blockID {
		return "", "", errors.New("navigation cursor scope does not match")
	}
	if revision != nil && payload[3] != *revision {
		return "", "", errors.New("navigation cursor revision changed; restart from the first page")
	}
	return payload[4], payload[5], nil
}

// ReadNavigationCursor validates a public catalog cursor against its block and
// revision. Static taxonomy pages use an integer offset in the first position.
func ReadNavigationCursor(cursor, workspaceID, profileID, blockID, revision string) (string, string, error) {
	return readCursor(cursor, workspaceID, profileID, blockID, &revision)
}

func (store *Store) getBlockTx(ctx context.Context, tx *sql.Tx, id string) (*BlockState, error) {
	state, err := scanBlock(tx.QueryRowContext(ctx, `SELECT `+blockColumns+` FROM topology_blocks
		WHERE workspace_id=? AND profile_id=? AND block_id=?`, store.workspaceID, store.profileID, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if state.WorkspaceID != store.workspaceID || state.ProfileID != store.profileID {
		return nil, errors.New("catalog block escaped selected scope")
	}
	return &state, nil
}

func (store *Store) childrenTx(ctx context.Context, tx *sql.Tx, id, afterTime, afterID string, limit int) ([][2]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT created_at,block_id FROM topology_blocks
		WHERE workspace_id=? AND profile_id=? AND parent_id=? AND status='active'
		AND (created_at,block_id)>(?,?) ORDER BY created_at,block_id LIMIT ?`,
		store.workspaceID, store.profileID, id, afterTime, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([][2]string, 0)
	for rows.Next() {
		var item [2]string
		if err := rows.Scan(&item[0], &item[1]); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) edgePreviewTx(ctx context.Context, tx *sql.Tx, visibleIDs []string, nav *Navigation) error {
	if len(visibleIDs) == 0 {
		return nil
	}
	unique := make([]string, 0, len(visibleIDs))
	for _, id := range visibleIDs {
		if !slices.Contains(unique, id) {
			unique = append(unique, id)
		}
	}
	slices.Sort(unique)
	if len(unique) < len(nav.States) {
		nav.EdgesTruncated = true
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, 0, len(unique)*2+3)
	args = append(args, store.workspaceID, store.profileID)
	for _, id := range unique {
		args = append(args, id)
	}
	for _, id := range unique {
		args = append(args, id)
	}
	args = append(args, 129)
	rows, err := tx.QueryContext(ctx, `SELECT edge_id,workspace_id,profile_id,src_id,dst_id,edge_type,
		reason,revision,created_at FROM topology_edges WHERE workspace_id=? AND profile_id=?
		AND src_id IN (`+placeholders+`) AND dst_id IN (`+placeholders+`) LIMIT ?`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var edge BlockGraphEdge
		if err := rows.Scan(&edge.EdgeID, &edge.WorkspaceID, &edge.ProfileID, &edge.SrcID,
			&edge.DstID, &edge.EdgeType, &edge.Reason, &edge.Revision, &edge.CreatedAt); err != nil {
			return err
		}
		count++
		if count > 128 {
			nav.EdgesTruncated = true
			continue
		}
		if edge.WorkspaceID != store.workspaceID || edge.ProfileID != store.profileID ||
			edge.SrcID == edge.DstID || edge.Revision < 1 || !blockEdgeTypes[edge.EdgeType] {
			return errors.New("stored catalog edge identity is invalid")
		}
		nav.Edges[edge.SrcID] = append(nav.Edges[edge.SrcID], edge)
		if edge.DstID != edge.SrcID {
			nav.Edges[edge.DstID] = append(nav.Edges[edge.DstID], edge)
		}
	}
	return rows.Err()
}

// Navigate reads one bounded catalog branch in a consistent SQLite snapshot.
func (store *Store) Navigate(ctx context.Context, options NavigateOptions) (Navigation, error) {
	depth := options.Depth
	if depth == 0 {
		depth = 1
	}
	childLimit := options.ChildLimit
	if childLimit == 0 {
		childLimit = 50
	}
	if depth < 1 || depth > 8 || childLimit < 1 || childLimit > 200 {
		return Navigation{}, errors.New("catalog navigation depth or child limit is outside allowed bounds")
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Navigation{}, err
	}
	defer tx.Rollback()
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT value FROM catalog_metadata WHERE key='catalog_revision'`).Scan(&revision); err != nil {
		return Navigation{}, err
	}
	nav := Navigation{States: []BlockState{}, PathIDs: []string{}, ChildIDs: map[string][]string{},
		Edges: map[string][]BlockGraphEdge{}, CatalogRevision: revision, ChildCounts: map[string]int{}, NextCursors: map[string]string{}}
	path := make([]string, 0)
	currentID := options.BlockID
	for currentID != "" {
		state, err := store.getBlockTx(ctx, tx, currentID)
		if err != nil {
			return Navigation{}, err
		}
		if state == nil || state.Status != "active" {
			if _, _, err := readCursor(options.Cursor, store.workspaceID, store.profileID, options.BlockID, nil); err != nil {
				return Navigation{}, err
			}
			return nav, tx.Commit()
		}
		if slices.Contains(path, currentID) {
			return Navigation{}, errors.New("memory block hierarchy contains a cycle")
		}
		path = append(path, currentID)
		currentID = state.Block.ParentID
	}
	for i := len(path) - 1; i >= 0; i-- {
		nav.PathIDs = append(nav.PathIDs, path[i])
	}
	revisionText := strconv.Itoa(revision)
	afterTime, afterID, err := readCursor(options.Cursor, store.workspaceID, store.profileID, options.BlockID, &revisionText)
	if err != nil {
		return Navigation{}, err
	}
	type pendingNode struct {
		id    string
		level int
	}
	pending := make([]pendingNode, 0)
	if options.BlockID != "" {
		pending = append(pending, pendingNode{options.BlockID, 1})
	} else {
		rows, err := store.childrenTx(ctx, tx, "", "", "", 201)
		if err != nil {
			return Navigation{}, err
		}
		for _, row := range rows {
			pending = append(pending, pendingNode{row[1], 1})
		}
	}
	for at := 0; at < len(pending); at++ {
		selected := pending[at]
		state, err := store.getBlockTx(ctx, tx, selected.id)
		if err != nil {
			return Navigation{}, err
		}
		if state == nil || state.Status != "active" {
			return Navigation{}, errors.New("navigation encountered an unavailable block")
		}
		nav.States = append(nav.States, *state)
		var childCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_blocks WHERE workspace_id=? AND profile_id=?
			AND parent_id=? AND status='active'`, store.workspaceID, store.profileID, selected.id).Scan(&childCount); err != nil {
			return Navigation{}, err
		}
		nav.ChildCounts[selected.id] = childCount
		pageLimit := 0
		if selected.level < depth || depth == 1 {
			pageLimit = childLimit
		}
		if selected.level < depth {
			pageLimit = min(pageLimit, max(0, 201-len(pending)))
		}
		pageTime, pageID := "", ""
		if selected.id == options.BlockID {
			pageTime, pageID = afterTime, afterID
		}
		rows, err := store.childrenTx(ctx, tx, selected.id, pageTime, pageID, pageLimit+1)
		if err != nil {
			return Navigation{}, err
		}
		shown := rows[:min(pageLimit, len(rows))]
		ids := make([]string, 0, len(shown))
		for _, row := range shown {
			ids = append(ids, row[1])
		}
		nav.ChildIDs[selected.id] = ids
		if len(rows) > pageLimit {
			if len(shown) > 0 {
				pageTime, pageID = shown[len(shown)-1][0], shown[len(shown)-1][1]
			}
			cursor, err := NavigationCursor(store.workspaceID, store.profileID, selected.id, revisionText, pageTime, pageID)
			if err != nil {
				return Navigation{}, err
			}
			nav.NextCursors[selected.id] = cursor
		} else {
			nav.NextCursors[selected.id] = ""
		}
		if selected.level < depth {
			for _, id := range ids {
				pending = append(pending, pendingNode{id, selected.level + 1})
			}
		}
	}
	if options.IncludeEdges {
		visible := append([]string{options.BlockID}, nav.ChildIDs[options.BlockID]...)
		if err := store.edgePreviewTx(ctx, tx, visible, &nav); err != nil {
			return Navigation{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Navigation{}, fmt.Errorf("finish catalog navigation snapshot: %w", err)
	}
	return nav, nil
}
