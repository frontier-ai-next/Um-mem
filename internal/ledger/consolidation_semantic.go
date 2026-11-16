package ledger

import (
	"errors"
	"slices"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type ConsolidationSemanticSubject struct {
	SourceIDs        []string `json:"source_ids"`
	Content          string   `json:"content"`
	Title            string   `json:"title"`
	RequestedBlockID string   `json:"requested_block_id"`
}

func (subject ConsolidationSemanticSubject) normalized() (ConsolidationSemanticSubject, error) {
	result := subject
	result.Content = strings.TrimSpace(result.Content)
	result.Title = strings.TrimSpace(result.Title)
	result.RequestedBlockID = strings.TrimSpace(result.RequestedBlockID)
	if result.Content == "" || result.Title == "" || result.RequestedBlockID == "" {
		return result, errors.New("semantic verification requires a complete proposal subject")
	}
	result.SourceIDs = make([]string, 0, len(subject.SourceIDs))
	seen := map[string]bool{}
	for _, raw := range subject.SourceIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			return result, errors.New("semantic verification requires exact source IDs")
		}
		if !seen[id] {
			result.SourceIDs = append(result.SourceIDs, id)
			seen[id] = true
		}
	}
	slices.Sort(result.SourceIDs)
	if len(result.SourceIDs) < 2 {
		return result, errors.New("semantic verification requires at least two source IDs")
	}
	return result, nil
}

func (subject ConsolidationSemanticSubject) Hash() (string, error) {
	normalized, err := subject.normalized()
	if err != nil {
		return "", err
	}
	return HashValue(normalized)
}

type ConsolidationSemanticVerification struct {
	RepoRoot             string
	WorkspaceRoot        string
	WorkspaceID          string
	Subject              ConsolidationSemanticSubject
	VerifierID           string
	VerifierModelVersion string
	ProposerModelVersion string
	Coverage             bool
	Preservation         bool
	Faithfulness         bool
	Reasons              []string
	ChangedFiles         []string
	ProducedAfterEventID string
}

func consolidationSemanticFields(subjectHash, verifierID, verifierModel, proposerModel string,
	coverage, preservation, faithfulness bool, reasons []string) map[string]any {
	return map[string]any{"semantic_subject_hash": subjectHash,
		"verifier_id": verifierID, "verifier_model_version": verifierModel,
		"proposer_model_version": proposerModel, "coverage": coverage,
		"preservation": preservation, "faithfulness": faithfulness, "reasons": reasons}
}

// AppendConsolidationSemanticVerification persists one verifier-owned report
// bound to the exact proposed consolidation subject.
func AppendConsolidationSemanticVerification(request ConsolidationSemanticVerification) (VerificationReport, error) {
	subject, err := request.Subject.normalized()
	if err != nil {
		return VerificationReport{}, err
	}
	verifierID := strings.TrimSpace(request.VerifierID)
	verifierModel := strings.TrimSpace(request.VerifierModelVersion)
	proposerModel := strings.TrimSpace(request.ProposerModelVersion)
	if verifierID == "" || verifierModel == "" || proposerModel == "" || strings.TrimSpace(request.WorkspaceID) == "" {
		return VerificationReport{}, errors.New("semantic verification requires verifier and model identities")
	}
	if len(request.Reasons) == 0 {
		return VerificationReport{}, errors.New("semantic verification requires non-empty reasons")
	}
	reasons := make([]string, len(request.Reasons))
	for i, reason := range request.Reasons {
		reasons[i] = strings.TrimSpace(reason)
		if reasons[i] == "" {
			return VerificationReport{}, errors.New("semantic verification requires non-empty reasons")
		}
	}
	subjectHash, err := subject.Hash()
	if err != nil {
		return VerificationReport{}, err
	}
	semantic := consolidationSemanticFields(subjectHash, verifierID, verifierModel, proposerModel,
		request.Coverage, request.Preservation, request.Faithfulness, reasons)
	reportHash, err := HashValue(semantic)
	if err != nil {
		return VerificationReport{}, err
	}
	passed := request.Coverage && request.Preservation && request.Faithfulness && verifierModel != proposerModel
	workspaceRoot := request.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = request.RepoRoot
	}
	workspaceHash, err := WorkspaceHash(workspaceRoot)
	if err != nil {
		return VerificationReport{}, err
	}
	diffHash, err := DiffHash(workspaceRoot, request.ChangedFiles)
	if err != nil {
		return VerificationReport{}, err
	}
	resultHash, err := HashValue(map[string]any{"report_hash": reportHash, "passed": passed,
		"workspace_hash": workspaceHash, "diff_hash": diffHash})
	if err != nil {
		return VerificationReport{}, err
	}
	result := make(map[string]any, len(semantic)+4)
	for key, value := range semantic {
		result[key] = value
	}
	result["passed"], result["workspace_hash"], result["diff_hash"] = passed, workspaceHash, diffHash
	result["produced_after_event_id"] = request.ProducedAfterEventID
	event, err := AppendWithResultHash(request.RepoRoot, request.WorkspaceID,
		"verifier", "consolidation_semantic_verification", "consolidation_semantic_verification",
		result, resultHash)
	if err != nil {
		return VerificationReport{}, err
	}
	return VerificationReport{ReportID: event.ID, ReportHash: reportHash,
		WorkspaceHash: workspaceHash, DiffHash: diffHash,
		ProducedAfterEventID: request.ProducedAfterEventID, VerifierID: verifierID,
		Passed: passed, LedgerHash: event.Hash}, nil
}

