package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
)

// TransitionSemanticVerification is supplied by a verifier independent of the
// proposal. The tribunal checks its provenance and all three semantic axes.
type TransitionSemanticVerification struct {
	VerifierID              string        `json:"verifier_id"`
	VerifierModelVersion    string        `json:"verifier_model_version"`
	Coverage                bool          `json:"coverage"`
	Preservation            bool          `json:"preservation"`
	Faithfulness            bool          `json:"faithfulness"`
	IndependentFromProposer bool          `json:"independent_from_proposer"`
	Reasons                 []string      `json:"reasons"`
	EvidenceRefs            []EvidenceRef `json:"evidence_refs"`
}

func (verification TransitionSemanticVerification) Validate() error {
	if strings.TrimSpace(verification.VerifierID) == "" ||
		strings.TrimSpace(verification.VerifierModelVersion) == "" ||
		len(verification.Reasons) == 0 {
		return errors.New("semantic verification requires verifier identity, model and reasons")
	}
	if _, err := sortedSignalIDs(verification.Reasons, true); err != nil {
		return err
	}
	_, err := uniqueEvidenceRefs(verification.EvidenceRefs, true)
	return err
}

type TransitionInvariantCheck struct {
	CheckID string `json:"check_id"`
	Passed  bool   `json:"passed"`
	Reason  string `json:"reason"`
}

type ConsolidationTribunalReport struct {
	ReportID                 string                         `json:"report_id"`
	TransitionID             string                         `json:"transition_id"`
	Verdict                  string                         `json:"verdict"`
	DeterministicChecks      []TransitionInvariantCheck     `json:"deterministic_checks"`
	SemanticVerification     TransitionSemanticVerification `json:"semantic_verification"`
	ProtectedLiterals        []string                       `json:"protected_literals"`
	Assessment               MemoryTransitionAssessment     `json:"assessment"`
	CanonicalMutationApplied bool                           `json:"canonical_mutation_applied"`
	HostActionAuthorized     bool                           `json:"host_action_authorized"`
}

func (report ConsolidationTribunalReport) FailedCheckIDs() []string {
	failed := []string{}
	for _, check := range report.DeterministicChecks {
		if !check.Passed {
			failed = append(failed, check.CheckID)
		}
	}
	return failed
}

func tribunalTextSet(value any) map[string]bool {
	result := map[string]bool{}
	values, ok := value.([]any)
	if !ok {
		return result
	}
	for _, item := range values {
		text := strings.TrimSpace(fmt.Sprint(item))
		if text != "" {
			result[text] = true
		}
	}
	return result
}

func tribunalMetadataTexts(record MemoryRecord, key string) []string {
	values, ok := record.Metadata[key].([]any)
	if !ok {
		if stringsValue, yes := record.Metadata[key].([]string); yes {
			values = make([]any, len(stringsValue))
			for i, value := range stringsValue {
				values[i] = value
			}
		} else {
			return nil
		}
	}
	result := []string{}
	for _, item := range values {
		text := strings.TrimSpace(fmt.Sprint(item))
		if text != "" {
			result = append(result, text)
		}
	}
	return result
}

func tribunalStrings(value any) []string {
	switch item := value.(type) {
	case string:
		return []string{item}
	case map[string]any:
		result := []string{}
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			result = append(result, tribunalStrings(item[key])...)
		}
		return result
	case []any:
		result := []string{}
		for _, nested := range item {
			result = append(result, tribunalStrings(nested)...)
		}
		return result
	default:
		return nil
	}
}

func tribunalSubset(left, right map[string]bool) bool {
	for id := range left {
		if !right[id] {
			return false
		}
	}
	return true
}

func tribunalEqualSet(left, right map[string]bool) bool {
	return len(left) == len(right) && tribunalSubset(left, right)
}

func tribunalSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func tribunalObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func tribunalItems(value any) []any {
	items, _ := value.([]any)
	return items
}

func tribunalTruthy(value any) bool {
	switch item := value.(type) {
	case nil:
		return false
	case bool:
		return item
	case string:
		return item != ""
	case []any:
		return len(item) != 0
	case map[string]any:
		return len(item) != 0
	case json.Number:
		return item != "0" && item != "0.0"
	default:
		return true
	}
}

func tribunalScopeRank(scope string) int {
	switch scope {
	case "subtask":
		return 0
	case "task":
		return 1
	case "session":
		return 2
	case "workspace":
		return 3
	case "project":
		return 4
	case "user_global":
		return 5
	default:
		return 99
	}
}

