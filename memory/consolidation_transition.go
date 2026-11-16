package memory

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
)

type SourceCoverageDiff struct {
	BeforeSourceIDs []string `json:"before_source_ids"`
	AfterSourceIDs  []string `json:"after_source_ids"`
	AddedSourceIDs  []string `json:"added_source_ids"`
	LostSourceIDs   []string `json:"lost_source_ids"`
}

func NewSourceCoverageDiff(before, after []string) (SourceCoverageDiff, error) {
	result := SourceCoverageDiff{}
	var err error
	result.BeforeSourceIDs, err = sortedSignalIDs(before, true)
	if err != nil {
		return result, err
	}
	result.AfterSourceIDs, err = sortedSignalIDs(after, false)
	if err != nil {
		return result, err
	}
	beforeSet, afterSet := map[string]bool{}, map[string]bool{}
	for _, id := range result.BeforeSourceIDs {
		beforeSet[id] = true
	}
	for _, id := range result.AfterSourceIDs {
		afterSet[id] = true
		if !beforeSet[id] {
			result.AddedSourceIDs = append(result.AddedSourceIDs, id)
		}
	}
	for _, id := range result.BeforeSourceIDs {
		if !afterSet[id] {
			result.LostSourceIDs = append(result.LostSourceIDs, id)
		}
	}
	if result.AddedSourceIDs == nil {
		result.AddedSourceIDs = []string{}
	}
	if result.LostSourceIDs == nil {
		result.LostSourceIDs = []string{}
	}
	return result, nil
}

type ConsolidationTransition struct {
	TransitionID             string             `json:"transition_id"`
	FromStatus               string             `json:"from_status"`
	ToStatus                 string             `json:"to_status"`
	DecisionID               string             `json:"decision_id"`
	PreStateSnapshotSHA256   string             `json:"pre_state_snapshot_sha256"`
	NewEvidenceRefs          []EvidenceRef      `json:"new_evidence_refs"`
	ProposedOperations       []string           `json:"proposed_operations"`
	TouchedRecordIDs         []string           `json:"touched_record_ids"`
	TouchedViewIDs           []string           `json:"touched_view_ids"`
	PostStateCandidate       string             `json:"post_state_candidate"`
	PostStateCandidateSHA256 string             `json:"post_state_candidate_sha256"`
	SourceCoverageDiff       SourceCoverageDiff `json:"source_coverage_diff"`
	PolicyVersion            string             `json:"policy_version"`
	ModelVersion             string             `json:"model_version"`
	PromptVersion            string             `json:"prompt_version"`
	EvidenceRefs             []EvidenceRef      `json:"evidence_refs"`
}

type ConsolidationProposal struct {
	Decision            ConsolidationDecision
	SourceRecords       []MemoryRecord
	NewEvidenceRefs     []EvidenceRef
	ProposedOperations  []string
	TouchedViewIDs      []string
	PostStateCandidate  map[string]any
	SourceCoverageAfter []string
	PolicyVersion       string
	ModelVersion        string
	PromptVersion       string
}

type MemoryTransitionAssessment struct {
	AssessmentID             string             `json:"assessment_id"`
	TransitionID             string             `json:"transition_id"`
	DecisionID               string             `json:"decision_id"`
	PreStateSnapshotSHA256   string             `json:"pre_state_snapshot_sha256"`
	PostStateCandidateSHA256 string             `json:"post_state_candidate_sha256"`
	SourceCoverageDiff       SourceCoverageDiff `json:"source_coverage_diff"`
	PolicyVersion            string             `json:"policy_version"`
	ModelVersion             string             `json:"model_version"`
	PromptVersion            string             `json:"prompt_version"`
	Verdict                  string             `json:"verdict"`
	Reasons                  []string           `json:"reasons"`
	EvidenceRefs             []EvidenceRef      `json:"evidence_refs"`
	CanonicalMutationApplied bool               `json:"canonical_mutation_applied"`
	HostActionAuthorized     bool               `json:"host_action_authorized"`
}

