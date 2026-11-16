package engine

import (
	"context"
	"errors"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/ledger"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/receipts"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type interruptedPublicationProjector struct{}

func (interruptedPublicationProjector) BackendID() string                       { return "mnemosyne" }
func (interruptedPublicationProjector) RequiresNative(memory.MemoryRecord) bool { return true }
func (interruptedPublicationProjector) Project(context.Context, memory.MemoryRecord,
	string, int64) (store.ProjectionResult, error) {
	return store.ProjectionResult{}, errors.New("simulated projection interruption")
}

func TestPromoteDirectCandidateCommitsAtomicReceiptAndReplays(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "A reviewed project fact",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	source, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	request := PromoteRequest{PromotionReviewRequest: PromotionReviewRequest{
		VerificationSubjectRequest: VerificationSubjectRequest{MemoryID: source.BlockID,
			ExpectedSourceRevision: source.RevisionID}}, Reason: "independent review passed",
		OperationID: "promotion-1"}
	subject, err := selected.VerificationSubject(ctx, request.VerificationSubjectRequest)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: subject, VerifierID: "reviewer-1",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The source claim is supported"}})
	if err != nil {
		t.Fatal(err)
	}
	request.Reports = []ledger.VerificationReport{report}
	first, err := selected.Promote(ctx, request)
	if err != nil || first["saved"] != true || first["replayed"] == true {
		t.Fatalf("publication failed: %+v, %v", first, err)
	}
	acceptedID, _ := first["block_id"].(string)
	accepted, err := selected.ReadStored(ctx, acceptedID)
	if err != nil || accepted == nil || accepted.Status != "accepted" ||
		accepted.RevisionOf != source.BlockID || accepted.Content != source.Content {
		t.Fatalf("accepted successor was not retained: %+v, %v", accepted, err)
	}
	receiptID, _ := first["promotion_receipt_id"].(string)
	committed, err := selected.index.PublicationReceipt(ctx, receiptID)
	if err != nil || committed == nil || committed.RecordID != acceptedID {
		t.Fatalf("canonical publication receipt is unavailable: %+v, %v", committed, err)
	}
	replayed, err := selected.Promote(ctx, request)
	if err != nil || replayed["replayed"] != true || replayed["block_id"] != acceptedID {
		t.Fatalf("operation retry did not recover the accepted revision: %+v, %v", replayed, err)
	}
	conflicting := request
	conflicting.Reason = "different approval"
	if _, err := selected.Promote(ctx, conflicting); err == nil {
		t.Fatal("same operation identity accepted different arguments")
	}
	newOperation := request
	newOperation.OperationID = "promotion-2"
	if _, err := selected.Promote(ctx, newOperation); err == nil {
		t.Fatal("a stale candidate was published under a new operation")
	}
	wrongOwner, err := selected.CompensatePromotion(ctx, CompensatePromotionRequest{
		MemoryID: source.BlockID, ExpectedRevision: source.RevisionID,
		OperationID: "promotion-2", Reason: "cancel review"})
	if err != nil || wrongOwner.Outcome != "superseded" {
		t.Fatalf("another operation retracted the publication: %+v, %v", wrongOwner, err)
	}
	compensated, err := selected.CompensatePromotion(ctx, CompensatePromotionRequest{
		MemoryID: source.BlockID, ExpectedRevision: source.RevisionID,
		OperationID: "promotion-1", Reason: "cancel review"})
	if err != nil || compensated.Outcome != "retracted" || compensated.RecordID == acceptedID {
		t.Fatalf("owned publication was not compensated: %+v, %v", compensated, err)
	}
	replay, err := selected.CompensatePromotion(ctx, CompensatePromotionRequest{
		MemoryID: source.BlockID, ExpectedRevision: source.RevisionID,
		OperationID: "promotion-1", Reason: "cancel review"})
	if err != nil || replay.Outcome != "already_retracted" || replay.RecordID != compensated.RecordID {
		t.Fatalf("compensation replay changed the canonical head: %+v, %v", replay, err)
	}
}

func TestPromoteDirectCandidateRecoversPendingProjection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Review survives an interrupted projection",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	source, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	request := PromoteRequest{PromotionReviewRequest: PromotionReviewRequest{
		VerificationSubjectRequest: VerificationSubjectRequest{MemoryID: source.BlockID,
			ExpectedSourceRevision: source.RevisionID}}, Reason: "proof is complete",
		OperationID: "promotion-interrupted"}
	subject, err := selected.VerificationSubject(ctx, request.VerificationSubjectRequest)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: subject, VerifierID: "reviewer-1",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The exact candidate passed review"}})
	if err != nil {
		t.Fatal(err)
	}
	request.Reports = []ledger.VerificationReport{report}
	actualProjector := selected.projector
	selected.projector = interruptedPublicationProjector{}
	if _, err := selected.Promote(ctx, request); err == nil {
		t.Fatal("interrupted native projection was treated as committed")
	}
	rootID := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
			break
		}
	}
	if rootID == "" {
		t.Fatal("workspace profile root is unavailable")
	}
	if _, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Unrelated catalog branch",
		Synopsis: "A separate catalog change", ParentID: rootID,
		Reason: "verify staged publication recovery", IndexOnly: true}); err != nil {
		t.Fatalf("unrelated catalog edit failed: %v", err)
	}
	current, err := selected.index.IsCurrentRecord(ctx, *source)
	if err != nil || !current {
		t.Fatalf("candidate head moved before native verification: %v, %v", current, err)
	}
	selected.projector = actualProjector
	recovered, err := selected.Promote(ctx, request)
	if err != nil || recovered["saved"] != true {
		t.Fatalf("pending publication did not recover: %+v, %v", recovered, err)
	}
	accepted, err := selected.ReadStored(ctx, recovered["block_id"].(string))
	if err != nil || accepted == nil || accepted.Status != "accepted" {
		t.Fatalf("recovered publication has no accepted record: %+v, %v", accepted, err)
	}
}

