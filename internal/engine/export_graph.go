package engine

import (
	"context"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

var provenanceEdgeTypes = map[string]bool{
	"derived_from": true, "observed_in": true, "produced_by": true,
	"produced_in": true, "verified_by": true, "verifies": true,
	"refutes": true, "revises": true, "supersedes": true,
	"supports": true, "cites": true, "references_file": true,
	"from_subtask": true,
}

func edgeIdentity(edge memory.EdgeRecord) map[string]any {
	return map[string]any{"src_id": edge.SrcID, "dst_id": edge.DstID, "edge_type": edge.EdgeType}
}

func graphCycles(edges []memory.EdgeRecord) [][]string {
	adjacency := make(map[string]map[string]bool)
	for _, edge := range edges {
		if edge.EdgeType != "derived_from" && edge.EdgeType != "supersedes" && edge.EdgeType != "revises" {
			continue
		}
		if adjacency[edge.SrcID] == nil {
			adjacency[edge.SrcID] = make(map[string]bool)
		}
		adjacency[edge.SrcID][edge.DstID] = true
	}
	visited, activeIndex := make(map[string]bool), make(map[string]int)
	active := make([]string, 0)
	cycles := make([][]string, 0)
	var visit func(string)
	visit = func(id string) {
		if start, ok := activeIndex[id]; ok {
			path := append([]string(nil), active[start:]...)
			cycles = append(cycles, append(path, id))
			return
		}
		if visited[id] {
			return
		}
		activeIndex[id] = len(active)
		active = append(active, id)
		children := make([]string, 0, len(adjacency[id]))
		for child := range adjacency[id] {
			children = append(children, child)
		}
		sort.Strings(children)
		for _, child := range children {
			visit(child)
		}
		active = active[:len(active)-1]
		delete(activeIndex, id)
		visited[id] = true
	}
	starts := make([]string, 0, len(adjacency))
	for id := range adjacency {
		starts = append(starts, id)
	}
	sort.Strings(starts)
	for _, id := range starts {
		visit(id)
	}
	return cycles
}

// ExportGraph preserves historical records and audits the complete scoped
// graph. An invalid audit is reported as data so operators can inspect it.
func (engine *Engine) ExportGraph(ctx context.Context) (map[string]any, error) {
	records, edges, indexNodes, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]memory.MemoryRecord, len(records))
	for _, record := range records {
		byID[record.BlockID] = record
	}
	endpoints := make(map[string]bool, len(byID)+len(indexNodes))
	for id := range byID {
		endpoints[id] = true
	}
	filteredNodes := make([]string, 0, len(indexNodes))
	for _, id := range indexNodes {
		if !endpoints[id] {
			endpoints[id] = true
			filteredNodes = append(filteredNodes, id)
		}
	}
	dangling, crossWorkspace := make([]map[string]any, 0), make([]map[string]any, 0)
	missingEvidence, crossBlockRevision := make([]map[string]any, 0), make([]map[string]any, 0)
	routes := make(map[string][]map[string]any)
	incomingWitness := make(map[string]bool)
	for _, edge := range edges {
		if !endpoints[edge.SrcID] || !endpoints[edge.DstID] {
			dangling = append(dangling, edgeIdentity(edge))
		}
		if edge.WorkspaceID != engine.config.WorkspaceID {
			item := edgeIdentity(edge)
			item["workspace_id"] = edge.WorkspaceID
			crossWorkspace = append(crossWorkspace, item)
		}
		if len(edge.EvidenceRefs) == 0 {
			missingEvidence = append(missingEvidence, edgeIdentity(edge))
		}
		if edge.EdgeType == "supersedes" || edge.EdgeType == "revises" {
			source, sourceOK := byID[edge.SrcID]
			target, targetOK := byID[edge.DstID]
			if !sourceOK || !targetOK || source.CanonicalKey != target.CanonicalKey ||
				source.BlockMembership == nil || target.BlockMembership == nil ||
				source.BlockMembership.PrimaryBlockID != target.BlockMembership.PrimaryBlockID {
				crossBlockRevision = append(crossBlockRevision, edgeIdentity(edge))
			}
		}
		if provenanceEdgeTypes[edge.EdgeType] {
			if _, ok := byID[edge.SrcID]; ok {
				routes[edge.SrcID] = append(routes[edge.SrcID], map[string]any{
					"edge_type": edge.EdgeType, "source_id": edge.SrcID, "target_id": edge.DstID,
					"reason": edge.Reason, "evidence_refs": edge.EvidenceRefs, "metadata": edge.Metadata})
			}
			incomingWitness[edge.DstID] = true
		}
	}
	durableIDs, isolatedIDs, unreachableWitness := make([]string, 0), make([]string, 0), make([]string, 0)
	durableWitnessRefs := make(map[string]bool)
	for _, record := range records {
		if record.Retention == "durable" {
			if id := strings.TrimSpace(stringValue(record.Metadata["raw_witness_id"])); id != "" {
				durableWitnessRefs[id] = true
			}
		}
	}
	for _, record := range records {
		if record.Retention == "durable" && record.DeliveryPolicy != "hidden" &&
			record.Status != "quarantined" && record.Status != "superseded" &&
			record.Status != "deprecated" && record.Status != "retracted" {
			durableIDs = append(durableIDs, record.BlockID)
			if len(routes[record.BlockID]) == 0 {
				isolatedIDs = append(isolatedIDs, record.BlockID)
			}
		}
		if kind, _ := record.Metadata["record_kind"].(string); kind == "raw_host_witness" &&
			!incomingWitness[record.BlockID] && durableWitnessRefs[record.BlockID] {
			unreachableWitness = append(unreachableWitness, record.BlockID)
		}
	}
	sort.Strings(durableIDs)
	sort.Strings(isolatedIDs)
	sort.Strings(unreachableWitness)
	cycles := graphCycles(edges)
	valid := len(dangling) == 0 && len(crossWorkspace) == 0 && len(missingEvidence) == 0 &&
		len(crossBlockRevision) == 0 && len(cycles) == 0 && len(isolatedIDs) == 0 && len(unreachableWitness) == 0
	audit := map[string]any{"valid": valid, "record_count": len(records), "edge_count": len(edges),
		"durable_semantic_record_count":        len(durableIDs),
		"isolated_durable_semantic_record_ids": isolatedIDs,
		"dangling_edges":                       dangling, "cross_workspace_edges": crossWorkspace,
		"cross_block_revision_edges": crossBlockRevision, "cycle_paths": cycles,
		"missing_edge_provenance":           missingEvidence,
		"unreachable_derived_witness_ids":   unreachableWitness,
		"semantic_similarity_edges_created": 0}
	return map[string]any{"blocks": records, "edges": edges, "index_node_ids": filteredNodes,
		"provenance_routes": routes, "audit": audit}, nil
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
