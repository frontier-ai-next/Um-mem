package engine

import (
	"context"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/receipts"
)

func TestAmendWritesCorrectionAndPreservesGovernance(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "First version",
		Title: "Shared fact", RequestedBlockID: "stable_project_facts",
		SessionID: "session-1", RunID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	original, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || original == nil {
		t.Fatalf("original record missing: %+v, %v", original, err)
	}
	content, title, retention := "Corrected version", "Corrected fact", "session"
	request := AmendRequest{MemoryID: original.BlockID, ExpectedRevision: original.RevisionID,
		Reason: "source correction", Content: &content, Title: &title, Tags: []string{"reviewed"},
		MetadataPatch: map[string]any{"importance": 0.5, "reviewed": true},
		Retention:     &retention, RequestedBlockID: "decisions_and_contracts"}
	if _, err := selected.Amend(ctx, AmendRequest{MemoryID: original.BlockID,
		ExpectedRevision: original.RevisionID, Reason: "invalid governance",
		MetadataPatch: map[string]any{"authority": "directive"}}); err == nil {
		t.Fatal("amend accepted an authority change")
	}
	corrected, err := selected.Amend(ctx, request)
	if err != nil || corrected["saved"] != true || corrected["old_id"] != original.BlockID ||
		corrected["requested_block_id"] != "decisions_and_contracts" {
		t.Fatalf("correction failed: %+v, %v", corrected, err)
	}
	newID := corrected["new_id"].(string)
	newRecord, err := selected.ReadStored(ctx, newID)
	if err != nil || newRecord == nil || newRecord.Content != content ||
		newRecord.BlockMembership.PrimaryBlockID != "decisions_and_contracts" ||
		newRecord.Retention != "session" || newRecord.Authority != original.Authority ||
		newRecord.Scope != original.Scope || newRecord.RevisionOf != original.BlockID {
		t.Fatalf("corrected record differs: %+v, %v", newRecord, err)
	}
	old, err := selected.ReadStored(ctx, original.BlockID)
	if err != nil || old == nil || old.Content != original.Content || old.RevisionID != original.RevisionID ||
		old.BlockMembership.PrimaryBlockID != "stable_project_facts" {
		t.Fatalf("correction changed history: %+v, %v", old, err)
	}
	if _, err := selected.Amend(ctx, request); err == nil {
		t.Fatal("amend accepted a stale expected revision")
	}
	durable := "durable"
	if _, err := selected.Amend(ctx, AmendRequest{MemoryID: newID,
		ExpectedRevision: newRecord.RevisionID, Reason: "increase retention", Retention: &durable}); err == nil {
		t.Fatal("amend increased retention")
	}
}

func TestAmendRecoversPendingNativeProjection(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Before update",
		Title: "Measured fact", RequestedBlockID: "stable_project_facts",
		SessionID: "session-1", RunID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	original, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || original == nil {
		t.Fatal(err)
	}
	content := "After update"
	request := AmendRequest{MemoryID: original.BlockID, ExpectedRevision: original.RevisionID,
		Reason: "correct measurement", Content: &content, OperationID: "amend-pending-1"}
	projector := selected.projector
	selected.projector = failingForgetProjector{}
	if _, err := selected.Amend(ctx, request); err == nil {
		t.Fatal("native failure was ignored")
	}
	selected.projector = projector
	recovered, err := selected.Amend(ctx, request)
	if err != nil || recovered["saved"] != true || recovered["replayed"] != true {
		t.Fatalf("pending correction was not recovered: %+v, %v", recovered, err)
	}
	status, err := selected.OperationStatus(ctx, request.OperationID)
	if err != nil || status["durable_status"] != "committed" ||
		status["record_id"] != recovered["new_id"] {
		t.Fatalf("amendment receipt is not observable: %+v, %v", status, err)
	}
	replayed, err := selected.Amend(ctx, request)
	if err != nil || replayed["replayed"] != true || replayed["new_id"] != recovered["new_id"] {
		t.Fatalf("committed amendment receipt did not replay: %+v, %v", replayed, err)
	}
}

func TestAmendRecoversCommitBeforeReceiptCompletion(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Before receipt gap",
		Title: "Measured fact", RequestedBlockID: "stable_project_facts",
		SessionID: "session-1", RunID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	original, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || original == nil {
		t.Fatal(err)
	}
	content := "After receipt gap"
	request := AmendRequest{MemoryID: original.BlockID, ExpectedRevision: original.RevisionID,
		Reason: "correct gap", Content: &content, OperationID: "amend-gap-1"}
	identity := receipts.Identity{WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne",
		CallerRole: "go_cli", ProtocolVersion: 2}
	fingerprintRequest := request
	fingerprintRequest.OperationID = ""
	arguments := map[string]any{"tool": "amend", "request": fingerprintRequest,
		"scope_lineage": map[string]string(nil)}
	claim, err := receipts.Claim(ctx, selected.config.RepoRoot, identity, request.OperationID, arguments)
	if err != nil || claim.Status != "new" {
		t.Fatalf("amend claim failed: %+v, %v", claim, err)
	}
	committed, err := selected.amendOnce(ctx, request, claim.CreatedAt, true)
	if err != nil || committed["saved"] != true {
		t.Fatalf("canonical correction failed: %+v, %v", committed, err)
	}
	recovered, err := selected.Amend(ctx, request)
	if err != nil || recovered["saved"] != true || recovered["replayed"] != true ||
		recovered["new_id"] != committed["new_id"] {
		t.Fatalf("committed correction was not recovered from receipt gap: %+v, %v", recovered, err)
	}
}
