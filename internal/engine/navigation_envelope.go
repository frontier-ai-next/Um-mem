package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
)

var maintenanceProposalOptions = map[string][]string{
	"capacity_pressure":        {"propose_split", "propose_budget_change"},
	"low_usage":                {"propose_merge", "propose_archive"},
	"high_revision_count":      {"propose_consolidation"},
	"many_conflicts":           {"propose_split", "propose_edge_review"},
	"active_reference_present": {"propose_mask_review"},
	"consolidation_available":  {"propose_consolidation"},
}

func (engine *Engine) ObserveMaintenance(ctx context.Context,
	request catalog.AddObservationRequest) (map[string]any, error) {
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	observation, err := opened.AddObservation(ctx, request)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}
	payload["ok"] = true
	return payload, nil
}

func blockStateMapping(state catalog.BlockState) (map[string]any, error) {
	encoded, err := json.Marshal(state.Block)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}
	payload["workspace_id"], payload["profile_id"] = state.WorkspaceID, state.ProfileID
	payload["revision"], payload["status"], payload["protected"] = state.Revision, state.Status, state.Protected
	payload["backend_partition_id"] = state.BackendPartitionID
	payload["created_at"], payload["updated_at"] = state.CreatedAt, state.UpdatedAt
	return payload, nil
}

func (engine *Engine) NavigationEnvelope(ctx context.Context, blockID string,
	limit int, cursor, relatedCursor string) (map[string]any, error) {
	if blockID == "" || blockID != strings.TrimSpace(blockID) {
		return nil, errors.New("navigation_envelope requires an exact block_id")
	}
	expanded, err := engine.ExpandBlock(ctx, blockID, limit, cursor)
	if err != nil {
		return nil, err
	}
	revision, ok := expanded["catalog_revision"].(int)
	if !ok {
		return nil, errors.New("expanded catalog revision is invalid")
	}
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	page, err := opened.PageEdges(ctx, blockID, limit, relatedCursor, &revision)
	if err != nil {
		return nil, err
	}
	observations, err := opened.Observations(ctx, blockID)
	if err != nil {
		return nil, err
	}
	related := make([]map[string]any, 0, len(page.Edges))
	for _, edge := range page.Edges {
		other := edge.SrcID
		if other == blockID {
			other = edge.DstID
		}
		state, err := opened.GetBlock(ctx, other)
		if err != nil {
			return nil, err
		}
		if state == nil {
			continue
		}
		item, err := blockStateMapping(*state)
		if err != nil {
			return nil, err
		}
		item["edge"] = edge
		related = append(related, item)
	}
	options := make([]string, 0)
	seen := map[string]bool{}
	for _, observation := range observations {
		for _, option := range maintenanceProposalOptions[observation.ObservationType] {
			if !seen[option] {
				options = append(options, option)
				seen[option] = true
			}
		}
	}
	root, ok := expanded["root"].(map[string]any)
	if !ok {
		return nil, errors.New("expanded block payload is invalid")
	}
	hints := map[string]any{"native_partitions": []any{}, "block_metrics": []any{}, "freshness": ""}
	for key := range hints {
		if value, exists := root[key]; exists {
			hints[key] = value
		}
	}
	return map[string]any{"ok": true,
		"workspace_id": engine.config.WorkspaceID, "profile_id": engine.config.ProfileID,
		"catalog_revision": revision, "tree_path": expanded["path_ids"],
		"next_cursor": expanded["next_cursor"], "child_count": expanded["child_count"],
		"block": root, "children": expanded["children"],
		"related_blocks": related, "related_count": page.Total,
		"related_next_cursor":     page.NextCursor,
		"related_edges_scope":     expanded["related_edges_scope"],
		"related_edges_truncated": expanded["related_edges_truncated"],
		"related_edges_limit":     expanded["related_edges_limit"],
		"health_hints":            hints, "maintenance_observations": observations,
		"proposal_options": options, "mutation_performed": false}, nil
}
