package engine

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/blockcatalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// ExpandBlock reads a single catalog branch with revision-pinned child and
// graph pages. It does not allow a caller to escape the engine's profile.
func (engine *Engine) ExpandBlock(ctx context.Context, blockID string, limit int, cursor string) (map[string]any, error) {
	blockID = strings.TrimSpace(blockID)
	if blockID == "" {
		return nil, errors.New("block_id must identify a visible catalog block")
	}
	if limit < 1 || limit > 200 {
		return nil, errors.New("catalog navigation child limit is outside allowed bounds")
	}
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	nav, err := opened.Navigate(ctx, catalog.NavigateOptions{BlockID: blockID, Depth: 2,
		ChildLimit: limit, Cursor: cursor, IncludeEdges: true})
	if err != nil {
		return nil, err
	}
	isMeta := len(nav.States) == 0
	rootEdges := catalog.EdgePage{Edges: []catalog.BlockGraphEdge{}}
	childEdges := map[string]catalog.EdgePage{}
	if isMeta {
		nav, err = engine.staticExpansionNavigation(ctx, nav, blockID, limit, cursor)
		if err != nil {
			return nil, err
		}
	} else {
		rootEdges, err = opened.PageEdges(ctx, blockID, 128, "", &nav.CatalogRevision)
		if err != nil {
			return nil, err
		}
		for _, id := range nav.ChildIDs[blockID][:min(16, len(nav.ChildIDs[blockID]))] {
			page, pageErr := opened.PageEdges(ctx, id, 8, "", &nav.CatalogRevision)
			if pageErr != nil {
				return nil, pageErr
			}
			childEdges[id] = page
		}
	}
	views, err := engine.blockViews(ctx, nav, isMeta, 2, limit, blockID, nil)
	if err != nil {
		return nil, err
	}
	if len(views) != 1 {
		return nil, errors.New("block_id must identify a visible catalog block")
	}
	root := views[0]
	children, _ := root["children"].([]map[string]any)
	if children == nil {
		children = []map[string]any{}
	}
	delete(root, "children")
	visible := append([]map[string]any{root}, children...)
	selectedIDs := make([]string, 0, len(visible))
	for _, view := range visible {
		selectedIDs = append(selectedIDs, view["block_id"].(string))
	}
	health, err := engine.BlockHealth(ctx, selectedIDs)
	if err != nil {
		return nil, err
	}
	partitions, _ := health["native_partitions"].([]map[string]any)
	metrics, _ := health["block_metrics"].([]map[string]any)
	for _, view := range visible {
		id := view["block_id"].(string)
		ownPartitions := make([]map[string]any, 0)
		ownMetrics := make([]map[string]any, 0)
		for _, row := range partitions {
			if row["primary_block_id"] == id {
				ownPartitions = append(ownPartitions, row)
			}
		}
		for _, row := range metrics {
			if row["primary_block_id"] == id {
				ownMetrics = append(ownMetrics, row)
			}
		}
		view["native_partitions"] = ownPartitions
		view["block_metrics"] = ownMetrics
		edges := append([]catalog.BlockGraphEdge{}, nav.Edges[id]...)
		edges = append(edges, rootEdges.Edges...)
		edgeCursor := rootEdges.NextCursor
		scope := "root_edge_page_and_page_children"
		complete := edgeCursor == "" && !nav.EdgesTruncated
		if id != blockID {
			if page, found := childEdges[id]; found {
				edges = append(append([]catalog.BlockGraphEdge{}, nav.Edges[id]...), page.Edges...)
				edgeCursor = page.NextCursor
				scope = "child_edge_page"
				complete = edgeCursor == ""
			} else {
				edges = append([]catalog.BlockGraphEdge{}, nav.Edges[id]...)
				edgeCursor = ""
				scope = "visible_subgraph_only"
				complete = false
			}
		}
		related := make([]string, 0)
		seen := map[string]bool{}
		for _, edge := range edges {
			other := edge.SrcID
			if other == id {
				other = edge.DstID
			}
			if !seen[other] {
				related = append(related, other)
				seen[other] = true
			}
		}
		view["links"].(map[string]any)["related_block_ids"] = related
		view["related_block_ids_scope"] = scope
		view["related_block_ids_complete"] = complete
		view["related_edges_truncated"] = !complete
		view["diagnostics_status"] = health["readiness"]
		view["metrics_scope"] = health["metrics_scope"]
	}
	return map[string]any{
		"path_ids": nav.PathIDs, "catalog_revision": nav.CatalogRevision,
		"child_count": nav.ChildCounts[blockID], "next_cursor": nav.NextCursors[blockID],
		"root": root, "children": children,
		"related_edges_scope":     "root_edge_page_and_page_children",
		"related_edges_truncated": rootEdges.NextCursor != "" || nav.EdgesTruncated,
		"related_edges_limit":     128, "root_related_next_cursor": rootEdges.NextCursor,
	}, nil
}