func TestPromoteCandidateMaterializesVerifiedCopyWithProvenance(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "A verified reusable lesson",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	source, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	request := PromoteRequest{PromotionReviewRequest: PromotionReviewRequest{
		VerificationSubjectRequest: VerificationSubjectRequest{MemoryID: source.BlockID,
			ExpectedSourceRevision: source.RevisionID, TargetBlockID: "verified_lessons"}},
		Reason: "independent review passed", OperationID: "copy-promotion-1"}
	subject, err := selected.VerificationSubject(ctx, request.VerificationSubjectRequest)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: subject, VerifierID: "reviewer-1",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The candidate can be reused"}})
	if err != nil {
		t.Fatal(err)
	}
	request.Reports = []ledger.VerificationReport{report}
	first, err := selected.Promote(ctx, request)
	if err != nil || first["saved"] != true {
		t.Fatalf("materialized publication failed: %+v, %v", first, err)
	}
	copyID := first["block_id"].(string)
	copy, err := selected.ReadStored(ctx, copyID)
	if err != nil || copy == nil || copy.RevisionOf != "" || copy.Status != "accepted" ||
		copy.Content != source.Content || copy.BlockMembership == nil ||
		copy.BlockMembership.PrimaryBlockID != "verified_lessons" {
		t.Fatalf("published copy is invalid: %+v, %v", copy, err)
	}
	edges, err := selected.index.EdgesFor(ctx, copyID)
	if err != nil || len(edges) != 1 || edges[0].EdgeType != "derived_from" ||
		edges[0].DstID != source.BlockID {
		t.Fatalf("atomic copy provenance was not committed: %+v, %v", edges, err)
	}
	replayed, err := selected.Promote(ctx, request)
	if err != nil || replayed["replayed"] != true || replayed["block_id"] != copyID {
		t.Fatalf("copy retry did not recover the same root: %+v, %v", replayed, err)
	}
	newOperation := request
	newOperation.OperationID = "copy-promotion-2"
	second, err := selected.Promote(ctx, newOperation)
	if err != nil || second["replayed"] != true || second["block_id"] != copyID ||
		second["promotion_receipt_id"] == first["promotion_receipt_id"] {
		t.Fatalf("new operation did not acknowledge the existing verified copy: %+v, %v", second, err)
	}
	secondRetry, err := selected.Promote(ctx, newOperation)
	if err != nil || secondRetry["replayed"] != true ||
		secondRetry["promotion_receipt_id"] != second["promotion_receipt_id"] {
		t.Fatalf("detached receipt did not recover: %+v, %v", secondRetry, err)
	}
	edges, err = selected.index.EdgesFor(ctx, copyID)
	if err != nil || len(edges) != 1 {
		t.Fatalf("second operation changed copy provenance: %+v, %v", edges, err)
	}
	if _, err := selected.Forget(ctx, ForgetRequest{MemoryID: copyID,
		Reason: "withdraw verified copy"}); err != nil {
		t.Fatalf("published copy could not be withdrawn: %v", err)
	}
	withdrawnRetry := request
	withdrawnRetry.OperationID = "copy-promotion-after-withdrawal"
	if _, err := selected.Promote(ctx, withdrawnRetry); err == nil {
		t.Fatal("new operation acknowledged a withdrawn verified copy")
	}
}

