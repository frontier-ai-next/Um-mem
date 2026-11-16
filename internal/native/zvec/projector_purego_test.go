//go:build purego

package zvec

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestCommittedNativeRecall(t *testing.T) {
	if os.Getenv("ZVEC_LIBRARY_PATH") == "" {
		t.Skip("ZVEC_LIBRARY_PATH is unset")
	}
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_MODEL_DIR")
	if modelDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip(err)
		}
		modelDir = filepath.Join(home, ".cache", "chroma", "onnx_models", "all-MiniLM-L6-v2", "onnx")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.onnx")); err != nil {
		t.Skip(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "memory", "testdata", "record_bound_v9.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoRoot := t.TempDir()
	path := filepath.Join(repoRoot, "canonical.sqlite")
	if err := store.CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := store.OpenWriter(path, record.WorkspaceID, record.ProfileID, "zvec")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	index, err := store.OpenIndex(path, record.WorkspaceID, record.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	projector, err := NewProjector(ctx, repoRoot, modelDir)
	if err != nil {
		t.Fatal(err)
	}
	defer projector.Close()
	staged, err := writer.PrepareRootWrite(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := writer.CompleteProjection(ctx, staged.CommitRevision, projector)
	if err != nil || receipt.ProjectionPending {
		t.Fatalf("Zvec native projection was not committed: %+v, %v", receipt, err)
	}
	plan := memory.NewRecallPlan("Bound fact")
	plan.WorkspaceID, plan.ProfileID = record.WorkspaceID, record.ProfileID
	plan.ScopeChain = []string{"workspace:" + record.WorkspaceID}
	plan.LogicalBlockIDs = []string{record.BlockMembership.PrimaryBlockID}
	plan.IncludeCandidates = true
	for _, mode := range []string{"semantic", "hybrid"} {
		plan.RetrievalMode = mode
		found, err := projector.Recall(ctx, index, plan)
		if err != nil || len(found) != 1 || found[0].BlockID != record.BlockID {
			t.Fatalf("%s native recall failed: %+v, %v", mode, found, err)
		}
	}
}

func TestConcurrentZvecProjectionReplaysOneDocument(t *testing.T) {
	if os.Getenv("ZVEC_LIBRARY_PATH") == "" {
		t.Skip("ZVEC_LIBRARY_PATH is unset")
	}
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_MODEL_DIR")
	if modelDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip(err)
		}
		modelDir = filepath.Join(home, ".cache", "chroma", "onnx_models", "all-MiniLM-L6-v2", "onnx")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.onnx")); err != nil {
		t.Skip(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "memory", "testdata", "record_bound_v9.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	projector, err := NewProjector(context.Background(), t.TempDir(), modelDir)
	if err != nil {
		t.Fatal(err)
	}
	defer projector.Close()
	const workers = 8
	ids := make([]string, workers)
	errors := make([]error, workers)
	var group sync.WaitGroup
	for i := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := projector.Project(context.Background(), record, string(data), 7)
			ids[i], errors[i] = result.NativeID, err
		}()
	}
	group.Wait()
	for i := range workers {
		if errors[i] != nil || ids[i] == "" || ids[i] != ids[0] {
			t.Fatalf("concurrent Zvec replay differed: ids=%+v errors=%+v", ids, errors)
		}
	}
}
