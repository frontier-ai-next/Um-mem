package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestMemPalaceEngineWriteRecallAndRestart(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if modelDir == "" {
		t.Skip("MiniLM model fixture is unavailable")
	}
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.BlockMembership = nil
	record.SemanticCatalogVersion = ""
	repo := t.TempDir()
	config := Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team",
		BackendID: "mempalace", ZvecModelDir: modelDir}
	ctx := context.Background()
	engine, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	receipt, err := engine.PutRoot(ctx, record)
	if err != nil || receipt.ProjectionPending || receipt.CommitRevision < 1 {
		t.Fatalf("MemPalace engine write failed: %+v, %v", receipt, err)
	}
	plan := memory.NewRecallPlan("Bound fact")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.LogicalBlockIDs = []string{"stable_project_facts"}
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	for _, mode := range []string{"semantic", "hybrid"} {
		plan.RetrievalMode = mode
		hits, err := engine.Recall(ctx, plan)
		if err != nil || len(hits) != 1 || hits[0].BlockID != record.BlockID {
			t.Fatalf("MemPalace %s recall failed: %+v, %v", mode, hits, err)
		}
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	plan.RetrievalMode = "semantic"
	hits, err := reopened.Recall(ctx, plan)
	if err != nil || len(hits) != 1 || hits[0].Content != record.Content {
		t.Fatalf("MemPalace recall after restart failed: %+v, %v", hits, err)
	}
	plan.ScopeChain = []string{"workspace:other"}
	hits, err = reopened.Recall(ctx, plan)
	if err != nil || len(hits) != 0 {
		t.Fatalf("MemPalace scope isolation failed: %+v, %v", hits, err)
	}
}

func TestMemPalaceLongRecordRecallThroughWindows(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if modelDir == "" {
		t.Skip("MiniLM model fixture is unavailable")
	}
	data, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	record.BlockMembership = nil
	record.SemanticCatalogVersion = ""
	record.Content = strings.Repeat("north orbit signal ", 140) + "quasar lantern ending"
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team",
		BackendID: "mempalace", MiniLMModelDir: modelDir})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	ctx := context.Background()
	if err := selected.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PutRoot(ctx, record); err != nil {
		t.Fatal(err)
	}
	plan := memory.NewRecallPlan("quasar lantern ending")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.LogicalBlockIDs = []string{"stable_project_facts"}
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	for _, mode := range []string{"semantic", "hybrid"} {
		plan.RetrievalMode = mode
		hits, err := selected.Recall(ctx, plan)
		if err != nil || len(hits) != 1 || hits[0].Content != record.Content {
			t.Fatalf("MemPalace %s missed the tail window: %+v, %v", mode, hits, err)
		}
		if mode == "hybrid" {
			ranks, ok := hits[0].Metadata["channel_ranks"].(map[string]int)
			if !ok || ranks["semantic"] < 1 || ranks["lexical"] < 1 {
				t.Fatalf("MemPalace hybrid recall did not use both native channels: %+v", hits[0].Metadata)
			}
		}
	}
}
