package memory

import (
	"slices"
	"testing"
)

func tribunalFixturePair(t *testing.T, metadata map[string]any, content string, after []string,
	mutate func(map[string]any), semanticChange func(*TransitionSemanticVerification)) (ConsolidationTransition, ConsolidationTribunalReport) {
	t.Helper()
	ref := EvidenceRef{RefType: "verification_report", RefID: "record", ProducedBy: "verifier", ProducerID: "tribunal-test"}
	record := MemoryRecord{BlockID: "episode-rare", CanonicalKey: "episode:episode-rare",
		RevisionID: "episode-rare@1", WorkspaceID: "ws", Kind: "episode",
		Content: content, Status: "candidate", Authority: "advisory", Scope: "workspace",
		EvidenceRefs: []EvidenceRef{ref}, Metadata: metadata}
	decision := ConsolidationDecision{DecisionID: "decision-rare", WorkspaceID: "ws",
		SourceEpisodeIDs: []string{record.BlockID}, GroupingKey: "release-constraints",
		TaskFamily: "release", Trigger: "rare_irreversible_fact",
		ExpectedApplicability: "same release procedure", Risk: "critical",
		CostBudgetTokens: 512, Rationale: "preserve one-off constraint",
		EvidenceRefs: []EvidenceRef{ref}, Action: "consolidate", Status: "proposed"}
	candidate := map[string]any{"workspace_id": "ws", "authority": "advisory",
		"scope": "workspace", "status": "candidate", "source_record_ids": []any{record.BlockID},
		"claims": []any{map[string]any{"content": content, "source_record_ids": []any{record.BlockID}}},
		"materialized_views": []any{map[string]any{"view_id": "view-release", "content": content,
			"source_record_ids": []any{record.BlockID}}},
		"continuation_applicability": "same release procedure",
		"procedure": map[string]any{"prerequisites": []any{"release owner"},
			"steps": []any{"retain exact constraint"}, "applicability": "same release procedure",
			"contraindications": []any{"source correction pending"},
			"evidence_refs":     []any{"episode-rare"}, "outcomes": []any{"preserved"}}}
	if mutate != nil {
		mutate(candidate)
	}
	transition, err := ProposeConsolidationTransition(ConsolidationProposal{
		Decision: decision, SourceRecords: []MemoryRecord{record}, NewEvidenceRefs: []EvidenceRef{ref},
		ProposedOperations: []string{"materialize_view", "materialize_procedure"},
		TouchedViewIDs:     []string{"view-release"}, PostStateCandidate: candidate,
		SourceCoverageAfter: after, PolicyVersion: "consolidation-policy-v1",
		ModelVersion: "proposer-model-v1", PromptVersion: "consolidation-prompt-v1"})
	if err != nil {
		t.Fatal(err)
	}
	semantic := TransitionSemanticVerification{VerifierID: "semantic-verifier",
		VerifierModelVersion: "independent-verifier-v1", Coverage: true, Preservation: true,
		Faithfulness: true, IndependentFromProposer: true,
		Reasons: []string{"semantic axes evaluated against canonical sources"}, EvidenceRefs: []EvidenceRef{ref}}
	if semanticChange != nil {
		semanticChange(&semantic)
	}
	report, err := AssessConsolidationTribunal(transition, decision, []MemoryRecord{record}, semantic, "assessment-tribunal")
	if err != nil {
		t.Fatal(err)
	}
	return transition, report
}

func tribunalFixture(t *testing.T, metadata map[string]any, content string, after []string,
	mutate func(map[string]any), semanticChange func(*TransitionSemanticVerification)) ConsolidationTribunalReport {
	_, report := tribunalFixturePair(t, metadata, content, after, mutate, semanticChange)
	return report
}

