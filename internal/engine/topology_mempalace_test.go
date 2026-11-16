package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/native/mempalace"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type failAllNativeProjector struct{ store.NativeProjector }

func (failAllNativeProjector) Project(context.Context, memory.MemoryRecord, string, int64) (store.ProjectionResult, error) {
	return store.ProjectionResult{}, errors.New("injected native projection interruption")
}

func memPalaceModelFixture(t *testing.T) string {
	t.Helper()
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if modelDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip(err)
		}
		modelDir = filepath.Join(home, ".cache", "chroma", "onnx_models",
			"all-MiniLM-L6-v2", "onnx")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.onnx")); err != nil {
		t.Skip("MiniLM model fixture is unavailable")
	}
	return modelDir
}

func TestSplitBlockStagesAndRecallsMemPalaceTarget(t *testing.T) {
	modelDir := memPalaceModelFixture(t)
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mempalace", MiniLMModelDir: modelDir})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	if health, err := selected.Health(ctx); err != nil || health["native_ok"] != true {
		t.Fatalf("MemPalace health did not probe its native store: %+v, %v", health, err)
	}
	blocks, err := selected.topology.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	created, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "MemPalace source",
		Synopsis: "Source facts", ParentID: blocks[0].Block.BlockID,
		Reason: "prepare split", Scope: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := created["block_id"].(string)
	written, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "The palace stores a durable source fact.",
		RequestedBlockID: sourceID, SessionID: "session-1", RunID: "session-1"})
	if err != nil || !written.Saved {
		t.Fatalf("MemPalace source was not saved: %+v, %v", written, err)
	}
	sourceRecord, err := selected.ReadStored(ctx, written.RecordID)
	if err != nil || sourceRecord == nil || sourceRecord.BlockMembership.PrimaryBlockID != sourceID {
		t.Fatalf("MemPalace source record is unavailable: %+v, %v", sourceRecord, err)
	}
	receipt, err := selected.SplitBlock(ctx, SplitBlockRequest{BlockID: sourceID,
		ExpectedRevision: 1, Reason: "relocate palace fact", OperationID: "mempalace-split",
		Targets: []BlockEvolutionTarget{{Name: "MemPalace target", Synopsis: "Target facts",
			RecordIDs: []string{written.RecordID}}}})
	if err != nil || receipt["generation_status"] != "published" {
		t.Fatalf("MemPalace split was not published: %+v, %v", receipt, err)
	}
	targetID := receipt["targets"].([]map[string]any)[0]["block_id"].(string)
	targetRecord, err := selected.ReadStored(ctx, written.RecordID)
	if err != nil || targetRecord == nil || targetRecord.BlockMembership.PrimaryBlockID != targetID {
		t.Fatalf("published MemPalace record has wrong membership: %+v, %v", targetRecord, err)
	}
	recent, err := selected.Recent(ctx, RecentRequest{Limit: 5, Scope: "workspace",
		ScopeID: "alpha", LogicalBlockID: targetID, IncludeCandidates: true})
	if err != nil || len(recent) != 1 || recent[0].BlockID != written.RecordID {
		t.Fatalf("MemPalace recent missed the published target: %+v, %v", recent, err)
	}
	page, err := selected.ListRecords(ctx, ListRecordsRequest{Scope: "workspace",
		ScopeID: "alpha", LogicalBlockID: targetID, Limit: 5})
	if err != nil || len(page.Records) != 1 || page.Records[0]["logical_block_id"] != targetID {
		t.Fatalf("MemPalace list_records missed the published target: %+v, %v", page, err)
	}
	old, err := selected.Recent(ctx, RecentRequest{Limit: 5, Scope: "workspace",
		ScopeID: "alpha", LogicalBlockID: sourceID, IncludeCandidates: true})
	if err != nil || len(old) != 0 {
		t.Fatalf("MemPalace archived source still claims the record: %+v, %v", old, err)
	}
	sourceCollection, err := mempalace.CollectionName(*sourceRecord)
	if err != nil {
		t.Fatal(err)
	}
	targetCollection, err := mempalace.CollectionName(*targetRecord)
	if err != nil || sourceCollection == targetCollection {
		t.Fatalf("source and target share one MemPalace collection: %q %q, %v",
			sourceCollection, targetCollection, err)
	}
	native, err := mempalace.OpenNativeClient(repo, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	sourceRows, err := native.Get(ctx, *sourceRecord, []string{written.RecordID})
	if err != nil || len(sourceRows) != 1 {
		t.Fatalf("source native collection lost immutable copy: %+v, %v", sourceRows, err)
	}
	targetRows, err := native.Get(ctx, *targetRecord, []string{written.RecordID})
	if err != nil || len(targetRows) != 1 || targetRows[0].Document != sourceRows[0].Document ||
		targetRows[0].Metadata["logical_block_id"] != targetID {
		t.Fatalf("target native collection lacks staged copy: rows=%d, %v", len(targetRows), err)
	}
	plan := memory.NewRecallPlan("durable source fact")
	plan.WorkspaceID, plan.ProfileID, plan.RunID = "alpha", "team", "session-1"
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.LogicalBlockIDs = []string{targetID}
	plan.IncludeCandidates = true
	recalled, err := selected.Recall(ctx, plan)
	if err != nil || len(recalled) != 1 || recalled[0].BlockID != written.RecordID ||
		recalled[0].BlockMembership.PrimaryBlockID != targetID {
		t.Fatalf("MemPalace target native recall differs: %+v, %v", recalled, err)
	}
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	eraseRequest := authorizedEraseRequest(t, repo, memory.MemoryEraseRequest{RequestID: "erase-after-split",
		MemoryID: written.RecordID, WorkspaceID: "alpha", Reason: "privacy"})
	erased, err := selected.Erase(ctx, eraseRequest)
	if err != nil || erased["ok"] != true {
		t.Fatalf("erase after published topology failed: %+v %v", erased, err)
	}
	native, err = mempalace.OpenNativeClient(repo, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []memory.MemoryRecord{*sourceRecord, *targetRecord} {
		rows, err := native.Get(ctx, record, []string{written.RecordID})
		if err != nil || len(rows) != 0 {
			t.Fatalf("published topology placement survived erase: %+v %v", rows, err)
		}
	}
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMemPalaceInterruptedProjectionCanBeReconciled(t *testing.T) {
	modelDir := memPalaceModelFixture(t)
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mempalace", MiniLMModelDir: modelDir})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	native := selected.projector
	selected.projector = failAllNativeProjector{NativeProjector: native}
	if _, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content: "A recoverable MemPalace fact", RequestedBlockID: "stable_project_facts",
		SessionID: "session-1", RunID: "session-1"}); err == nil {
		t.Fatal("injected native interruption was accepted")
	}
	before, err := selected.Reconcile(ctx, true)
	if err != nil || before.PendingWriteOutbox != 1 || before.Repaired != 0 {
		t.Fatalf("MemPalace interrupted projection was not inventoried: %+v, %v", before, err)
	}
	selected.projector = native
	after, err := selected.Reconcile(ctx, false)
	if err != nil || after.PendingWriteOutbox != 1 || after.Repaired < 1 ||
		after.Remaining != 0 || after.Failures != 0 {
		t.Fatalf("MemPalace native projection was not recovered: %+v, %v", after, err)
	}
	recent, err := selected.Recent(ctx, RecentRequest{Limit: 5, Scope: "workspace",
		ScopeID: "alpha", LogicalBlockID: "stable_project_facts", IncludeCandidates: true})
	if err != nil || len(recent) != 1 || recent[0].Content != "A recoverable MemPalace fact" {
		t.Fatalf("MemPalace recovered record is not readable: %+v, %v", recent, err)
	}
}