func TestPromoteCandidateApprovesTrustedCorrection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Original accepted claim",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	source, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	firstRequest := PromoteRequest{PromotionReviewRequest: PromotionReviewRequest{
		VerificationSubjectRequest: VerificationSubjectRequest{MemoryID: source.BlockID,
			ExpectedSourceRevision: source.RevisionID}}, Reason: "first review",
		OperationID: "initial-approval"}
	subject, err := selected.VerificationSubject(ctx, firstRequest.VerificationSubjectRequest)
	if err != nil {
		t.Fatal(err)
	}
	firstReport, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: subject, VerifierID: "reviewer-1",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The original claim is supported"}})
	if err != nil {
		t.Fatal(err)
	}
	firstRequest.Reports = []ledger.VerificationReport{firstReport}
	approved, err := selected.Promote(ctx, firstRequest)
	if err != nil || approved["saved"] != true {
		t.Fatalf("initial approval failed: %+v, %v", approved, err)
	}
	targetID := approved["block_id"].(string)
	target, err := selected.ReadStored(ctx, targetID)
	if err != nil || target == nil {
		t.Fatal(err)
	}
	proposed, err := selected.CorrectFor(ctx, CorrectRequest{MemoryID: targetID,
		Content: "Corrected accepted claim", ExpectedRevision: target.RevisionID,
		RequestedBlockID: "stable_project_facts", SessionID: "session-1",
		OperationID: "correction-proposal"}, "memory_client", 2)
	if err != nil || proposed["pending_host_approval"] != true {
		t.Fatalf("trusted correction proposal failed: %+v, %v", proposed, err)
	}
	proposalID, _ := proposed["memory_id"].(string)
	proposal, err := selected.ReadStored(ctx, proposalID)
	if err != nil || proposal == nil || proposal.Status != "candidate" {
		t.Fatalf("correction proposal was not retained: %+v, %v", proposal, err)
	}
	request := PromoteRequest{PromotionReviewRequest: PromotionReviewRequest{
		VerificationSubjectRequest: VerificationSubjectRequest{MemoryID: proposal.BlockID,
			ExpectedSourceRevision: proposal.RevisionID}}, Reason: "correction review passed",
		OperationID: "correction-approval"}
	correctionSubject, err := selected.VerificationSubject(ctx, request.VerificationSubjectRequest)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: correctionSubject, VerifierID: "reviewer-2",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The correction is faithful to the source"}})
	if err != nil {
		t.Fatal(err)
	}
	request.Reports = []ledger.VerificationReport{report}
	result, err := selected.Promote(ctx, request)
	if err != nil || result["saved"] != true {
		t.Fatalf("approved correction failed: %+v, %v", result, err)
	}
	corrected, err := selected.ReadStored(ctx, result["block_id"].(string))
	if err != nil || corrected == nil || corrected.RevisionOf != targetID ||
		corrected.Content != proposal.Content ||
		corrected.Metadata["approved_correction_candidate_id"] != proposal.BlockID {
		t.Fatalf("approved correction did not preserve proposal provenance: %+v, %v", corrected, err)
	}
	replayed, err := selected.Promote(ctx, request)
	if err != nil || replayed["replayed"] != true || replayed["block_id"] != corrected.BlockID {
		t.Fatalf("correction replay failed: %+v, %v", replayed, err)
	}
	compensated, err := selected.CompensatePromotion(ctx, CompensatePromotionRequest{
		MemoryID: proposal.BlockID, ExpectedRevision: proposal.RevisionID,
		OperationID: "correction-approval", Reason: "review withdrawn"})
	if err != nil || compensated.Outcome != "retracted" {
		t.Fatalf("approved correction was not compensated: %+v, %v", compensated, err)
	}
	restored, err := selected.compensationCurrent(ctx, target.CanonicalKey)
	if err != nil || restored.Status != "accepted" || restored.Content != target.Content ||
		restored.RevisionOf != corrected.BlockID {
		t.Fatalf("compensation did not restore the pinned target: %+v, %v", restored, err)
	}
}

