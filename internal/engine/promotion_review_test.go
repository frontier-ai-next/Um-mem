package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestPromotionReviewRequiresFreshIndependentSemanticProof(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "The build uses a pinned toolchain",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	record, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	request := PromotionReviewRequest{VerificationSubjectRequest: VerificationSubjectRequest{
		MemoryID: record.BlockID, ExpectedSourceRevision: record.RevisionID}}
	subject, err := selected.VerificationSubject(ctx, request.VerificationSubjectRequest)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: subject, VerifierID: "reviewer-1",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The candidate matches its source evidence"}})
	if err != nil {
		t.Fatal(err)
	}
	request.Reports = []ledger.VerificationReport{report}
	review, err := selected.ValidatePromotion(ctx, request)
	if err != nil || review.Decision.Decision != "accept" ||
		review.Subject.RevisionID != record.RevisionID || review.SemanticReceipt == nil ||
		len(review.PromotionRefs) != 1 {
		t.Fatalf("verified candidate was rejected: %+v, %v", review, err)
	}
	missing := request
	missing.Reports = nil
	if _, err := selected.ValidatePromotion(ctx, missing); err == nil {
		t.Fatal("candidate without semantic report passed review")
	}
	forged := request
	forged.Reports = []ledger.VerificationReport{report}
	forged.Reports[0].LedgerHash = "forged-event-hash"
	if _, err := selected.ValidatePromotion(ctx, forged); err == nil {
		t.Fatal("forged report passed review")
	}
	fakeRef := request
	fakeRef.SourceEvidenceRefs = []memory.EvidenceRef{{RefType: "ledger_event", RefID: "missing-event",
		ProducedBy: "verifier", ProducerID: "reviewer-1"}}
	if _, err := selected.ValidatePromotion(ctx, fakeRef); err == nil {
		t.Fatal("fake source evidence passed review")
	}
	changed := request
	changed.TargetScope, changed.TargetScopeID = "project", "project-1"
	changed.Lineage.ProjectID = "project-1"
	if _, err := selected.ValidatePromotion(ctx, changed); err == nil ||
		!strings.Contains(err.Error(), "bound to this publication") {
		t.Fatalf("report was reused for a different target: %v", err)
	}
	content := "The build uses an unpinned toolchain"
	if _, err := selected.Amend(ctx, AmendRequest{MemoryID: record.BlockID,
		ExpectedRevision: record.RevisionID, Reason: "source changed", Content: &content}); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.VerificationSubject(ctx, request.VerificationSubjectRequest); err == nil {
		t.Fatal("stale candidate was issued a new verification subject")
	}
	if _, err := selected.ValidatePromotion(ctx, request); err == nil {
		t.Fatal("stale candidate passed publication review")
	}
}

func TestPromotionReviewBlocksDirectContradictionAndUngatedProcedure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "A procedure derived from a task",
		Kind: "procedure", RequestedBlockID: "stable_project_facts", SessionID: "session-1",
		Metadata: map[string]any{"procedure_applicability": "build tasks",
			"source_task_signature": "build", "source_episode_ids": []any{"episode-1"}}})
	if err != nil || !created.Saved {
		t.Fatalf("procedure setup failed: %+v, %v", created, err)
	}
	record, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	request := PromotionReviewRequest{VerificationSubjectRequest: VerificationSubjectRequest{
		MemoryID: record.BlockID, ExpectedSourceRevision: record.RevisionID}}
	subject, err := selected.VerificationSubject(ctx, request.VerificationSubjectRequest)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: subject, VerifierID: "reviewer-1",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The candidate has been reviewed"}})
	if err != nil {
		t.Fatal(err)
	}
	request.Reports = []ledger.VerificationReport{report}
	if _, err := selected.ValidatePromotion(ctx, request); err == nil ||
		!strings.Contains(err.Error(), "source episode") {
		t.Fatalf("procedure with a missing source episode passed review: %v", err)
	}
	other, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "A conflicting claim",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !other.Saved {
		t.Fatalf("contradiction setup failed: %+v, %v", other, err)
	}
	otherRecord, err := selected.ReadStored(ctx, other.RecordID)
	if err != nil || otherRecord == nil {
		t.Fatal(err)
	}
	otherRequest := PromotionReviewRequest{VerificationSubjectRequest: VerificationSubjectRequest{
		MemoryID: otherRecord.BlockID, ExpectedSourceRevision: otherRecord.RevisionID}}
	otherSubject, err := selected.VerificationSubject(ctx, otherRequest.VerificationSubjectRequest)
	if err != nil {
		t.Fatal(err)
	}
	otherReport, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: otherSubject, VerifierID: "reviewer-2",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The conflicting claim has been reviewed"}})
	if err != nil {
		t.Fatal(err)
	}
	otherRequest.Reports = []ledger.VerificationReport{otherReport}
	if _, err := selected.AddEdge(ctx, AddEdgeRequest{SrcID: other.RecordID, DstID: record.BlockID,
		EdgeType: "contradicts", Weight: json.Number("1.0"),
		EvidenceRefs: []memory.EvidenceRef{reportEvidenceRef(otherReport)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.ValidatePromotion(ctx, otherRequest); err == nil ||
		!strings.Contains(err.Error(), "hard_contradiction_path") {
		t.Fatalf("direct graph contradiction passed the tribunal: %v", err)
	}
}
