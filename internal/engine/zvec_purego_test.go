//go:build purego

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestZvecEngineWriteRecallAndReopen(t *testing.T) {
	if os.Getenv("ZVEC_LIBRARY_PATH") == "" && os.Getenv("UM_MEM_TEST_ZVEC_AUTOINSTALL") != "1" {
		t.Skip("ZVEC_LIBRARY_PATH is unset and automatic native installation is not requested")
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
	ctx := context.Background()
	config := Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team",
		BackendID: "zvec", ZvecModelDir: modelDir}
	selected, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	created, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content:          "The blue comet protocol is stored in the Zvec native partition.",
		RequestedBlockID: "stable_project_facts", SourceID: "zvec-source-1",
	})
	if err != nil || !created.Saved || created.RecordID == "" {
		t.Fatalf("Zvec candidate write failed: %+v, %v", created, err)
	}
	plan := memory.NewRecallPlan("blue comet")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.LogicalBlockIDs = []string{"stable_project_facts"}
	plan.IncludeCandidates = true
	plan.ScopeChain = []string{"workspace:alpha"}
	for _, mode := range []string{"semantic", "hybrid"} {
		plan.RetrievalMode = mode
		found, err := selected.Recall(ctx, plan)
		if err != nil || len(found) != 1 || found[0].BlockID != created.RecordID {
			t.Fatalf("%s Zvec recall failed: %+v, %v", mode, found, err)
		}
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	plan.RetrievalMode = "semantic"
	found, err := reopened.Recall(ctx, plan)
	if err != nil || len(found) != 1 || found[0].BlockID != created.RecordID {
		t.Fatalf("reopened Zvec partition lost committed record: %+v, %v", found, err)
	}
}