func TestPromotionDefaultScopeRejectsForeignUserBeforeProofAndReplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Private user preference",
		RequestedBlockID: "user_global_preferences", UserID: "user-b",
		SessionID: "session-b"})
	if err != nil || !created.Saved {
		t.Fatalf("user candidate setup failed: %+v, %v", created, err)
	}
	source, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	request := PromoteRequest{PromotionReviewRequest: PromotionReviewRequest{
		VerificationSubjectRequest: VerificationSubjectRequest{MemoryID: source.BlockID,
			ExpectedSourceRevision: source.RevisionID,
			Lineage:                VerificationLineage{UserID: "user-a"}}},
		Reason: "review passed", OperationID: "user-b-promotion"}
	if _, err := selected.VerificationSubject(ctx, request.VerificationSubjectRequest); err == nil {
		t.Fatal("foreign user obtained a default-scope verification subject")
	}
	if _, err := selected.Promote(ctx, request); err == nil {
		t.Fatal("foreign user claimed a publication operation")
	}
	request.Lineage.UserID = "user-b"
	subject, err := selected.VerificationSubject(ctx, request.VerificationSubjectRequest)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: subject, VerifierID: "reviewer-1",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The user preference passed independent review"}})
	if err != nil {
		t.Fatal(err)
	}
	request.Reports = []ledger.VerificationReport{report}
	approved, err := selected.Promote(ctx, request)
	if err != nil || approved["saved"] != true {
		t.Fatalf("owner could not publish the candidate: %+v, %v", approved, err)
	}
	request.Lineage.UserID = "user-a"
	if _, err := selected.Promote(ctx, request); err == nil {
		t.Fatal("foreign user replayed an existing publication receipt")
	}
}

func TestPromotionPrivateTargetBindsOperationToUser(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content: "Shared project finding", RequestedBlockID: "project_scope_stable_project_facts",
		ProjectID: "project-1", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("project candidate setup failed: %+v, %v", created, err)
	}
	source, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	request := PromoteRequest{PromotionReviewRequest: PromotionReviewRequest{
		VerificationSubjectRequest: VerificationSubjectRequest{MemoryID: source.BlockID,
			ExpectedSourceRevision: source.RevisionID, TargetBlockID: "user_global_preferences",
			Lineage: VerificationLineage{ProjectID: "project-1", UserID: "user-a"}}},
		Reason: "user-specific review passed", OperationID: "private-target-operation"}
	subject, err := selected.VerificationSubject(ctx, request.VerificationSubjectRequest)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: subject, VerifierID: "reviewer-1",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The exact candidate is appropriate for this user"}})
	if err != nil {
		t.Fatal(err)
	}
	request.Reports = []ledger.VerificationReport{report}
	result, err := selected.Promote(ctx, request)
	if err != nil || result["saved"] != true {
		t.Fatalf("owner could not publish private copy: %+v, %v", result, err)
	}
	expectedHash, err := receipts.Fingerprint(publicationArguments(request, root))
	if err != nil {
		t.Fatal(err)
	}
	commitReceipt, err := selected.index.PublicationReceipt(ctx, result["promotion_receipt_id"].(string))
	if err != nil || commitReceipt == nil || commitReceipt.RequestHash != expectedHash ||
		commitReceipt.TargetScopeID != "user-a" {
		t.Fatalf("private receipt changed Python request hash or lost bound target: %+v, %v",
			commitReceipt, err)
	}
	request.Lineage.UserID = "user-b"
	if _, err := selected.Promote(ctx, request); err == nil {
		t.Fatal("another user replayed a shared project source into a private target")
	}
}
