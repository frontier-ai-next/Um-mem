package engine

import (
	"context"
	"errors"
	"math"
	"sort"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// PanelCatalog returns the full logical taxonomy and a bounded set of
// content-free topology edges. The tree merges the packaged catalog blocks
// (content-free meta nodes the engine routes writes to, such as
// project_scope_stable_project_facts) with the dynamic blocks created in this
// workspace. The browser builds its own tree from parent IDs and must treat
// meta nodes as read-only.
func (engine *Engine) PanelCatalog(ctx context.Context, scope, scopeID string) (map[string]any, error) {
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	states, err := opened.ListBlocks(ctx, false)
	if err != nil {
		return nil, err
	}
	edges, err := opened.ListEdges(ctx, 129)
	if err != nil {
		return nil, err
	}
	revision, err := opened.CatalogRevision(ctx)
	if err != nil {
		return nil, err
	}
	truncated := len(edges) > 128
	if truncated {
		edges = edges[:128]
	}
	navigation := catalog.Navigation{States: states, ChildIDs: map[string][]string{},
		ChildCounts: map[string]int{}, NextCursors: map[string]string{}}
	for _, state := range states {
		id := state.Block.BlockID
		navigation.ChildIDs[id] = []string{}
	}
	for _, state := range states {
		parent := state.Block.ParentID
		if parent != "" {
			if _, exists := navigation.ChildIDs[parent]; !exists {
				return nil, errors.New("catalog block is not reachable from a visible root")
			}
			navigation.ChildIDs[parent] = append(navigation.ChildIDs[parent], state.Block.BlockID)
		}
	}
	for id, children := range navigation.ChildIDs {
		sort.Strings(children)
		navigation.ChildCounts[id] = len(children)
	}
	filter := &blockViewFilter{scopeKey: scope + ":" + scopeID, projectIDs: map[string]struct{}{}}
	tree, err := engine.blockViews(ctx, navigation, true, 8, 200, "", filter)
	if err != nil {
		return nil, err
	}
	flat := make([]map[string]any, 0, len(states)+32)
	var collect func([]map[string]any)
	collect = func(nodes []map[string]any) {
		for _, node := range nodes {
			children, _ := node["children"].([]map[string]any)
			delete(node, "children")
			flat = append(flat, node)
			collect(children)
		}
	}
	collect(tree)
	seen := make(map[string]bool, len(flat))
	for _, node := range flat {
		id, _ := node["block_id"].(string)
		if id == "" || seen[id] {
			return nil, errors.New("catalog snapshot contains duplicate blocks")
		}
		seen[id] = true
	}
	for _, state := range states {
		if !seen[state.Block.BlockID] {
			return nil, errors.New("catalog snapshot exceeds visible tree depth")
		}
	}
	// Packaged catalog blocks are routing nodes, not the blocks records live in:
	// without this pass the panel shows a tree whose every node is empty. Attach
	// the logical blocks that actually hold records, under the first visible root.
	liveCounts, liveLabeled := map[string]int{}, map[string]int{}
	if records, _, _, snapErr := engine.index.ExportGraphSnapshot(ctx); snapErr == nil {
		for _, record := range records {
			if record.Status == "retracted" || record.Status == "superseded" {
				continue
			}
			block := ""
			if record.BlockMembership != nil {
				block = record.BlockMembership.PrimaryBlockID
			}
			if block == "" {
				continue
			}
			liveCounts[block]++
			if len(record.Metadata) > 0 {
				liveLabeled[block]++
			}
		}
	}
	rootID := ""
	for _, state := range states {
		if state.Block.ParentID == "" {
			rootID = state.Block.BlockID
			break
		}
	}
	logicalIDs := make([]string, 0, len(liveCounts))
	for block := range liveCounts {
		logicalIDs = append(logicalIDs, block)
	}
	sort.Strings(logicalIDs)
	for _, block := range logicalIDs {
		if seen[block] || liveCounts[block] == 0 {
			continue
		}
		node := map[string]any{"block_id": block, "name": block, "parent_id": rootID,
			"status": "active", "index_only": false, "children": []map[string]any{},
			"canonical_record_count": liveCounts[block], "record_count": liveCounts[block],
			"logical": true}
		if total := liveCounts[block]; total > 0 {
			node["label_share"] = float64(liveLabeled[block]) / float64(total)
		}
		flat = append(flat, node)
	}
	projectIDs := make([]string, 0, len(filter.projectIDs))
	for id := range filter.projectIDs {
		projectIDs = append(projectIDs, id)
	}
	sort.Strings(projectIDs)
	return map[string]any{"catalog_revision": revision, "blocks": flat,
		"edges": edges, "edges_truncated": truncated, "edge_limit": 128,
		"project_ids": projectIDs}, nil
}

func (engine *Engine) PanelCanonicalHistory(ctx context.Context, record memory.MemoryRecord) ([]memory.MemoryRecord, error) {
	if record.CanonicalKey == "" {
		return []memory.MemoryRecord{record}, nil
	}
	return engine.index.CanonicalHistory(ctx, record.CanonicalKey)
}

func (engine *Engine) PanelIsCurrent(ctx context.Context, record memory.MemoryRecord) (bool, error) {
	return engine.index.IsCurrentRecord(ctx, record)
}

// PanelRecordEdges applies the browser's 128-edge and 64 KiB budgets before
// returning evidence-bearing edges among its visible 64 recent records.
func (engine *Engine) PanelRecordEdges(ctx context.Context, records []memory.MemoryRecord) ([]map[string]any, bool, error) {
	ids := make([]string, 0, len(records))
	visible := make(map[string]bool, len(records))
	for _, record := range records {
		if !visible[record.BlockID] {
			visible[record.BlockID] = true
			ids = append(ids, record.BlockID)
		}
	}
	if len(ids) == 0 {
		return []map[string]any{}, false, nil
	}
	candidates, err := engine.index.EdgesBetween(ctx, ids, 129)
	if err != nil {
		return nil, false, err
	}
	truncated := len(candidates) > 128
	if truncated {
		candidates = candidates[:128]
	}
	result := make([]map[string]any, 0, len(candidates))
	bytesUsed := 0
	seen := map[[3]string]bool{}
	for _, edge := range candidates {
		if edge.WorkspaceID != engine.config.WorkspaceID || !visible[edge.SrcID] ||
			!visible[edge.DstID] || edge.SrcID == edge.DstID || len(edge.EvidenceRefs) == 0 {
			continue
		}
		weight, err := edge.Weight.Float64()
		if err != nil || math.IsInf(weight, 0) || math.IsNaN(weight) {
			continue
		}
		key := [3]string{edge.SrcID, edge.DstID, edge.EdgeType}
		if seen[key] {
			continue
		}
		seen[key] = true
		refs := make([]map[string]any, 0, min(len(edge.EvidenceRefs), 4))
		refsTruncated := len(edge.EvidenceRefs) > 4
		for _, ref := range edge.EvidenceRefs[:min(len(edge.EvidenceRefs), 4)] {
			refID, hash, producerID := ref.RefID, "", ref.ProducerID
			if ref.Hash != nil {
				hash = *ref.Hash
			}
			if len(refID) > 240 || len(hash) > 240 || len(ref.ProducedBy) > 64 || len(producerID) > 240 {
				refsTruncated = true
			}
			refs = append(refs, map[string]any{"ref_type": ref.RefType,
				"ref_id": trimPanelText(refID, 240), "hash": trimPanelText(hash, 240),
				"produced_by": trimPanelText(ref.ProducedBy, 64), "producer_id": trimPanelText(producerID, 240)})
		}
		payload := map[string]any{"src_id": edge.SrcID, "dst_id": edge.DstID,
			"edge_type": edge.EdgeType, "reason": trimPanelText(edge.Reason, 240),
			"reason_truncated": len(edge.Reason) > 240, "weight": weight,
			"evidence_refs": refs, "evidence_ref_count": len(edge.EvidenceRefs),
			"evidence_refs_truncated": refsTruncated}
		raw, err := jsoncompat.MarshalPythonDefault(payload, false)
		if err != nil {
			return nil, false, err
		}
		if bytesUsed+len(raw) > 64*1024 {
			return result, true, nil
		}
		result = append(result, payload)
		bytesUsed += len(raw)
	}
	return result, truncated, nil
}

func trimPanelText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