func AssessConsolidationTransition(transition ConsolidationTransition,
	assessmentID, verdict string, reasons []string,
	evidenceRefs []EvidenceRef) (MemoryTransitionAssessment, error) {
	result := MemoryTransitionAssessment{}
	if err := transition.Validate(); err != nil {
		return result, err
	}
	assessmentID = strings.TrimSpace(assessmentID)
	if assessmentID == "" {
		return result, errors.New("assessment_id is required")
	}
	if verdict != "accept" && verdict != "reject" && verdict != "defer" {
		return result, errors.New("memory transition verdict is invalid")
	}
	if len(reasons) == 0 {
		return result, errors.New("memory transition assessment requires reasons")
	}
	normalizedReasons := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return result, errors.New("memory transition assessment requires nonempty reasons")
		}
		normalizedReasons = append(normalizedReasons, reason)
	}
	refs, err := uniqueEvidenceRefs(evidenceRefs, true)
	if err != nil {
		return result, err
	}
	result = MemoryTransitionAssessment{AssessmentID: assessmentID,
		TransitionID: transition.TransitionID, DecisionID: transition.DecisionID,
		PreStateSnapshotSHA256:   transition.PreStateSnapshotSHA256,
		PostStateCandidateSHA256: transition.PostStateCandidateSHA256,
		SourceCoverageDiff:       transition.SourceCoverageDiff,
		PolicyVersion:            transition.PolicyVersion, ModelVersion: transition.ModelVersion,
		PromptVersion: transition.PromptVersion, Verdict: verdict,
		Reasons: normalizedReasons, EvidenceRefs: refs}
	return result, nil
}