func TestMemPalaceCandidateOperationReplaysInterruptedProjection(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mempalace",
		MiniLMModelDir: memPalaceModelFixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	request := CandidateRequest{Content: "A retryable MemPalace fact",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1",
		RunID: "session-1", OperationID: "mempalace-candidate-retry"}
	native := selected.projector
	selected.projector = failAllNativeProjector{NativeProjector: native}
	if _, err := selected.RememberCandidate(ctx, request); err == nil {
		t.Fatal("injected native interruption was accepted")
	}
	selected.projector = native
	replayed, err := selected.RememberCandidate(ctx, request)
	if err != nil || !replayed.Saved || !replayed.Replayed || replayed.RecordID == "" {
		t.Fatalf("MemPalace operation did not recover its staged record: %+v, %v", replayed, err)
	}
	again, err := selected.RememberCandidate(ctx, request)
	if err != nil || !again.Saved || !again.Replayed || again.RecordID != replayed.RecordID {
		t.Fatalf("MemPalace committed operation changed on retry: %+v, %v", again, err)
	}
	stored, err := selected.ReadStored(ctx, replayed.RecordID)
	if err != nil || stored == nil || stored.Content != request.Content {
		t.Fatalf("MemPalace recovered operation has no canonical record: %+v, %v", stored, err)
	}
}
