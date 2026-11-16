package engine

import (
	"context"
	"testing"
)

func TestCompensatePromotionRequiresPinnedSourceAndRetractsCandidate(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content: "A candidate that did not pass review", RequestedBlockID: "stable_project_facts"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	source, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || source == nil {
		t.Fatalf("pinned candidate was not stored: %+v, %v", source, err)
	}
	request := CompensatePromotionRequest{MemoryID: source.BlockID,
		ExpectedRevision: source.RevisionID, OperationID: "failed-review",
		Reason: "independent review failed"}
	stale := request
	stale.ExpectedRevision = "wrong-revision"
	if _, err := selected.CompensatePromotion(ctx, stale); err == nil {
		t.Fatal("compensation accepted a stale source revision")
	}
	result, err := selected.CompensatePromotion(ctx, request)
	if err != nil || result.Outcome != "retracted" || result.RecordID == source.BlockID {
		t.Fatalf("candidate was not retracted: %+v, %v", result, err)
	}
	current, err := selected.ReadStored(ctx, result.RecordID)
	if err != nil || current == nil || current.Status != "retracted" ||
		current.RevisionOf != source.BlockID {
		t.Fatalf("retraction was not durable: %+v, %v", current, err)
	}
}