// AssessConsolidationTribunal repeats deterministic checks over immutable
// canonical sources and candidate JSON. Its result remains proposal-only.
func AssessConsolidationTribunal(transition ConsolidationTransition, decision ConsolidationDecision,
	sourceRecords []MemoryRecord, semantic TransitionSemanticVerification,
	assessmentID string) (ConsolidationTribunalReport, error) {
	report := ConsolidationTribunalReport{}
	if err := transition.Validate(); err != nil {
		return report, err
	}
	if transition.DecisionID != decision.DecisionID || decision.DecisionID == "" {
		return report, errors.New("transition decision identity mismatch")
	}
	if err := semantic.Validate(); err != nil {
		return report, err
	}
	if len(sourceRecords) == 0 {
		return report, errors.New("source_records are required")
	}
	records := make(map[string]MemoryRecord, len(sourceRecords))
	for _, record := range sourceRecords {
		if record.BlockID == "" || record.CanonicalKey == "" || record.RevisionID == "" {
			return report, errors.New("source_records must contain canonical records")
		}
		if _, exists := records[record.BlockID]; exists {
			return report, errors.New("source record IDs must be unique")
		}
		records[record.BlockID] = record
	}
	orderedIDs := make([]string, 0, len(records))
	for id := range records {
		orderedIDs = append(orderedIDs, id)
	}
	slices.Sort(orderedIDs)
	if !slices.Equal(orderedIDs, decision.SourceEpisodeIDs) {
		return report, errors.New("tribunal sources must match the decision")
	}
	ordered := make([]MemoryRecord, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		ordered = append(ordered, records[id])
	}
	preState, err := jsoncompat.MarshalPython(ordered, false)
	if err != nil {
		return report, err
	}
	if consolidationHash(preState) != transition.PreStateSnapshotSHA256 {
		return report, errors.New("tribunal canonical source snapshot does not match transition")
	}
	var candidate map[string]any
	decoder := json.NewDecoder(strings.NewReader(transition.PostStateCandidate))
	decoder.UseNumber()
	if err := decoder.Decode(&candidate); err != nil || candidate == nil {
		return report, errors.New("post-state candidate must be an object")
	}
	candidateStrings := strings.Join(tribunalStrings(candidate), "\n")
	sourceIDs := tribunalSet(orderedIDs)
	erasedIDs := map[string]bool{}
	for _, record := range sourceRecords {
		if record.Metadata["privacy_erased"] == true {
			erasedIDs[record.BlockID] = true
		}
	}
	activeIDs := map[string]bool{}
	for id := range sourceIDs {
		if !erasedIDs[id] {
			activeIDs[id] = true
		}
	}
	candidateIDs := tribunalTextSet(candidate["source_record_ids"])
	afterIDs := tribunalSet(transition.SourceCoverageDiff.AfterSourceIDs)
	checks := make([]TransitionInvariantCheck, 0, 11)
	add := func(id string, passed bool, ok, failure string) {
		reason := failure
		if passed {
			reason = ok
		}
		checks = append(checks, TransitionInvariantCheck{CheckID: id, Passed: passed, Reason: reason})
	}
	originOK := candidate["workspace_id"] == decision.WorkspaceID &&
		tribunalEqualSet(tribunalSet(transition.TouchedRecordIDs), sourceIDs) &&
		tribunalSubset(candidateIDs, sourceIDs)
	for _, record := range sourceRecords {
		originOK = originOK && record.WorkspaceID == decision.WorkspaceID
	}
	add("origin_integrity", originOK, "workspace and canonical source identities close", "workspace or canonical source identity does not close")
	authorityOK := candidate["authority"] == "advisory"
	for _, key := range []string{"claims", "materialized_views"} {
		for _, item := range tribunalItems(candidate[key]) {
			if object := tribunalObject(item); object != nil {
				value, exists := object["authority"]
				if exists && value != "advisory" {
					authorityOK = false
				}
			}
		}
	}
	add("no_authority_uplift", authorityOK, "candidate remains advisory", "candidate attempts authority uplift")
	maxRank := 0
	lifecycleOK := candidate["status"] == "candidate"
	for _, record := range sourceRecords {
		if rank := tribunalScopeRank(record.Scope); rank > maxRank {
			maxRank = rank
		}
		if record.Status == "quarantined" || record.Status == "retracted" {
			lifecycleOK = false
		}
		if record.ValidFrom != "" && candidate["valid_from"] != nil && candidate["valid_from"] != "" && candidate["valid_from"] != record.ValidFrom {
			lifecycleOK = false
		}
		if record.ValidTo != "" && candidate["valid_to"] != nil && candidate["valid_to"] != "" && candidate["valid_to"] != record.ValidTo {
			lifecycleOK = false
		}
	}
	lifecycleOK = lifecycleOK && tribunalScopeRank(fmt.Sprint(candidate["scope"])) <= maxRank
	add("scope_validity_lifecycle", lifecycleOK, "scope, validity, and lifecycle remain bounded", "scope widened or validity/lifecycle changed unsafely")
	coverageOK := len(transition.SourceCoverageDiff.AddedSourceIDs) == 0 && tribunalEqualSet(afterIDs, activeIDs) && tribunalEqualSet(candidateIDs, activeIDs)
	add("source_coverage", coverageOK, "every non-erased canonical source remains covered", "canonical source coverage was lost, added, or misreported")
	literals := map[string]bool{}
	for _, record := range sourceRecords {
		if !erasedIDs[record.BlockID] {
			for _, literal := range tribunalMetadataTexts(record, "exact_literals") {
				literals[literal] = true
			}
		}
	}
	protected := make([]string, 0, len(literals))
	rareOK := true
	for literal := range literals {
		protected = append(protected, literal)
		if !strings.Contains(candidateStrings, literal) {
			rareOK = false
		}
	}
	slices.Sort(protected)
	add("rare_exact_literals", rareOK, "rare and exact literals survive the candidate", "a rare or exact literal is missing or distorted")
	corrections, contradictions := map[string]bool{}, map[string]bool{}
	for _, record := range sourceRecords {
		for _, id := range tribunalMetadataTexts(record, "correction_of") {
			corrections[id] = true
		}
		for _, id := range tribunalMetadataTexts(record, "contradiction_of") {
			contradictions[id] = true
		}
	}
	lineageOK := tribunalSubset(corrections, tribunalTextSet(candidate["correction_ids"])) && tribunalSubset(contradictions, tribunalTextSet(candidate["contradiction_ids"]))
	add("contradiction_correction_closure", lineageOK, "correction and contradiction relations remain explicit", "correction or contradiction lineage was silently dropped")
	erasedLiterals := []string{}
	for _, record := range sourceRecords {
		if erasedIDs[record.BlockID] {
			erasedLiterals = append(erasedLiterals, record.Content)
			erasedLiterals = append(erasedLiterals, tribunalMetadataTexts(record, "exact_literals")...)
		}
	}
	privacyOK := true
	for id := range erasedIDs {
		if candidateIDs[id] || afterIDs[id] {
			privacyOK = false
		}
	}
	for _, literal := range erasedLiterals {
		if literal != "" && strings.Contains(candidateStrings, literal) {
			privacyOK = false
		}
	}
	add("privacy_erase_closure", privacyOK, "erased sources and literals are absent", "erased source identity or content leaked into the candidate")
	views := map[string]map[string]any{}
	for _, item := range tribunalItems(candidate["materialized_views"]) {
		if view := tribunalObject(item); view != nil && view["view_id"] != nil && view["view_id"] != "" {
			views[fmt.Sprint(view["view_id"])] = view
		}
	}
	viewsOK := true
	for _, id := range transition.TouchedViewIDs {
		if _, found := views[id]; !found {
			viewsOK = false
		}
	}
	for _, view := range views {
		ids := tribunalTextSet(view["source_record_ids"])
		if len(ids) == 0 || !tribunalSubset(ids, activeIDs) {
			viewsOK = false
		}
	}
	add("affected_materialized_views", viewsOK, "every affected view has bounded transitive sources", "an affected view is missing or has invalid source coverage")
	continuationOK := candidate["continuation_applicability"] == decision.ExpectedApplicability
	add("continuation_applicability", continuationOK, "continuation applicability matches the decision", "continuation applicability widened or drifted")
	procedureOK := true
	for _, operation := range transition.ProposedOperations {
		if strings.Contains(operation, "procedure") {
			procedure := tribunalObject(candidate["procedure"])
			if procedure == nil || procedure["applicability"] != decision.ExpectedApplicability {
				procedureOK = false
			}
			for _, field := range []string{"prerequisites", "steps", "applicability", "contraindications", "evidence_refs", "outcomes"} {
				if !tribunalTruthy(procedure[field]) {
					procedureOK = false
				}
			}
			break
		}
	}
	add("procedure_applicability", procedureOK, "procedure contract is closed and applicability is bounded", "procedure contract or applicability is incomplete")
	semanticOK := semantic.IndependentFromProposer && semantic.VerifierModelVersion != transition.ModelVersion && semantic.Coverage && semantic.Preservation && semantic.Faithfulness
	add("independent_semantic_verification", semanticOK, "independent verifier confirms coverage, preservation, and faithfulness", "semantic verifier is not independent or an assessment axis failed")
	verdict := "accept"
	reasons := make([]string, 0, len(checks))
	for _, check := range checks {
		if !check.Passed {
			verdict = "reject"
		}
		reasons = append(reasons, check.Reason)
	}
	refs := append(append([]EvidenceRef{}, transition.EvidenceRefs...), semantic.EvidenceRefs...)
	assessment, err := AssessConsolidationTransition(transition, assessmentID, verdict, reasons, refs)
	if err != nil {
		return report, err
	}
	return ConsolidationTribunalReport{ReportID: "tribunal-" + transition.TransitionID,
		TransitionID: transition.TransitionID, Verdict: verdict, DeterministicChecks: checks,
		SemanticVerification: semantic, ProtectedLiterals: protected, Assessment: assessment}, nil
}