func validConsolidationHash(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

// Validate checks the immutable candidate and its hash before an assessment
// can rely on a transition received from another process or persisted store.
func (transition ConsolidationTransition) Validate() error {
	if transition.FromStatus != "proposed" || transition.ToStatus != "assessed" ||
		strings.TrimSpace(transition.TransitionID) == "" ||
		strings.TrimSpace(transition.DecisionID) == "" ||
		!validConsolidationHash(transition.PreStateSnapshotSHA256) ||
		!validConsolidationHash(transition.PostStateCandidateSHA256) ||
		strings.TrimSpace(transition.PolicyVersion) == "" ||
		strings.TrimSpace(transition.ModelVersion) == "" ||
		strings.TrimSpace(transition.PromptVersion) == "" {
		return errors.New("consolidation transition is incomplete or invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(transition.PostStateCandidate))
	decoder.UseNumber()
	var candidate any
	if err := decoder.Decode(&candidate); err != nil {
		return errors.New("post_state_candidate must be valid JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("post_state_candidate must contain exactly one JSON value")
	}
	canonical, err := jsoncompat.MarshalPython(candidate, false)
	if err != nil {
		return err
	}
	if consolidationHash(canonical) != transition.PostStateCandidateSHA256 {
		return errors.New("post_state_candidate_sha256 does not match the candidate")
	}
	if _, err := uniqueEvidenceRefs(transition.NewEvidenceRefs, true); err != nil {
		return err
	}
	if _, err := uniqueEvidenceRefs(transition.EvidenceRefs, false); err != nil {
		return err
	}
	if len(transition.ProposedOperations) == 0 || len(transition.TouchedRecordIDs) == 0 {
		return errors.New("consolidation transition requires operations and touched records")
	}
	coverage, err := NewSourceCoverageDiff(transition.SourceCoverageDiff.BeforeSourceIDs,
		transition.SourceCoverageDiff.AfterSourceIDs)
	if err != nil || !slices.Equal(coverage.BeforeSourceIDs, transition.TouchedRecordIDs) ||
		!slices.Equal(coverage.AddedSourceIDs, transition.SourceCoverageDiff.AddedSourceIDs) ||
		!slices.Equal(coverage.LostSourceIDs, transition.SourceCoverageDiff.LostSourceIDs) {
		return errors.New("consolidation transition source coverage is inconsistent")
	}
	return nil
}

func uniqueEvidenceRefs(values []EvidenceRef, required bool) ([]EvidenceRef, error) {
	if required && len(values) == 0 {
		return nil, errors.New("consolidation transition requires evidence refs")
	}
	result := make([]EvidenceRef, 0, len(values))
	seen := map[string]bool{}
	for _, ref := range values {
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		encoded, err := jsoncompat.MarshalPython(ref, false)
		if err != nil {
			return nil, err
		}
		if !seen[string(encoded)] {
			result = append(result, ref)
			seen[string(encoded)] = true
		}
	}
	return result, nil
}

// ProposeConsolidationTransition binds a proposal to exact canonical sources
// and immutable pre/post-state hashes. It neither writes records nor accepts
// a tribunal verdict; that boundary is handled by the later commit stage.
func ProposeConsolidationTransition(proposal ConsolidationProposal) (ConsolidationTransition, error) {
	result := ConsolidationTransition{}
	decision := proposal.Decision
	if decision.Status != "proposed" || decision.DecisionID == "" ||
		decision.WorkspaceID == "" || len(decision.SourceEpisodeIDs) == 0 {
		return result, errors.New("consolidation decision must be a complete proposal")
	}
	if len(proposal.SourceRecords) == 0 {
		return result, errors.New("source_records must contain canonical records")
	}
	byID := make(map[string]MemoryRecord, len(proposal.SourceRecords))
	for _, record := range proposal.SourceRecords {
		if record.BlockID == "" || record.CanonicalKey == "" || record.RevisionID == "" {
			return result, errors.New("source_records must be canonical records")
		}
		if record.WorkspaceID != decision.WorkspaceID {
			return result, errors.New("canonical source workspace must match the decision")
		}
		if _, exists := byID[record.BlockID]; exists {
			return result, errors.New("canonical source record IDs must be unique")
		}
		byID[record.BlockID] = record
	}
	sourceIDs := make([]string, 0, len(byID))
	for id := range byID {
		sourceIDs = append(sourceIDs, id)
	}
	slices.Sort(sourceIDs)
	if !slices.Equal(sourceIDs, decision.SourceEpisodeIDs) {
		return result, errors.New("canonical source episode set must match the decision")
	}
	newRefs, err := uniqueEvidenceRefs(proposal.NewEvidenceRefs, true)
	if err != nil {
		return result, err
	}
	operations, err := sortedSignalIDs(proposal.ProposedOperations, true)
	if err != nil {
		return result, err
	}
	views, err := sortedSignalIDs(proposal.TouchedViewIDs, false)
	if err != nil {
		return result, err
	}
	coverageAfter, err := sortedSignalIDs(proposal.SourceCoverageAfter, false)
	if err != nil {
		return result, err
	}
	policy, model, prompt := strings.TrimSpace(proposal.PolicyVersion),
		strings.TrimSpace(proposal.ModelVersion), strings.TrimSpace(proposal.PromptVersion)
	if policy == "" || model == "" || prompt == "" {
		return result, errors.New("consolidation proposal requires policy, model and prompt versions")
	}
	ordered := make([]MemoryRecord, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		ordered = append(ordered, byID[id])
	}
	preState, err := jsoncompat.MarshalPython(ordered, false)
	if err != nil {
		return result, err
	}
	if proposal.PostStateCandidate == nil {
		return result, errors.New("post_state_candidate must be a mapping")
	}
	postState, err := jsoncompat.MarshalPython(proposal.PostStateCandidate, false)
	if err != nil {
		return result, err
	}
	coverage, err := NewSourceCoverageDiff(sourceIDs, coverageAfter)
	if err != nil {
		return result, err
	}
	preHash, postHash := consolidationHash(preState), consolidationHash(postState)
	basis, err := jsoncompat.MarshalPython(map[string]any{
		"decision_id":                 decision.DecisionID,
		"pre_state_snapshot_sha256":   preHash,
		"post_state_candidate_sha256": postHash,
		"proposed_operations":         operations,
		"touched_record_ids":          sourceIDs,
		"touched_view_ids":            views,
		"source_coverage_after":       coverageAfter,
		"policy_version":              policy,
		"model_version":               model,
		"prompt_version":              prompt,
	}, false)
	if err != nil {
		return result, err
	}
	allRefs, err := uniqueEvidenceRefs(append(append([]EvidenceRef{}, decision.EvidenceRefs...), newRefs...), true)
	if err != nil {
		return result, err
	}
	result = ConsolidationTransition{
		TransitionID: "transition-" + consolidationHash(basis)[7:31],
		FromStatus:   "proposed", ToStatus: "assessed", DecisionID: decision.DecisionID,
		PreStateSnapshotSHA256: preHash, NewEvidenceRefs: newRefs,
		ProposedOperations: operations, TouchedRecordIDs: sourceIDs,
		TouchedViewIDs: views, PostStateCandidate: string(postState),
		PostStateCandidateSHA256: postHash, SourceCoverageDiff: coverage,
		PolicyVersion: policy, ModelVersion: model, PromptVersion: prompt,
		EvidenceRefs: allRefs,
	}
	if err := result.Validate(); err != nil {
		return ConsolidationTransition{}, fmt.Errorf("validate consolidation transition: %w", err)
	}
	return result, nil
}
