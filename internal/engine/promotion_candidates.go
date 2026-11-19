package engine

import (
	"context"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// PromotionCandidate is a record that carries enough independent support to be
// reviewed for promotion. Promotion itself stays a reviewed act: this only
// makes the queue visible, because a store where nothing is ever eligible can
// never mature and nobody notices.
type PromotionCandidate struct {
	RecordID      string   `json:"record_id"`
	StableID      string   `json:"stable_id"`
	Title         string   `json:"title"`
	LogicalBlock  string   `json:"logical_block_id"`
	Kind          string   `json:"kind"`
	Status        string   `json:"status"`
	EvidenceCount int      `json:"evidence_count"`
	Reasons       []string `json:"reasons"`
}

// PromotionCandidates applies the promotion rules over the live graph.
func (engine *Engine) PromotionCandidates(ctx context.Context, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 200 {
		limit = 200
	}
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]memory.MemoryRecord, len(records))
	for _, record := range records {
		byID[record.BlockID] = record
	}
	withdrawn := retractedTargets(records, edges)
	support := map[string]bool{}
	for _, edge := range edges {
		if edge.EdgeType == "supports" || edge.EdgeType == "verified_by" {
			support[edge.SrcID] = true
			support[edge.DstID] = true
		}
	}

	candidates := make([]PromotionCandidate, 0, limit)
	for _, record := range records {
		if record.Status != "candidate" && record.Status != "verified" {
			continue
		}
		if withdrawn[record.BlockID] {
			continue
		}
		reasons := promotionReasons(record, support)
		if len(reasons) == 0 {
			continue
		}
		candidates = append(candidates, PromotionCandidate{
			RecordID: record.BlockID, StableID: stableRoot(byID, record.BlockID),
			Title: record.Title, LogicalBlock: logicalBlockOf(record), Kind: record.Kind,
			Status: record.Status, EvidenceCount: len(record.EvidenceRefs), Reasons: reasons,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if len(candidates[i].Reasons) != len(candidates[j].Reasons) {
			return len(candidates[i].Reasons) > len(candidates[j].Reasons)
		}
		return candidates[i].Title < candidates[j].Title
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return map[string]any{
		"ok": true, "count": len(candidates), "candidates": candidates,
		"workspace_id": engine.config.WorkspaceID, "backend": engine.config.BackendID,
		"note": "promotion stays reviewed; these records only qualify for review",
	}, nil
}

// promotionReasons is the single definition of "ready for review". Statistics and
// the queue must agree, otherwise the two numbers argue with each other.
func promotionReasons(record memory.MemoryRecord, support map[string]bool) []string {
	reasons := make([]string, 0, 3)
	for _, ref := range record.EvidenceRefs {
		if ref.RefType == "human_approval" {
			reasons = append(reasons, "human_approval")
			break
		}
	}
	if len(record.EvidenceRefs) >= 2 {
		reasons = append(reasons, "multiple_evidence")
	}
	if support[record.BlockID] {
		reasons = append(reasons, "supported_by_edge")
	}
	return reasons
}

func stableRoot(byID map[string]memory.MemoryRecord, start string) string {
	current := start
	visited := map[string]bool{}
	for {
		record, ok := byID[current]
		if !ok || strings.TrimSpace(record.RevisionOf) == "" || visited[current] {
			return current
		}
		visited[current] = true
		if _, ok := byID[record.RevisionOf]; !ok {
			return current
		}
		current = record.RevisionOf
	}
}