func (engine *Engine) staticExpansionNavigation(ctx context.Context, nav catalog.Navigation,
	blockID string, limit int, cursor string) (catalog.Navigation, error) {
	blocks, err := blockcatalog.Load(blockcatalog.Options{RepoRoot: engine.config.RepoRoot,
		WorkspaceID: engine.config.WorkspaceID, ProfileID: engine.config.ProfileID, Strict: true})
	if err != nil {
		return catalog.Navigation{}, err
	}
	semantic, err := memory.CompileSemanticCatalog(blocks)
	if err != nil {
		return catalog.Navigation{}, err
	}
	byID := map[string]memory.Block{}
	children := map[string][]string{}
	for _, block := range blocks {
		byID[block.BlockID] = block
		children[block.BlockID] = append([]string{}, block.ChildIDs...)
	}
	selected, found := byID[blockID]
	if !found {
		return catalog.Navigation{}, errors.New("block_id must identify a visible catalog block")
	}
	path := []string{selected.BlockID}
	for selected.ParentID != "" {
		parent, exists := byID[selected.ParentID]
		if !exists || len(path) > len(blocks) {
			return catalog.Navigation{}, errors.New("static catalog hierarchy is invalid")
		}
		path = append([]string{parent.BlockID}, path...)
		selected = parent
	}
	if nav.PathIDs == nil {
		nav.PathIDs = []string{}
	}
	nav.PathIDs = path
	offsetText, afterID, err := catalog.ReadNavigationCursor(cursor, engine.config.WorkspaceID,
		engine.config.ProfileID, blockID, semantic.RevisionID)
	if err != nil {
		return catalog.Navigation{}, err
	}
	if afterID != "" {
		return catalog.Navigation{}, errors.New("invalid navigation cursor")
	}
	offset := 0
	if offsetText != "" {
		offset, err = strconv.Atoi(offsetText)
		if err != nil || offset < 0 {
			return catalog.Navigation{}, errors.New("invalid navigation cursor")
		}
	}
	all := children[blockID]
	if offset > len(all) {
		return catalog.Navigation{}, errors.New("invalid navigation cursor offset")
	}
	end := min(offset+limit, len(all))
	nav.ChildIDs = map[string][]string{blockID: append([]string{}, all[offset:end]...)}
	nav.ChildCounts = map[string]int{blockID: len(all)}
	nav.NextCursors = map[string]string{blockID: ""}
	if end < len(all) {
		nav.NextCursors[blockID], err = catalog.NavigationCursor(engine.config.WorkspaceID,
			engine.config.ProfileID, blockID, semantic.RevisionID, strconv.Itoa(end), "")
		if err != nil {
			return catalog.Navigation{}, err
		}
	}
	for _, id := range nav.ChildIDs[blockID] {
		nav.ChildIDs[id] = []string{}
		nav.ChildCounts[id] = len(children[id])
		nav.NextCursors[id] = ""
		if len(children[id]) > 0 {
			nav.NextCursors[id], err = catalog.NavigationCursor(engine.config.WorkspaceID,
				engine.config.ProfileID, id, semantic.RevisionID, "0", "")
			if err != nil {
				return catalog.Navigation{}, err
			}
		}
	}
	return nav, ctx.Err()
}
