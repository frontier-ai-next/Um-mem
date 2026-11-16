//go:build purego

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/native/zvec"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestSplitBlockStagesAndRecallsZvecTarget(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_MODEL_DIR")
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
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "zvec", ZvecModelDir: modelDir})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	if health, err := selected.Health(ctx); err != nil || health["native_ok"] != true {
		t.Fatalf("Zvec health did not verify its native library: %+v, %v", health, err)
	}
	blocks, err := selected.topology.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	created, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Zvec source",
		Synopsis: "Source facts", ParentID: blocks[0].Block.BlockID,
		Reason: "prepare split", Scope: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := created["block_id"].(string)
	written, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Zvec stores a durable source fact.",
		RequestedBlockID: sourceID, SessionID: "session-1", RunID: "session-1"})
	if err != nil || !written.Saved {
		t.Fatalf("Zvec source was not saved: %+v, %v", written, err)
	}
	sourceRecord, err := selected.ReadStored(ctx, written.RecordID)
	if err != nil || sourceRecord == nil || sourceRecord.BlockMembership.PrimaryBlockID != sourceID {
		t.Fatalf("Zvec source record is unavailable: %+v, %v", sourceRecord, err)
	}
	receipt, err := selected.SplitBlock(ctx, SplitBlockRequest{BlockID: sourceID,
		ExpectedRevision: 1, Reason: "relocate Zvec fact", OperationID: "zvec-split",
		Targets: []BlockEvolutionTarget{{Name: "Zvec target", Synopsis: "Target facts",
			RecordIDs: []string{written.RecordID}}}})
	if err != nil || receipt["generation_status"] != "published" {
		t.Fatalf("Zvec split was not published: %+v, %v", receipt, err)
	}
	targetID := receipt["targets"].([]map[string]any)[0]["block_id"].(string)
	targetRecord, err := selected.ReadStored(ctx, written.RecordID)
	if err != nil || targetRecord == nil || targetRecord.BlockMembership.PrimaryBlockID != targetID {
		t.Fatalf("published Zvec record has wrong membership: %+v, %v", targetRecord, err)
	}
	sourcePath, err := zvec.CollectionPath(repo, *sourceRecord)
	if err != nil {
		t.Fatal(err)
	}
	targetPath, err := zvec.CollectionPath(repo, *targetRecord)
	if err != nil || sourcePath == targetPath {
		t.Fatalf("Zvec source and target share a collection: %q %q, %v",
			sourcePath, targetPath, err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("Zvec source native collection is unavailable: %v", err)
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("Zvec target native collection is unavailable: %v", err)
	}
	plan := memory.NewRecallPlan("durable source fact")
	plan.WorkspaceID, plan.ProfileID, plan.RunID = "alpha", "team", "session-1"
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.LogicalBlockIDs = []string{targetID}
	plan.IncludeCandidates = true
	recalled, err := selected.Recall(ctx, plan)
	if err != nil || len(recalled) != 1 || recalled[0].BlockID != written.RecordID ||
		recalled[0].BlockMembership.PrimaryBlockID != targetID {
		t.Fatalf("Zvec target native recall differs: %+v, %v", recalled, err)
	}
}
