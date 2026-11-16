package memory

import (
	"errors"
	"math"
	"strings"
)

type TribunalRule struct {
	Status string `json:"status"`
}

type TribunalInput struct {
	CandidateID       string
	Rules             []TribunalRule
	SourceEvidence    []EvidenceRef
	Actor             *string
	SourceBackend     string
	WorkspaceID       string
	TargetWorkspaceID string
	GraphEdges        []EdgeRecord
	UtilityValue      *float64
}

type TribunalCheck struct {
	Name     string         `json:"name"`
	Passed   bool           `json:"passed"`
	Severity string         `json:"severity"`
	Reason   string         `json:"reason"`
	Metadata map[string]any `json:"metadata"`
}

type TribunalDecision struct {
	CandidateID string          `json:"candidate_id"`
	Decision    string          `json:"decision"`
	Reason      string          `json:"reason"`
	Checks      []TribunalCheck `json:"checks"`
	Warnings    []string        `json:"warnings"`
	ReportPath  string          `json:"report_path"`
}

type MemoryProofScore struct {
	CandidateID          string         `json:"candidate_id"`
	Score                float64        `json:"score"`
	VerifiedSupportPaths int            `json:"verified_support_paths"`
	ContradictionPaths   int            `json:"contradiction_paths"`
	HardContradiction    bool           `json:"hard_contradiction"`
	Details              map[string]any `json:"details"`
}

// ScoreMemoryEdges uses the Python tribunal's default weights.
func ScoreMemoryEdges(candidateID string, edges []EdgeRecord) (MemoryProofScore, error) {
	return ScoreMemoryEdgesWithWeights(candidateID, edges, 0.5, 0, 0)
}

// ScoreMemoryEdgesWithWeights considers direct evidence paths only. Any direct
// contradicts edge is a hard promotion barrier regardless of the score.
func ScoreMemoryEdgesWithWeights(candidateID string, edges []EdgeRecord,
	trustWeight, recencyWeight, outcomeCorrelation float64) (MemoryProofScore, error) {
	candidateID = strings.TrimSpace(candidateID)
	if candidateID == "" {
		return MemoryProofScore{}, errors.New("candidate_id is required")
	}
	support := make([][]string, 0)
	contradictions := make([][]string, 0)
	for _, edge := range edges {
		if edge.DstID == candidateID && (edge.EdgeType == "supports" || edge.EdgeType == "verified_by") {
			support = append(support, []string{edge.SrcID, edge.DstID})
		}
		if (edge.SrcID == candidateID || edge.DstID == candidateID) && edge.EdgeType == "contradicts" {
			contradictions = append(contradictions, []string{edge.SrcID, edge.DstID})
		}
	}
	return MemoryProofScore{CandidateID: candidateID,
		Score: math.Log1p(float64(len(support))) - 1.5*math.Log1p(float64(len(contradictions))) +
			0.3*recencyWeight + 0.7*outcomeCorrelation + 0.8*trustWeight,
		VerifiedSupportPaths: len(support), ContradictionPaths: len(contradictions),
		HardContradiction: len(contradictions) > 0,
		Details:           map[string]any{"support_paths": support, "contradiction_paths": contradictions}}, nil
}

func tribunalCheck(name string, passed bool, severity, reason string, metadata map[string]any) TribunalCheck {
	if metadata == nil {
		metadata = map[string]any{}
	}
	return TribunalCheck{Name: name, Passed: passed, Severity: severity,
		Reason: reason, Metadata: metadata}
}

// RunTribunal preserves the deterministic Python check order and verdict
// priority. All six checks run, so the decision retains a complete audit trail.
func RunTribunal(input TribunalInput) (TribunalDecision, error) {
	checks := make([]TribunalCheck, 0, 6)
	actor := "UM-MEM"
	if input.Actor != nil {
		actor = *input.Actor
	}
	typedCount := 0
	for _, ref := range input.SourceEvidence {
		if ref.RefType != "" && ref.RefID != "" && ref.ProducedBy != "" {
			typedCount++
		}
	}
	if typedCount == 0 {
		checks = append(checks, tribunalCheck("EvidenceCheck", false, "blocking", "missing_typed_evidence", nil))
	} else {
		checks = append(checks, tribunalCheck("EvidenceCheck", true, "info", "typed_evidence_present",
			map[string]any{"count": typedCount}))
	}
	if (input.SourceBackend == "hindsight" || input.SourceBackend == "mnemosyne") &&
		actor == input.SourceBackend {
		checks = append(checks, tribunalCheck("TrustCheck", false, "blocking", "passive_backend_cannot_self_promote", nil))
	} else if actor == "supervisor" || actor == "verifier" || actor == "harness" ||
		actor == "watcher" || actor == "UM-MEM" {
		checks = append(checks, tribunalCheck("TrustCheck", true, "info", "trusted_actor", nil))
	} else {
		checks = append(checks, tribunalCheck("TrustCheck", false, "blocking", "untrusted_actor", nil))
	}
	if input.TargetWorkspaceID != "" && input.WorkspaceID != "" &&
		input.TargetWorkspaceID != input.WorkspaceID {
		checks = append(checks, tribunalCheck("ScopeCheck", false, "blocking", "cross_workspace_candidate_blocked", nil))
	} else {
		checks = append(checks, tribunalCheck("ScopeCheck", true, "info", "scope_ok", nil))
	}
	lifecycle := tribunalCheck("LifecycleCheck", true, "info", "lifecycle_ok", nil)
	for _, rule := range input.Rules {
		if rule.Status == "deprecated" || rule.Status == "retracted" || rule.Status == "superseded" {
			lifecycle = tribunalCheck("LifecycleCheck", false, "blocking", "status_"+rule.Status, nil)
			break
		}
	}
	checks = append(checks, lifecycle)
	if len(input.GraphEdges) == 0 {
		checks = append(checks, tribunalCheck("ContradictionCheck", true, "info", "graph_unavailable", nil))
	} else {
		score, err := ScoreMemoryEdges(input.CandidateID, input.GraphEdges)
		if err != nil {
			return TribunalDecision{}, err
		}
		if score.HardContradiction {
			checks = append(checks, tribunalCheck("ContradictionCheck", false, "blocking", "hard_contradiction_path",
				map[string]any{"proof_score": score.Score}))
		} else {
			checks = append(checks, tribunalCheck("ContradictionCheck", true, "info", "no_contradiction",
				map[string]any{"proof_score": score.Score}))
		}
	}
	if input.UtilityValue != nil && *input.UtilityValue < -0.5 {
		checks = append(checks, tribunalCheck("UtilityCheck", true, "warning", "low_utility_warning_only", nil))
	} else {
		checks = append(checks, tribunalCheck("UtilityCheck", true, "info", "utility_ok", nil))
	}
	decision := TribunalDecision{CandidateID: input.CandidateID, Decision: "accept",
		Reason: "accepted", Checks: checks, Warnings: []string{}}
	for _, check := range checks {
		if check.Severity == "warning" {
			decision.Warnings = append(decision.Warnings, check.Reason)
		}
	}
	for _, check := range checks {
		if !check.Passed && check.Severity == "quarantine" {
			decision.Decision, decision.Reason = "quarantine", check.Reason
			return decision, nil
		}
	}
	for _, check := range checks {
		if !check.Passed && (check.Severity == "blocking" || check.Severity == "error") {
			decision.Decision, decision.Reason = "reject", check.Reason
			if check.Reason == "missing_typed_evidence" {
				decision.Decision = "needs_more_evidence"
			}
			return decision, nil
		}
	}
	return decision, nil
}
