package ledger

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestCandidateSemanticReportBindsExactPublicationSubject(t *testing.T) {
	root := t.TempDir()
	subject := memory.CandidateVerificationSubject{WorkspaceID: "alpha", ProfileID: "team",
		MemoryID: "candidate-1", RevisionID: "revision-1",
		PayloadHash: "candidate-payload-hash", SourceEvidenceHash: "source-hash",
		TargetBlockID: "stable_project_facts", Authority: "advisory",
		Scope: "workspace", ScopeID: "alpha"}
	request := CandidateSemanticVerification{RepoRoot: root, Subject: subject,
		VerifierID: "verifier-1", VerifierModelVersion: "review-model",
		ProposerModelVersion: "author-model", Coverage: true, Preservation: true,
		Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"the exact claim is supported"}}
	report, err := AppendCandidateSemanticVerification(request)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := OpenResolver(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	issues, err := resolver.ValidateVerificationReport(report, root, nil, false)
	if err != nil || len(issues) != 0 {
		t.Fatalf("typed semantic report failed general proof: %v, %v", issues, err)
	}
	validated, err := resolver.ValidateCandidateSemanticReport(report, subject, root, nil, false)
	if err != nil || validated["verifier_model_version"] != "review-model" {
		t.Fatalf("exact semantic subject was rejected: %+v, %v", validated, err)
	}
	otherSubject := subject
	otherSubject.RevisionID = "newer-revision"
	if _, err := resolver.ValidateCandidateSemanticReport(report, otherSubject, root, nil, false); err == nil {
		t.Fatal("semantic report was reused for another revision")
	}
	if err := os.WriteFile(filepath.Join(root, "candidate.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ValidateCandidateSemanticReport(report, subject, root, nil, false); err == nil ||
		!strings.Contains(err.Error(), "proof_stale_rerun_required") {
		t.Fatalf("stale workspace passed semantic gate: %v", err)
	}
	if report.ReportHash != "9f1b3baa5459f55e362861172088f923e4b98a6d8425fcea4d5c9c14fff338cb" {
		t.Fatalf("candidate report hash changed: %s", report.ReportHash)
	}
}

func TestCandidateSemanticAppendRejectsInvalidSubjectAndFailedOutcome(t *testing.T) {
	root := t.TempDir()
	request := CandidateSemanticVerification{RepoRoot: root,
		Subject:    memory.CandidateVerificationSubject{WorkspaceID: "alpha"},
		VerifierID: "verifier-1", VerifierModelVersion: "review-model",
		ProposerModelVersion: "author-model", Coverage: true, Preservation: true,
		Faithfulness: true, InstructionSafety: true, Reasons: []string{"supported"}}
	if _, err := AppendCandidateSemanticVerification(request); err == nil {
		t.Fatal("incomplete candidate subject was accepted")
	}
	request.Subject = memory.CandidateVerificationSubject{WorkspaceID: "alpha", ProfileID: "team",
		MemoryID: "candidate-1", RevisionID: "revision-1", PayloadHash: "payload",
		SourceEvidenceHash: "evidence", TargetBlockID: "stable_project_facts",
		Authority: "advisory", Scope: "workspace", ScopeID: "alpha"}
	request.VerifierModelVersion = "author-model"
	report, err := AppendCandidateSemanticVerification(request)
	if err != nil || report.Passed {
		t.Fatalf("same-model review was accepted: %+v, %v", report, err)
	}
	resolver, err := OpenResolver(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	issues, err := resolver.ValidateVerificationReport(report, root, nil, false)
	if err != nil || !slices.Contains(issues, "verification_report_not_passed") {
		t.Fatalf("failed review escaped general gate: %v, %v", issues, err)
	}
	request.VerifierModelVersion = "review-model"
	request.Reasons = []string{"  "}
	if _, err := AppendCandidateSemanticVerification(request); err == nil {
		t.Fatal("empty semantic reasons were accepted")
	}
	path, err := Path(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Clean(path))
	if err != nil || len(contents) == 0 {
		t.Fatalf("failed review did not leave an auditable ledger event: %v", err)
	}
}
