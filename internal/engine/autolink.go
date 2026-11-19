package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// AutoLink attaches a record to the closest existing record when the write path
// did not name a relation itself. Links used to appear only when a caller
// remembered to add them, so a store could grow to hundreds of records and stay
// a flat list. Similarity here is lexical on purpose: it is cheap, stable across
// embedding models, and explainable ("these two share terms").
func (engine *Engine) AutoLink(ctx context.Context, recordID string, threshold float64) (map[string]any, error) {
	id := strings.TrimSpace(recordID)
	if id == "" {
		return nil, errors.New("auto-link requires a record id")
	}
	if threshold <= 0 {
		threshold = 0.1
	}
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]memory.MemoryRecord, len(records))
	for _, record := range records {
		byID[record.BlockID] = record
	}
	target, found := byID[id]
	if !found {
		return nil, errors.New("record not found in the selected workspace and profile")
	}
	for _, edge := range edges {
		if edge.EdgeType != "supersedes" && (edge.SrcID == id || edge.DstID == id) {
			return map[string]any{"ok": true, "linked": false, "reason": "record already has a semantic edge", "record_id": id}, nil
		}
	}
	if target.Kind == "episode" {
		return map[string]any{"ok": true, "linked": false, "reason": "episodes are not linked automatically", "record_id": id}, nil
	}

	targetTokens := tokenSet(target.Title + " " + target.Content)
	bestID, bestScore := "", 0.0
	for _, candidate := range records {
		if candidate.BlockID == id || candidate.Status == "retracted" || candidate.Status == "superseded" || candidate.Kind == "episode" {
			continue
		}
		score := similarity(target.Title, targetTokens, candidate.Title, tokenSet(candidate.Title+" "+candidate.Content))
		if score > bestScore {
			bestScore, bestID = score, candidate.BlockID
		}
	}
	if bestID == "" || bestScore < threshold {
		return map[string]any{"ok": true, "linked": false, "record_id": id, "best_score": bestScore}, nil
	}

	weight, _ := json.Number(trimFloat(bestScore)).Float64()
	edge := memory.EdgeRecord{
		SrcID: id, DstID: bestID, EdgeType: "related_to",
		WorkspaceID: engine.config.WorkspaceID,
		Reason:      "auto-link: shared terms",
		Weight:      json.Number(trimFloat(bestScore)),
		EvidenceRefs: []memory.EvidenceRef{{
			RefType: "tool_result", RefID: "auto-link:" + id,
			ProducedBy: "agent", ProducerID: "um-mem-autolink",
		}},
		Metadata: map[string]any{"auto": true, "score": weight},
	}
	receipt, err := engine.writer.AddEdge(ctx, edge)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "linked": true, "record_id": id,
		"dst_id": receipt.DstID, "edge_type": "related_to", "score": bestScore}, nil
}

func trimFloat(value float64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

var linkStopWords = map[string]bool{
	"этот": true, "этого": true, "чтобы": true, "когда": true, "только": true, "также": true,
	"который": true, "которая": true, "после": true, "перед": true, "между": true, "нужно": true,
	"можно": true, "будет": true, "есть": true, "для": true, "или": true, "при": true,
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true,
}

func tokenSet(text string) map[string]bool {
	set := make(map[string]bool, 32)
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(field)) < 4 || linkStopWords[field] {
			continue
		}
		set[field] = true
	}
	return set
}

// similarity blends set overlap with a title bonus. Plain Jaccard punishes a
// short note that sits next to a long one, which is exactly the pair a reader
// wants connected.
func similarity(targetTitle string, left map[string]bool, candidateTitle string, right map[string]bool) float64 {
	score := jaccard(left, right)
	if overlap := containment(left, right); overlap > score {
		score = overlap
	}
	sharedTitle := 0
	titleTokens := tokenSet(candidateTitle)
	for token := range tokenSet(targetTitle) {
		if titleTokens[token] {
			sharedTitle++
		}
	}
	if sharedTitle >= 2 {
		score += 0.15
	}
	if score > 1 {
		score = 1
	}
	return score
}

func containment(left, right map[string]bool) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	smaller := len(left)
	if len(right) < smaller {
		smaller = len(right)
	}
	shared := 0
	for token := range left {
		if right[token] {
			shared++
		}
	}
	return float64(shared) / float64(smaller)
}

func jaccard(left, right map[string]bool) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	shared := 0
	for token := range left {
		if right[token] {
			shared++
		}
	}
	union := len(left) + len(right) - shared
	if union == 0 {
		return 0
	}
	return float64(shared) / float64(union)
}

// Stats is the product view of a store: not hygiene, but whether the memory is
// maturing. Without these numbers "the memory got better" stays an opinion.
func (engine *Engine) Stats(ctx context.Context) (map[string]any, error) {
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	byBlock := map[string]int{}
	byKind := map[string]int{}
	byStatus := map[string]int{}
	semantic := map[string]bool{}
	verifiedBy := map[string]bool{}
	for _, edge := range edges {
		if edge.EdgeType == "supersedes" {
			continue
		}
		semantic[edge.SrcID] = true
		semantic[edge.DstID] = true
		if edge.EdgeType == "verified_by" || edge.EdgeType == "supports" {
			verifiedBy[edge.SrcID] = true
			verifiedBy[edge.DstID] = true
		}
	}
	withdrawn := retractedTargets(records, edges)
	live, promoted, linked, labeled, eligible := 0, 0, 0, 0, 0
	for _, record := range records {
		if record.Status == "retracted" || record.Status == "superseded" || withdrawn[record.BlockID] {
			continue
		}
		live++
		byBlock[logicalBlockOf(record)]++
		byKind[record.Kind]++
		byStatus[record.Status]++
		if record.VerificationStatus == "verified" || record.Status == "verified" || record.PromotionDecision == "promoted" {
			promoted++
		}
		if semantic[record.BlockID] {
			linked++
		}
		// A candidate becomes eligible for review when it carries more than one
		// piece of evidence, or when someone linked a verification to it.
		if len(promotionReasons(record, verifiedBy)) > 0 {
			eligible++
		}
		for key := range record.Metadata {
			if !strings.HasPrefix(key, "operation_") && key != "write_mode" && key != "evidence_kind" &&
				key != "route_decision_reason" && key != "status_reason" {
				labeled++
				break
			}
		}
	}
	types := make([]string, 0, len(byKind))
	for name := range byKind {
		types = append(types, name)
	}
	sort.Strings(types)
	return map[string]any{
		"ok": true, "workspace_id": engine.config.WorkspaceID, "backend": engine.config.BackendID,
		"records": live, "retracted": len(records) - live,
		"blocks": byBlock, "kinds": byKind, "statuses": byStatus,
		"promoted": promoted, "linked": linked, "labeled": labeled, "promotion_candidates": eligible,
		"edges": len(edges), "semantic_edges": len(semantic),
		"promoted_share": share(promoted, live), "linked_share": share(linked, live),
		"labeled_share": share(labeled, live),
	}, nil
}

func share(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// retractedTargets collects records that a retraction record already covers.
// Retraction is modelled as a successor record plus a `supersedes` edge, so the
// original keeps its candidate status and only the edge marks it as withdrawn.
func retractedTargets(records []memory.MemoryRecord, edges []memory.EdgeRecord) map[string]bool {
	targets := map[string]bool{}
	for _, edge := range edges {
		// Any supersedes edge retires its target: an amendment replaces the old
		// revision just as a retraction does, and both leave the original record
		// with its candidate status.
		if edge.EdgeType == "supersedes" {
			targets[edge.DstID] = true
		}
	}
	return targets
}
