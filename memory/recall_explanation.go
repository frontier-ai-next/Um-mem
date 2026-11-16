package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/text/cases"
)

type RecallQualityGate struct {
	CandidateRecallThreshold         float64 `json:"candidate_recall_threshold"`
	RenderedLiteralCoverageThreshold float64 `json:"rendered_literal_coverage_threshold"`
	CandidateRecall                  float64 `json:"candidate_recall"`
	RenderedLiteralCoverage          float64 `json:"rendered_literal_coverage"`
	Passed                           bool    `json:"passed"`
	Status                           string  `json:"status"`
}

type RecallExplanation struct {
	Query                 string               `json:"query"`
	QueryType             string               `json:"query_type"`
	WorkspaceID           string               `json:"workspace_id"`
	RunID                 string               `json:"run_id"`
	BackendID             string               `json:"backend_id"`
	PlanReason            string               `json:"plan_reason"`
	CandidateRecordIDs    []string             `json:"candidate_record_ids"`
	RenderedRecordIDs     []string             `json:"rendered_record_ids"`
	Budget                map[string]int       `json:"budget"`
	Visibility            []EvidenceVisibility `json:"visibility"`
	ExactLiteralCapsule   []string             `json:"exact_literal_capsule"`
	ExactLiteralLosses    []ExactLiteralLoss   `json:"exact_literal_losses"`
	CanonicalSourceRefs   []map[string]any     `json:"canonical_source_refs"`
	CorrectiveRecall      map[string]any       `json:"corrective_recall"`
	QualityGate           RecallQualityGate    `json:"quality_gate"`
	RenderedContextSHA256 string               `json:"rendered_context_sha256"`
	PromptIncludesTrace   bool                 `json:"prompt_includes_trace"`
}

func explanationIDs(records []MemoryRecord, items []map[string]any) ([]string, []string) {
	candidates, rendered := []string{}, []string{}
	for _, record := range records {
		candidates = appendUniqueExplanationID(candidates, record.BlockID)
	}
	for _, item := range items {
		metadata, _ := item["metadata"].(map[string]any)
		id, _ := metadata["block_id"].(string)
		if id == "" {
			id, _ = item["source"].(string)
		}
		rendered = appendUniqueExplanationID(rendered, id)
	}
	return candidates, rendered
}

func appendUniqueExplanationID(values []string, id string) []string {
	if id == "" {
		return values
	}
	for _, existing := range values {
		if existing == id {
			return values
		}
	}
	return append(values, id)
}

func recallQualityGate(candidates []string, rendered string, goldIDs, goldLiterals []string) RecallQualityGate {
	gate := RecallQualityGate{CandidateRecallThreshold: 1, RenderedLiteralCoverageThreshold: 1,
		Status: "not_evaluated"}
	if len(goldIDs) == 0 && len(goldLiterals) == 0 {
		return gate
	}
	gate.CandidateRecall, gate.RenderedLiteralCoverage = 1, 1
	if len(goldIDs) > 0 {
		matched := 0
		for _, id := range goldIDs {
			for _, candidate := range candidates {
				if id == candidate {
					matched++
					break
				}
			}
		}
		gate.CandidateRecall = float64(matched) / float64(len(goldIDs))
	}
	if len(goldLiterals) > 0 {
		matched := 0
		folded := cases.Fold().String(rendered)
		for _, literal := range goldLiterals {
			if strings.Contains(folded, cases.Fold().String(literal)) {
				matched++
			}
		}
		gate.RenderedLiteralCoverage = float64(matched) / float64(len(goldLiterals))
	}
	gate.Passed = gate.CandidateRecall >= 1 && gate.RenderedLiteralCoverage >= 1
	if gate.Passed {
		gate.Status = "passed"
	} else {
		gate.Status = "failed"
	}
	return gate
}

// BuildRecallExplanation returns out-of-prompt evidence and quality metrics.
// The trace itself is never added to PackedEvidencePayload.RenderedContext.
func BuildRecallExplanation(plan RecallPlan, records []MemoryRecord, payload PackedEvidencePayload,
	backendID string, goldBlockIDs, goldLiterals []string) RecallExplanation {
	candidates, rendered := explanationIDs(records, payload.Items)
	sources := make([]map[string]any, 0)
	corrective := map[string]any{}
	for _, record := range records {
		metadata := record.Metadata
		if next, ok := metadata["corrective_recall"].(map[string]any); ok {
			corrective = make(map[string]any, len(next))
			for key, value := range next {
				corrective[key] = value
			}
		}
		depth := false
		switch value := metadata["reverse_provenance_depth"].(type) {
		case int:
			depth = value == 1
		case int64:
			depth = value == 1
		case float64:
			depth = value == 1
		}
		if !depth {
			continue
		}
		textValue := func(raw any) string {
			if raw == nil {
				return ""
			}
			return fmt.Sprint(raw)
		}
		id, _ := record.Metadata["reverse_provenance_derived_id"]
		root, _ := record.Metadata["reverse_provenance_root_id"]
		referenceOnly, _ := record.Metadata["reverse_provenance_reference_only"].(bool)
		sources = append(sources, map[string]any{"record_id": record.BlockID,
			"derived_id": textValue(id), "root_id": textValue(root), "reference_only": referenceOnly})
	}
	visibility := append([]EvidenceVisibility{}, payload.Visibility...)
	capsule := append([]string{}, payload.ExactLiteralCapsule...)
	losses := append([]ExactLiteralLoss{}, payload.ExactLiteralLosses...)
	digest := sha256.Sum256([]byte(payload.RenderedContext))
	return RecallExplanation{Query: plan.Query, QueryType: plan.QueryType, WorkspaceID: plan.WorkspaceID,
		RunID: plan.RunID, BackendID: backendID, PlanReason: plan.Reason, CandidateRecordIDs: candidates,
		RenderedRecordIDs: rendered, Budget: map[string]int{"requested_tokens": plan.MaxTokens,
			"allocated_tokens": payload.TokenBudget, "estimated_tokens": payload.EstimatedTokens,
			"metadata_tokens": payload.MetadataTokens}, Visibility: visibility,
		ExactLiteralCapsule: capsule, ExactLiteralLosses: losses, CanonicalSourceRefs: sources,
		CorrectiveRecall: corrective, QualityGate: recallQualityGate(candidates, payload.RenderedContext, goldBlockIDs, goldLiterals),
		RenderedContextSHA256: hex.EncodeToString(digest[:])}
}
