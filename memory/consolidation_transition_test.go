package memory

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestConsolidationTransitionMatchesPythonFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.CanonicalKey = "episode:mem_bound"
	ref := EvidenceRef{RefType: "test_run", RefID: "fixture",
		ProducedBy: "verifier", ProducerID: "go-port-test"}
	newRef := EvidenceRef{RefType: "test_run", RefID: "new",
		ProducedBy: "verifier", ProducerID: "go-port-test"}
	decision := ConsolidationDecision{DecisionID: "decision-1", WorkspaceID: "alpha",
		SourceEpisodeIDs: []string{"mem_bound"}, GroupingKey: "host",
		TaskFamily: "release", Trigger: "session_boundary", ExpectedApplicability: "same workspace",
		Risk: "low", CostBudgetTokens: 1024, Rationale: "verified closure",
		EvidenceRefs: []EvidenceRef{ref}, Action: "consolidate", Status: "proposed"}
	proposal := ConsolidationProposal{Decision: decision, SourceRecords: []MemoryRecord{record},
		NewEvidenceRefs:    []EvidenceRef{newRef},
		ProposedOperations: []string{"consolidate_host_observations"},
		PostStateCandidate: map[string]any{"claims": []any{
			map[string]any{"authority": "advisory", "content": "Bound fact"}},
			"source_record_ids": []string{"mem_bound"}},
		SourceCoverageAfter: []string{"mem_bound"}, PolicyVersion: "host-lifecycle-v1",
		ModelVersion: "deterministic-host-proposer-v1", PromptVersion: "none"}
	transition, err := ProposeConsolidationTransition(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if transition.TransitionID != "transition-ef228be5c98dbd92e868ffd7" ||
		transition.PreStateSnapshotSHA256 != "sha256:88797bbfad9db0cd3d97fb99c6e31e693548fbdb5d7cf851fa8a024a6d053da5" ||
		transition.PostStateCandidateSHA256 != "sha256:6b6a6dbc47a46dfc9cb12587dc6cf99d6e1f4d9ed3d26b4614956cf7c7a37a42" ||
		transition.PostStateCandidate != `{"claims":[{"authority":"advisory","content":"Bound fact"}],"source_record_ids":["mem_bound"]}` ||
		len(transition.EvidenceRefs) != 2 || len(transition.SourceCoverageDiff.LostSourceIDs) != 0 {
		t.Fatalf("Go consolidation transition differs from Python: %+v", transition)
	}
	again, err := ProposeConsolidationTransition(proposal)
	if err != nil || !reflect.DeepEqual(again, transition) {
		t.Fatalf("consolidation transition is not deterministic: %+v, %v", again, err)
	}
	assessment, err := AssessConsolidationTransition(transition, "assessment-1", "defer",
		[]string{"needs independent tribunal"}, []EvidenceRef{newRef})
	if err != nil || assessment.TransitionID != transition.TransitionID ||
		assessment.Verdict != "defer" || assessment.CanonicalMutationApplied ||
		assessment.HostActionAuthorized {
		t.Fatalf("assessment bypassed the non-authorizing boundary: %+v, %v", assessment, err)
	}
	tampered := transition
	tampered.PostStateCandidate = `{"claims":[]}`
	if _, err := AssessConsolidationTransition(tampered, "assessment-2", "accept",
		[]string{"looks fine"}, []EvidenceRef{newRef}); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("assessment accepted a post-state changed after hashing: %v", err)
	}
}

func TestConsolidationTransitionRejectsWrongSourceSet(t *testing.T) {
	_, err := ProposeConsolidationTransition(ConsolidationProposal{
		Decision: ConsolidationDecision{DecisionID: "decision-1", WorkspaceID: "alpha",
			SourceEpisodeIDs: []string{"other"}, Status: "proposed"},
		SourceRecords: []MemoryRecord{{BlockID: "record-a", CanonicalKey: "key-a",
			RevisionID: "revision-a", WorkspaceID: "alpha"}},
	})
	if err == nil || !strings.Contains(err.Error(), "source episode set") {
		t.Fatalf("transition accepted a source set not named by the decision: %v", err)
	}
}

func TestConsolidationCoverageDiffMarksLossAndAddition(t *testing.T) {
	diff, err := NewSourceCoverageDiff([]string{"source-b", "source-a"},
		[]string{"source-c", "source-b"})
	if err != nil || !reflect.DeepEqual(diff.BeforeSourceIDs, []string{"source-a", "source-b"}) ||
		!reflect.DeepEqual(diff.AfterSourceIDs, []string{"source-b", "source-c"}) ||
		!reflect.DeepEqual(diff.AddedSourceIDs, []string{"source-c"}) ||
		!reflect.DeepEqual(diff.LostSourceIDs, []string{"source-a"}) {
		t.Fatalf("coverage diff missed exact source change: %+v, %v", diff, err)
	}
}
