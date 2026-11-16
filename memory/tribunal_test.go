package memory

import (
	"math"
	"testing"
)

func TestTribunalDecisionsMatchDeterministicContract(t *testing.T) {
	proof := EvidenceRef{RefType: "verification_report", RefID: "report-1",
		ProducedBy: "verifier", ProducerID: "reviewer"}
	verifier := "verifier"
	base := TribunalInput{CandidateID: "candidate-1", Actor: &verifier,
		WorkspaceID: "alpha", TargetWorkspaceID: "alpha", SourceBackend: "mnemosyne",
		Rules: []TribunalRule{{Status: "candidate"}}, SourceEvidence: []EvidenceRef{proof}}
	decide := func(input TribunalInput) TribunalDecision {
		t.Helper()
		decision, err := RunTribunal(input)
		if err != nil {
			t.Fatal(err)
		}
		return decision
	}
	if decision := decide(base); decision.Decision != "accept" || len(decision.Checks) != 6 {
		t.Fatalf("valid candidate was rejected: %+v", decision)
	}
	defaultActor := base
	defaultActor.Actor = nil
	if decision := decide(defaultActor); decision.Decision != "accept" {
		t.Fatalf("default UM-MEM actor was rejected: %+v", decision)
	}
	emptyActor := ""
	defaultActor.Actor = &emptyActor
	if decision := decide(defaultActor); decision.Reason != "untrusted_actor" {
		t.Fatalf("explicit empty actor was trusted: %+v", decision)
	}
	missing := base
	missing.SourceEvidence = nil
	if decision := decide(missing); decision.Decision != "needs_more_evidence" ||
		decision.Reason != "missing_typed_evidence" {
		t.Fatalf("missing typed proof got wrong verdict: %+v", decision)
	}
	passive := base
	passiveActor := "mnemosyne"
	passive.Actor = &passiveActor
	if decision := decide(passive); decision.Decision != "reject" ||
		decision.Reason != "passive_backend_cannot_self_promote" {
		t.Fatalf("passive backend self-promotion passed: %+v", decision)
	}
	crossWorkspace := base
	crossWorkspace.TargetWorkspaceID = "other"
	if decision := decide(crossWorkspace); decision.Reason != "cross_workspace_candidate_blocked" {
		t.Fatalf("cross-workspace candidate passed: %+v", decision)
	}
	stale := base
	stale.Rules = []TribunalRule{{Status: "superseded"}}
	if decision := decide(stale); decision.Reason != "status_superseded" {
		t.Fatalf("superseded candidate passed: %+v", decision)
	}
	contradicted := base
	contradicted.GraphEdges = []EdgeRecord{{SrcID: "candidate-1", DstID: "fact-2",
		EdgeType: "contradicts"}}
	if decision := decide(contradicted); decision.Reason != "hard_contradiction_path" {
		t.Fatalf("contradiction passed: %+v", decision)
	}
	lowUtility := base
	value := -0.75
	lowUtility.UtilityValue = &value
	if decision := decide(lowUtility); decision.Decision != "accept" ||
		len(decision.Warnings) != 1 || decision.Warnings[0] != "low_utility_warning_only" {
		t.Fatalf("utility warning became a rejection: %+v", decision)
	}
}

func TestTribunalScoresDirectGraphPaths(t *testing.T) {
	edges := []EdgeRecord{{SrcID: "support", DstID: "candidate", EdgeType: "verified_by"},
		{SrcID: "candidate", DstID: "other", EdgeType: "contradicts"},
		{SrcID: "unrelated", DstID: "other", EdgeType: "contradicts"}}
	score, err := ScoreMemoryEdges("candidate", edges)
	if err != nil {
		t.Fatal(err)
	}
	expected := math.Log1p(1) - 1.5*math.Log1p(1) + 0.4
	if score.VerifiedSupportPaths != 1 || score.ContradictionPaths != 1 ||
		!score.HardContradiction || math.Abs(score.Score-expected) > 1e-12 {
		t.Fatalf("direct graph score differs from Python contract: %+v", score)
	}
	weighted, err := ScoreMemoryEdgesWithWeights("candidate", edges, 0, 1, 1)
	if err != nil || math.Abs(weighted.Score-(expected+0.6)) > 1e-12 {
		t.Fatalf("custom proof weights differ from Python contract: %+v, %v", weighted, err)
	}
	normalized, err := ScoreMemoryEdges(" candidate ", edges)
	if err != nil || normalized.VerifiedSupportPaths != 1 {
		t.Fatalf("candidate identity was not normalized: %+v, %v", normalized, err)
	}
	if _, err := ScoreMemoryEdges("  ", edges); err == nil {
		t.Fatal("empty candidate identity was accepted")
	}
}
