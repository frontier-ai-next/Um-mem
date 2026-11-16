package engine

import (
	"context"
	"errors"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type failingForgetProjector struct{}

func (failingForgetProjector) BackendID() string                       { return "mnemosyne" }
func (failingForgetProjector) RequiresNative(memory.MemoryRecord) bool { return true }
func (failingForgetProjector) Project(context.Context, memory.MemoryRecord, string, int64) (store.ProjectionResult, error) {
	return store.ProjectionResult{}, errors.New("temporary native failure")
}

func TestForgetRetriesPendingSuccessorAndKeepsHistory(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	result, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Measured fact",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1"})
	if err != nil || !result.Saved {
		t.Fatalf("candidate write failed: %+v, %v", result, err)
	}
	before, err := selected.ReadStored(ctx, result.RecordID)
	if err != nil || before == nil {
		t.Fatalf("original record missing: %+v, %v", before, err)
	}
	wrong, err := selected.Forget(ctx, ForgetRequest{MemoryID: before.BlockID,
		ExpectedRevision: "wrong", Reason: "withdraw"})
	if err != nil || wrong["error"] != "expected_revision_conflict" {
		t.Fatalf("stale retraction was accepted: %+v, %v", wrong, err)
	}
	originalProjector := selected.projector
	selected.projector = failingForgetProjector{}
	if _, err := selected.Forget(ctx, ForgetRequest{MemoryID: before.BlockID,
		ExpectedRevision: before.RevisionID, Reason: "withdraw"}); err == nil {
		t.Fatal("native failure was not reported")
	}
	rows, err := selected.index.ProjectionRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pendingID string
	for _, row := range rows {
		if row.Status == "pending" && row.Record.RevisionOf == before.BlockID {
			pendingID = row.Record.BlockID
		}
	}
	if pendingID == "" {
		t.Fatal("failed projection left no recoverable successor")
	}
	current, err := selected.ReadStored(ctx, before.BlockID)
	if err != nil || current == nil || current.Status == "retracted" {
		t.Fatalf("native failure modified the old revision: %+v, %v", current, err)
	}
	selected.projector = originalProjector
	recovered, err := selected.Forget(ctx, ForgetRequest{MemoryID: before.BlockID,
		ExpectedRevision: before.RevisionID, Reason: "withdraw"})
	if err != nil || recovered["block_id"] != pendingID || recovered["saved"] != true {
		t.Fatalf("pending retraction was not recovered: %+v, %v", recovered, err)
	}
	repeated, err := selected.Forget(ctx, ForgetRequest{MemoryID: before.BlockID,
		ExpectedRevision: before.RevisionID, Reason: "withdraw"})
	if err != nil || repeated["already_retracted"] != true || repeated["block_id"] != pendingID {
		t.Fatalf("exact retraction retry was not idempotent: %+v, %v", repeated, err)
	}
	after, err := selected.ReadStored(ctx, pendingID)
	if err != nil || after == nil || after.Status != "retracted" || after.RevisionOf != before.BlockID ||
		after.DeliveryPolicy != "hidden" || after.Retention != "archive" {
		t.Fatalf("retracted successor is invalid: %+v, %v", after, err)
	}
	old, err := selected.ReadStored(ctx, before.BlockID)
	if err != nil || old == nil || old.Content != before.Content || old.RevisionID != before.RevisionID {
		t.Fatalf("historical revision was changed: %+v, %v", old, err)
	}
}

func TestCascadeDeleteRetractsCurrentRecordsBeforeTombstone(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	rootID := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
		}
	}
	leaf, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Temporary",
		Synopsis: "Temporary observations", ParentID: rootID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	leafID := leaf["block_id"].(string)
	ids := make([]string, 0, 2)
	for _, content := range []string{"First independent observation", "Second independent observation"} {
		result, err := selected.RememberCandidate(ctx, CandidateRequest{Content: content,
			RequestedBlockID: leafID, SessionID: "session-1", RunID: "session-1"})
		if err != nil || !result.Saved {
			t.Fatalf("candidate write failed: %+v, %v", result, err)
		}
		ids = append(ids, result.RecordID)
	}
	if _, err := selected.ArchiveBlock(ctx, BlockTransitionRequest{BlockID: leafID,
		ExpectedRevision: 1, Reason: "retire temporary scope"}); err != nil {
		t.Fatal(err)
	}
	preview, err := selected.DeleteBlock(ctx, DeleteBlockRequest{BlockID: leafID,
		ExpectedRevision: 2, Reason: "cleanup"})
	if err != nil || preview["deleted"] != false || preview["records_remaining"] != 2 {
		t.Fatalf("delete preview lost current records: %+v, %v", preview, err)
	}
	removed, err := selected.DeleteBlock(ctx, DeleteBlockRequest{BlockID: leafID,
		ExpectedRevision: 2, Reason: "cleanup", Cascade: true})
	if err != nil || removed["deleted"] != true || removed["records_remaining"] != 0 {
		t.Fatalf("cascade deletion failed: %+v, %v", removed, err)
	}
	for _, id := range ids {
		original, err := selected.ReadStored(ctx, id)
		if err != nil || original == nil || original.Status == "retracted" {
			t.Fatalf("cascade changed historical record: %+v, %v", original, err)
		}
		repeated, err := selected.Forget(ctx, ForgetRequest{MemoryID: id,
			ExpectedRevision: original.RevisionID, Reason: "cascade delete block: cleanup"})
		if err != nil || repeated["already_retracted"] != true {
			t.Fatalf("cascade did not commit a retraction: %+v, %v", repeated, err)
		}
	}
}

func TestCascadeDeleteReportsNativeFailureAndRecovers(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	rootID := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
		}
	}
	leaf, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Failure scope",
		Synopsis: "Native retry check", ParentID: rootID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	leafID := leaf["block_id"].(string)
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "A result awaiting retirement",
		RequestedBlockID: leafID, SessionID: "session-1", RunID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	if _, err := selected.ArchiveBlock(ctx, BlockTransitionRequest{BlockID: leafID,
		ExpectedRevision: 1, Reason: "retire scope"}); err != nil {
		t.Fatal(err)
	}
	projector := selected.projector
	selected.projector = failingForgetProjector{}
	failed, err := selected.DeleteBlock(ctx, DeleteBlockRequest{BlockID: leafID,
		ExpectedRevision: 2, Reason: "delete after retraction", Cascade: true})
	if err != nil || failed["deleted"] != false || failed["records_remaining"] != 1 ||
		failed["pending_native_writes"] != true || len(failed["cascade_failures"].([]map[string]string)) != 1 {
		t.Fatalf("cascade failure did not preserve and report the block: %+v, %v", failed, err)
	}
	selected.projector = projector
	recovered, err := selected.DeleteBlock(ctx, DeleteBlockRequest{BlockID: leafID,
		ExpectedRevision: 2, Reason: "delete after retraction", Cascade: true})
	if err != nil || recovered["deleted"] != true {
		t.Fatalf("cascade retry did not recover pending retraction: %+v, %v", recovered, err)
	}
}
