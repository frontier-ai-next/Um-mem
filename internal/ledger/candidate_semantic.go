package ledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

var candidateSemanticFields = []string{"subject", "semantic_subject_hash", "verifier_id",
	"verifier_model_version", "proposer_model_version", "verifier_prompt_version",
	"coverage", "preservation", "faithfulness", "instruction_safety", "reasons"}

type CandidateSemanticVerification struct {
	RepoRoot              string
	WorkspaceRoot         string
	Subject               memory.CandidateVerificationSubject
	VerifierID            string
	VerifierModelVersion  string
	ProposerModelVersion  string
	VerifierPromptVersion string
	Coverage              bool
	Preservation          bool
	Faithfulness          bool
	InstructionSafety     bool
	Reasons               []string
	ChangedFiles          []string
	ProducedAfterEventID  string
}

// AppendCandidateSemanticVerification writes verifier-owned semantic proof for
// one typed, prepublication candidate subject. The returned report still needs
// general ledger/freshness validation before its semantic result can be used.
func AppendCandidateSemanticVerification(request CandidateSemanticVerification) (VerificationReport, error) {
	if err := request.Subject.Validate(); err != nil {
		return VerificationReport{}, err
	}
	if request.VerifierPromptVersion == "" {
		request.VerifierPromptVersion = "candidate-review/1"
	}
	for _, identity := range []string{request.VerifierID, request.VerifierModelVersion,
		request.ProposerModelVersion, request.VerifierPromptVersion} {
		if identity == "" || identity != strings.TrimSpace(identity) {
			return VerificationReport{}, errors.New("candidate semantic verification requires exact verifier and model identities")
		}
	}
	if len(request.Reasons) == 0 {
		return VerificationReport{}, errors.New("candidate semantic verification requires reasons")
	}
	for _, reason := range request.Reasons {
		if strings.TrimSpace(reason) == "" {
			return VerificationReport{}, errors.New("candidate semantic verification requires nonempty reasons")
		}
	}
	subjectHash, err := HashValue(request.Subject)
	if err != nil {
		return VerificationReport{}, err
	}
	semantic := map[string]any{
		"subject": request.Subject, "semantic_subject_hash": subjectHash,
		"verifier_id":             request.VerifierID,
		"verifier_model_version":  request.VerifierModelVersion,
		"proposer_model_version":  request.ProposerModelVersion,
		"verifier_prompt_version": request.VerifierPromptVersion,
		"coverage":                request.Coverage, "preservation": request.Preservation,
		"faithfulness":       request.Faithfulness,
		"instruction_safety": request.InstructionSafety,
		"reasons":            request.Reasons,
	}
	reportHash, err := HashValue(semantic)
	if err != nil {
		return VerificationReport{}, err
	}
	passed := request.Coverage && request.Preservation && request.Faithfulness &&
		request.InstructionSafety && request.VerifierModelVersion != request.ProposerModelVersion
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
	result["passed"] = passed
	result["workspace_hash"] = workspaceHash
	result["diff_hash"] = diffHash
	result["produced_after_event_id"] = request.ProducedAfterEventID
	event, err := AppendWithResultHash(request.RepoRoot, request.Subject.WorkspaceID,
		"verifier", "candidate_semantic_verification", "candidate_semantic_verification",
		result, resultHash)
	if err != nil {
		return VerificationReport{}, err
	}
	return VerificationReport{ReportID: event.ID, ReportHash: reportHash,
		WorkspaceHash: workspaceHash, DiffHash: diffHash,
		ProducedAfterEventID: request.ProducedAfterEventID, VerifierID: request.VerifierID,
		Passed: passed, LedgerHash: event.Hash}, nil
}

// ValidateCandidateSemanticReport checks freshness and the verifier result
// against one exact, prepublication subject. The general report gate runs
// first, so callers cannot accidentally use a stale semantic result alone.
func (resolver *Resolver) ValidateCandidateSemanticReport(report VerificationReport,
	subject memory.CandidateVerificationSubject, workspaceRoot string,
	changedFiles []string, allowWorkspaceHashMismatch bool) (map[string]any, error) {
	if err := subject.Validate(); err != nil {
		return nil, err
	}
	issues, err := resolver.ValidateVerificationReport(report, workspaceRoot,
		changedFiles, allowWorkspaceHashMismatch)
	if err != nil {
		return nil, err
	}
	if len(issues) != 0 {
		return nil, errors.New("candidate semantic report failed general verification: " +
			strings.Join(issues, ", "))
	}
	encoded, err := json.Marshal(subject)
	if err != nil {
		return nil, err
	}
	var subjectMap map[string]any
	if err := json.Unmarshal(encoded, &subjectMap); err != nil {
		return nil, err
	}
	row := resolver.Row(report.ReportID)
	if row == nil || row["tool"] != "candidate_semantic_verification" || row["actor"] != "verifier" ||
		!report.Passed || report.LedgerHash == "" || row["event_hash"] != report.LedgerHash {
		return nil, errors.New("candidate semantic verification ledger event is unavailable")
	}
	result, ok := row["result"].(map[string]any)
	if !ok {
		return nil, errors.New("candidate semantic verification result is malformed")
	}
	semantic := make(map[string]any, len(candidateSemanticFields))
	for _, field := range candidateSemanticFields {
		semantic[field] = result[field]
	}
	semanticHash, err := HashValue(semantic)
	if err != nil {
		return nil, err
	}
	subjectHash, err := HashValue(subject)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(result["subject"], subjectMap) ||
		result["semantic_subject_hash"] != subjectHash ||
		report.ReportHash != semanticHash || result["verifier_id"] != report.VerifierID ||
		result["passed"] != true {
		return nil, errors.New("candidate semantic verification is not bound to this publication")
	}
	identities := []string{"verifier_id", "verifier_model_version",
		"proposer_model_version", "verifier_prompt_version"}
	for _, field := range identities {
		value, ok := result[field].(string)
		if !ok || value == "" || value != strings.TrimSpace(value) {
			return nil, errors.New("candidate semantic verification has an invalid model identity")
		}
	}
	if result["verifier_model_version"] == result["proposer_model_version"] {
		return nil, errors.New("candidate semantic verification reused the proposer model")
	}
	for _, field := range []string{"coverage", "preservation", "faithfulness", "instruction_safety"} {
		if result[field] != true {
			return nil, errors.New("candidate semantic verification did not pass every outcome")
		}
	}
	reasons, ok := result["reasons"].([]any)
	if !ok || len(reasons) == 0 {
		return nil, errors.New("candidate semantic verification requires reasons")
	}
	for _, item := range reasons {
		reason, ok := item.(string)
		if !ok || strings.TrimSpace(reason) == "" {
			return nil, errors.New("candidate semantic verification has an empty reason")
		}
	}
	return semantic, nil
}
