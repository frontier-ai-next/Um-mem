package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/blockcatalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// BlockMap returns the bounded visible workspace tree and, when requested,
// the protected packaged taxonomy. It reads current canonical coverage only
// when depth requires the expanded block diagnostics.
func (engine *Engine) BlockMap(ctx context.Context, depth int, includeMeta bool, childLimit int) ([]map[string]any, error) {
	if depth < 1 || depth > 8 {
		return nil, errors.New("block_map depth must be an integer from 1 to 8")
	}
	if childLimit < 1 || childLimit > 200 {
		return nil, errors.New("block_map child_limit must be an integer from 1 to 200")
	}
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	navigation, err := opened.Navigate(ctx, catalog.NavigateOptions{Depth: depth, ChildLimit: childLimit})
	if err != nil {
		return nil, err
	}
	return engine.blockViews(ctx, navigation, includeMeta, depth, childLimit, "", nil)
}

type blockViewFilter struct {
	scopeKey   string
	projectIDs map[string]struct{}
}

func (engine *Engine) blockViews(ctx context.Context, navigation catalog.Navigation,
	includeMeta bool, depth, childLimit int, selectedRootID string,
	filter *blockViewFilter) ([]map[string]any, error) {
	states := map[string]catalog.BlockState{}
	allBlocks := make([]memory.Block, 0, len(navigation.States)+32)
	metaRevision := ""
	if includeMeta {
		static, err := blockcatalog.Load(blockcatalog.Options{RepoRoot: engine.config.RepoRoot,
			WorkspaceID: engine.config.WorkspaceID, ProfileID: engine.config.ProfileID, Strict: true})
		if err != nil {
			return nil, err
		}
		metaCatalog, err := memory.CompileSemanticCatalog(static)
		if err != nil {
			return nil, err
		}
		metaRevision = metaCatalog.RevisionID
		allBlocks = append(allBlocks, static...)
	}
	for _, state := range navigation.States {
		states[state.Block.BlockID] = state
		allBlocks = append(allBlocks, state.Block)
	}
	if len(allBlocks) == 0 {
		return []map[string]any{}, nil
	}
	semantic, err := memory.CompileSemanticCatalog(allBlocks)
	if err != nil {
		return nil, err
	}
	descriptors := map[string]memory.BlockDescriptor{}
	byID := map[string]memory.Block{}
	children := map[string][]string{}
	for _, descriptor := range semantic.Descriptors {
		descriptors[descriptor.BlockID] = descriptor
	}
	for _, block := range allBlocks {
		if _, exists := byID[block.BlockID]; exists {
			return nil, fmt.Errorf("catalog ID collision: %s", block.BlockID)
		}
		byID[block.BlockID] = block
		children[block.BlockID] = append([]string{}, block.ChildIDs...)
	}
	for _, block := range allBlocks {
		if block.ParentID != "" && !slices.Contains(children[block.ParentID], block.BlockID) {
			children[block.ParentID] = append(children[block.ParentID], block.BlockID)
		}
	}
	for id, visible := range navigation.ChildIDs {
		children[id] = append([]string{}, visible...)
	}
	type coverage struct {
		count      int
		freshness  string
		dimensions map[string]bool
	}
	counts := map[string]*coverage{}
	if depth > 1 {
		rows, err := engine.index.ProjectionRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			record := row.Record
			if !row.Current || row.Status != "committed" || row.Backend != engine.recordNativeStore(row.Record) ||
				record.BlockMembership == nil || record.Status == "superseded" ||
				record.Status == "deprecated" || record.Status == "retracted" ||
				record.Status == "quarantined" || record.Status == "expired" {
				continue
			}
			if filter != nil {
				if record.DeliveryPolicy == "hidden" {
					continue
				}
				key, keyErr := record.ScopeKey()
				if keyErr != nil {
					continue
				}
				if filter.projectIDs != nil && record.Scope == "project" &&
					record.ProjectID != "" && key == "project:"+record.ProjectID {
					filter.projectIDs[record.ProjectID] = struct{}{}
				}
				if filter.scopeKey != "" && key != filter.scopeKey {
					continue
				}
			}
			id := record.BlockMembership.PrimaryBlockID
			item := counts[id]
			if item == nil {
				item = &coverage{dimensions: map[string]bool{}}
				counts[id] = item
			}
			item.count++
			stamp := firstNonempty(record.UpdatedAt, record.RecordedAt, record.CreatedAt)
			if stamp > item.freshness {
				item.freshness = stamp
			}
			for _, dimension := range []string{"paths", "symbols", "entities", "config_keys", "endpoints", "artifact_hashes"} {
				if nonemptyFacet(record.Metadata[dimension]) {
					item.dimensions[dimension] = true
				}
			}
		}
	}
	var view func(string, []string) map[string]any
	view = func(id string, path []string) map[string]any {
		block := byID[id]
		descriptor := descriptors[id]
		state, dynamic := states[id]
		childIDs := append([]string{}, children[id]...)
		childCount := len(childIDs)
		cursor := ""
		if dynamic {
			childCount = navigation.ChildCounts[id]
			cursor = navigation.NextCursors[id]
		} else {
			if selectedRootID != "" {
				if count, found := navigation.ChildCounts[id]; found {
					childCount = count
					childIDs = append([]string{}, navigation.ChildIDs[id]...)
					cursor = navigation.NextCursors[id]
				}
			} else {
				if len(path) >= depth && depth != 1 {
					childIDs = []string{}
				} else if len(childIDs) > childLimit {
					childIDs = childIDs[:childLimit]
				}
				if childCount > len(childIDs) {
					cursor, _ = catalog.NavigationCursor(engine.config.WorkspaceID, engine.config.ProfileID,
						id, metaRevision, strconv.Itoa(len(childIDs)), "")
				}
			}
		}
		writable := block.AgentWritable && !block.IndexOnly && childCount == 0 && descriptor.Authority != "directive"
		status, revision, partition, protected := "active", 0, "", false
		if dynamic {
			status, revision, partition, protected = state.Status, state.Revision, state.BackendPartitionID, state.Protected
		}
		synopsis := descriptor.Guidance.Synopsis
		recordCount, freshness := 0, ""
		dimensions := []string{}
		if depth > 1 {
			if item := counts[id]; item != nil {
				recordCount, freshness = item.count, item.freshness
				for dimension := range item.dimensions {
					dimensions = append(dimensions, dimension)
				}
				sort.Strings(dimensions)
			}
			synopsis = fmt.Sprintf("%s Coverage: %d canonical record(s).", synopsis, recordCount)
		}
		nodeKind := "meta"
		if dynamic {
			nodeKind = "workspace"
		}
		payload := map[string]any{
			"block_id": id, "name": block.Name, "path": append([]string{}, path...),
			"tags": append([]string{}, block.Tags...), "description": descriptor.Guidance.Synopsis,
			"synopsis": synopsis, "purpose": map[string]any{"use_when": descriptor.Guidance.UseWhen,
				"avoid_when": descriptor.Guidance.AvoidWhen, "consumers": descriptor.Consumers},
			"scope": descriptor.Scope, "temperature": block.Temperature, "trust": descriptor.Authority,
			"parent_id": block.ParentID, "child_ids": childIDs, "child_count": childCount,
			"children_truncated": childCount > len(childIDs), "next_cursor": cursor,
			"expandable": childCount > 0, "links": map[string]any{
				"parent_id": block.ParentID, "child_ids": childIDs, "related_block_ids": []string{}},
			"record_count": recordCount, "canonical_record_count": recordCount,
			"latest_title": "", "exact_dimensions": dimensions, "freshness": freshness,
			"index_only": block.IndexOnly, "agent_writable": block.AgentWritable,
			"writable": writable, "metadata": block.Metadata,
			"workspace_id": engine.config.WorkspaceID, "profile_id": engine.config.ProfileID,
			"revision": revision, "status": status, "protected": protected,
			"backend_partition_id": partition, "meta": !dynamic, "node_kind": nodeKind,
		}
		if !dynamic && id == "memory_root_index" {
			payload["description"] = "Protected meta-node taxonomy for routing agent-authored memory."
			payload["synopsis"] = payload["description"]
			if depth > 1 {
				payload["synopsis"] = fmt.Sprintf("%s Coverage: %d canonical record(s).", payload["description"], recordCount)
			}
		}
		if depth > 1 {
			writableReason := "catalog_policy_non_agent_writable"
			switch {
			case writable:
				writableReason = "agent_writable_leaf"
			case childCount > 0:
				writableReason = "branch_index_content_free"
			case block.IndexOnly:
				writableReason = "index_node_content_free"
			case descriptor.Authority == "directive":
				writableReason = "directive_authority_host_only"
			}
			operations := []string{"recall"}
			if childCount > 0 {
				operations = []string{"expand"}
			} else if writable {
				operations = append(operations, "remember")
			}
			if childCount == 0 && block.LifecyclePolicy["mutable"] == true {
				operations = append(operations, "lifecycle_transition")
			}
			if childCount == 0 && len(block.PromotionPolicy) > 0 {
				operations = append(operations, "promote")
			}
			payload["retention"] = descriptor.Retention
			lifecycle := map[string]any{"delivery_policy": descriptor.DeliveryPolicy}
			for key, value := range descriptor.LifecyclePolicy {
				lifecycle[key] = value
			}
			payload["lifecycle"] = lifecycle
			payload["retrieval_mode"] = descriptor.RecallPath
			payload["token_budget"] = descriptor.TokenBudget
			payload["writable_reason"] = writableReason
			payload["allowed_operations"] = operations
			payload["promotion"] = map[string]any{"target": block.PromotionPolicy["target"],
				"requirements": block.PromotionPolicy["requires"]}
			payload["isolation"] = block.IsolationPolicy
			payload["native_partitions"] = []any{}
			payload["metrics"] = map[string]any{}
			payload["block_metrics"] = []any{}
		}
		relativeLevel := len(path)
		if selectedRootID != "" {
			relativeLevel = len(path) - len(navigation.PathIDs) + 1
		}
		if relativeLevel < depth {
			branches := []map[string]any{}
			for _, childID := range childIDs {
				if _, found := byID[childID]; !found || slices.Contains(path, childID) {
					continue
				}
				branches = append(branches, view(childID, append(slices.Clone(path), childID)))
			}
			if len(branches) > 0 {
				payload["children"] = branches
			}
		}
		return payload
	}
	roots := make([]memory.Block, 0)
	for _, block := range allBlocks {
		if (selectedRootID == "" && block.ParentID == "") || block.BlockID == selectedRootID {
			roots = append(roots, block)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		_, leftDynamic := states[roots[i].BlockID]
		_, rightDynamic := states[roots[j].BlockID]
		if leftDynamic != rightDynamic {
			return leftDynamic
		}
		return roots[i].Name < roots[j].Name
	})
	result := make([]map[string]any, 0, len(roots))
	for _, root := range roots {
		path := []string{root.BlockID}
		if selectedRootID != "" && len(navigation.PathIDs) > 0 {
			path = append([]string{}, navigation.PathIDs...)
		}
		result = append(result, view(root.BlockID, path))
	}
	return result, nil
}

func nonemptyFacet(value any) bool {
	switch selected := value.(type) {
	case nil:
		return false
	case string:
		return selected != ""
	case []string:
		return len(selected) > 0
	case []any:
		return len(selected) > 0
	case map[string]any:
		return len(selected) > 0
	default:
		return true
	}
}
