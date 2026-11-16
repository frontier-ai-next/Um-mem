package memory

import "testing"

func TestRecallExplanationSeparatesCandidateAndRenderedCoverage(t *testing.T) {
	plan := NewRecallPlan("Parser error")
	plan.WorkspaceID, plan.RunID, plan.Reason, plan.MaxTokens = "ws", "run-1", "explicit_block_first_recall", 100
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	records := []MemoryRecord{{BlockID: "memory-1", Metadata: map[string]any{
		"reverse_provenance_depth": 1, "reverse_provenance_derived_id": "derived-1",
		"reverse_provenance_root_id": "root-1", "reverse_provenance_reference_only": true}}}
	packed := PackedEvidencePayload{Items: []map[string]any{{"source": "memory-1", "metadata": map[string]any{"block_id": "memory-1"}}},
		RenderedContext: "The parser error is fixed.", TokenBudget: 100, EstimatedTokens: 8, MetadataTokens: 2,
		Visibility: []EvidenceVisibility{{RecordID: "memory-1", State: "rendered"}}}
	unknown := BuildRecallExplanation(plan, records, packed, "mempalace", nil, nil)
	if unknown.QualityGate.Status != "not_evaluated" || unknown.PromptIncludesTrace || len(unknown.CanonicalSourceRefs) != 1 {
		t.Fatalf("unscored explanation: %+v", unknown)
	}
	passed := BuildRecallExplanation(plan, records, packed, "mempalace", []string{"memory-1"}, []string{"PARSER ERROR"})
	if !passed.QualityGate.Passed || passed.QualityGate.Status != "passed" || passed.QualityGate.CandidateRecall != 1 ||
		passed.QualityGate.RenderedLiteralCoverage != 1 {
		t.Fatalf("covered explanation: %+v", passed)
	}
	failed := BuildRecallExplanation(plan, records, packed, "mempalace", []string{"memory-2"}, []string{"missing literal"})
	if failed.QualityGate.Passed || failed.QualityGate.Status != "failed" || failed.QualityGate.CandidateRecall != 0 ||
		failed.QualityGate.RenderedLiteralCoverage != 0 {
		t.Fatalf("missed explanation: %+v", failed)
	}
}