// ValidateConsolidationSemanticReport checks the ledger, workspace freshness,
// proposal identity and all semantic axes before providing tribunal input.
func (resolver *Resolver) ValidateConsolidationSemanticReport(report VerificationReport,
	subject ConsolidationSemanticSubject, proposerModel, workspaceRoot string,
	changedFiles []string) (memory.TransitionSemanticVerification, error) {
	result := memory.TransitionSemanticVerification{}
	begin, err := subject.normalized()
	if err != nil {
		return result, err
	}
	issues, err := resolver.ValidateVerificationReport(report, workspaceRoot, changedFiles, false)
	if err != nil {
		return result, err
	}
	if len(issues) != 0 {
		return result, errors.New("consolidation semantic report failed general verification: " + strings.Join(issues, ", "))
	}
	row := resolver.Row(report.ReportID)
	if row == nil || row["actor"] != "verifier" || row["tool"] != "consolidation_semantic_verification" ||
		row["event_hash"] != report.LedgerHash || !report.Passed {
		return result, errors.New("consolidation semantic verification ledger event is unavailable")
	}
	semantic, ok := row["result"].(map[string]any)
	if !ok {
		return result, errors.New("consolidation semantic verification result is malformed")
	}
	subjectHash, err := begin.Hash()
	if err != nil {
		return result, err
	}
	verifierID, _ := semantic["verifier_id"].(string)
	verifierModel, _ := semantic["verifier_model_version"].(string)
	proposer, _ := semantic["proposer_model_version"].(string)
	if strings.TrimSpace(verifierID) == "" || strings.TrimSpace(verifierModel) == "" ||
		verifierID != report.VerifierID || proposer != proposerModel || verifierModel == proposer ||
		semantic["semantic_subject_hash"] != subjectHash || semantic["passed"] != true {
		return result, errors.New("semantic verification report is not bound to this accepted proposal")
	}
	coverage, okCoverage := semantic["coverage"].(bool)
	preservation, okPreservation := semantic["preservation"].(bool)
	faithfulness, okFaithfulness := semantic["faithfulness"].(bool)
	if !okCoverage || !okPreservation || !okFaithfulness || !coverage || !preservation || !faithfulness {
		return result, errors.New("semantic verification report outcomes must all pass")
	}
	rawReasons, ok := semantic["reasons"].([]any)
	if !ok || len(rawReasons) == 0 {
		return result, errors.New("semantic verification report reasons must be typed")
	}
	reasons := make([]string, len(rawReasons))
	for i, raw := range rawReasons {
		reason, ok := raw.(string)
		if !ok || strings.TrimSpace(reason) == "" {
			return result, errors.New("semantic verification report reasons must be non-empty")
		}
		reasons[i] = strings.TrimSpace(reason)
	}
	semanticFields := consolidationSemanticFields(subjectHash, verifierID, verifierModel, proposer,
		coverage, preservation, faithfulness, reasons)
	expectedHash, err := HashValue(semanticFields)
	if err != nil {
		return result, err
	}
	if report.ReportHash != expectedHash {
		return result, errors.New("semantic verification report hash does not match ledger result")
	}
	hash := report.LedgerHash
	ref := memory.EvidenceRef{RefType: "verification_report", RefID: report.ReportID,
		Hash: &hash, ProducedBy: "verifier", ProducerID: report.VerifierID}
	if report.ProducedAfterEventID != "" {
		ref.CreatedAfterEvent = &report.ProducedAfterEventID
	}
	result = memory.TransitionSemanticVerification{VerifierID: verifierID,
		VerifierModelVersion: verifierModel, Coverage: coverage, Preservation: preservation,
		Faithfulness: faithfulness, IndependentFromProposer: true, Reasons: reasons,
		EvidenceRefs: []memory.EvidenceRef{ref}}
	return result, result.Validate()
}