func TestConsolidationTribunalAcceptsCoveredRareFact(t *testing.T) {
	report := tribunalFixture(t, map[string]any{"rare_critical": true,
		"exact_literals": []any{"DO_NOT_ROTATE_KEY_7"}},
		"Keep literal DO_NOT_ROTATE_KEY_7 for the release procedure.",
		[]string{"episode-rare"}, nil, nil)
	if report.Verdict != "accept" || report.Assessment.Verdict != "accept" ||
		len(report.DeterministicChecks) != 11 || len(report.FailedCheckIDs()) != 0 ||
		!slices.Equal(report.ProtectedLiterals, []string{"DO_NOT_ROTATE_KEY_7"}) ||
		report.CanonicalMutationApplied || report.HostActionAuthorized ||
		report.Assessment.CanonicalMutationApplied || report.Assessment.HostActionAuthorized {
		t.Fatalf("tribunal failed the non-authorizing accepted fixture: %+v", report)
	}
}

func TestConsolidationTribunalRejectsPythonGovernanceFixtures(t *testing.T) {
	tests := []struct {
		name           string
		metadata       map[string]any
		content        string
		after          []string
		mutate         func(map[string]any)
		semanticChange func(*TransitionSemanticVerification)
		failed         []string
	}{
		{"lost source and literal", map[string]any{"exact_literals": []any{"DO_NOT_ROTATE_KEY_7"}}, "DO_NOT_ROTATE_KEY_7", nil,
			func(c map[string]any) {
				c["claims"] = []any{map[string]any{"content": "A lossy summary."}}
				c["materialized_views"] = []any{map[string]any{"view_id": "view-release", "content": "A lossy summary.", "source_record_ids": []any{}}}
			}, nil,
			[]string{"source_coverage", "rare_exact_literals"}},
		{"authority uplift", nil, "safe", []string{"episode-rare"}, func(c map[string]any) { c["authority"] = "directive" }, nil, []string{"no_authority_uplift"}},
		{"wrong origin", nil, "safe", []string{"episode-rare"}, func(c map[string]any) { c["workspace_id"] = "other" }, nil, []string{"origin_integrity"}},
		{"scope widened", nil, "safe", []string{"episode-rare"}, func(c map[string]any) { c["scope"] = "manager" }, nil, []string{"scope_validity_lifecycle"}},
		{"view missing", nil, "safe", []string{"episode-rare"}, func(c map[string]any) { c["materialized_views"] = []any{} }, nil, []string{"affected_materialized_views"}},
		{"applicability drift", nil, "safe", []string{"episode-rare"}, func(c map[string]any) { c["continuation_applicability"] = "all tasks" }, nil, []string{"continuation_applicability"}},
		{"incomplete procedure", nil, "safe", []string{"episode-rare"}, func(c map[string]any) { c["procedure"] = map[string]any{"steps": []any{"unsafe"}} }, nil, []string{"procedure_applicability"}},
		{"verifier not independent", nil, "safe", []string{"episode-rare"}, nil, func(s *TransitionSemanticVerification) { s.IndependentFromProposer = false }, []string{"independent_semantic_verification"}},
		{"verifier same model", nil, "safe", []string{"episode-rare"}, nil, func(s *TransitionSemanticVerification) { s.VerifierModelVersion = "proposer-model-v1" }, []string{"independent_semantic_verification"}},
		{"semantic preservation failed", nil, "safe", []string{"episode-rare"}, nil, func(s *TransitionSemanticVerification) { s.Preservation = false }, []string{"independent_semantic_verification"}},
		{"privacy erase", map[string]any{"privacy_erased": true, "exact_literals": []any{"ERASE_ME_42"}}, "secret ERASE_ME_42", []string{"episode-rare"}, nil, nil, []string{"privacy_erase_closure"}},
		{"correction dropped", map[string]any{"correction_of": []any{"episode-old-port"}}, "port 4318", []string{"episode-rare"},
			func(c map[string]any) { c["correction_ids"] = []any{} }, nil, []string{"contradiction_correction_closure"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := tribunalFixture(t, test.metadata, test.content, test.after, test.mutate, test.semanticChange)
			if report.Verdict != "reject" || report.Assessment.Verdict != "reject" ||
				report.CanonicalMutationApplied || report.HostActionAuthorized {
				t.Fatalf("unsafe candidate was not rejected: %+v", report)
			}
			for _, id := range test.failed {
				if !slices.Contains(report.FailedCheckIDs(), id) {
					t.Fatalf("missing failed check %s: %v", id, report.FailedCheckIDs())
				}
			}
		})
	}
}
