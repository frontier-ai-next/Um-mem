package engine

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestMnemosyneNativeWriteAndRecall(t *testing.T) {
	ctx := context.Background()
	config := Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"}
	selected, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	created, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content:          "The blue comet protocol is stored in the Mnemosyne native partition.",
		RequestedBlockID: "stable_project_facts", SourceID: "native-source-1",
	})
	if err != nil || !created.Saved || created.RecordID == "" {
		t.Fatalf("Mnemosyne candidate write failed: %+v, %v", created, err)
	}
	path := filepath.Join(config.RepoRoot, "workspaces", "alpha", ".memory", "mnemosyne", "partitions")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("native partition was not created: %v", err)
	}
	plan := memory.NewRecallPlan("blue comet")
	plan.WorkspaceID = "alpha"
	plan.ProfileID = "team"
	plan.LogicalBlockIDs = []string{"stable_project_facts"}
	plan.IncludeCandidates = true
	plan.ScopeChain = []string{"workspace:alpha"}
	recalled, err := selected.Recall(ctx, plan)
	if err != nil || len(recalled) != 1 || recalled[0].BlockID != created.RecordID {
		t.Fatalf("native BEAM recall did not return committed candidate: %+v, %v", recalled, err)
	}
	observed, err := selected.BlockHealth(ctx, []string{"stable_project_facts"})
	if err != nil {
		t.Fatal(err)
	}
	metrics := observed["block_metrics"].([]map[string]any)
	if len(metrics) != 1 || metrics[0]["recall_count"] != 1 || metrics[0]["hit_count"] != 1 ||
		metrics[0]["scoped_recall_count"] != 1 {
		t.Fatalf("native recall observations were not recorded: %+v", metrics)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recalled, err = reopened.Recall(ctx, plan)
	if err != nil || len(recalled) != 1 || recalled[0].BlockID != created.RecordID {
		t.Fatalf("reopened native BEAM partition lost committed record: %+v, %v", recalled, err)
	}
	databases, err := filepath.Glob(filepath.Join(path, "*", "mnemosyne.db"))
	if err != nil || len(databases) != 1 {
		t.Fatalf("native partition database is missing: %+v, %v", databases, err)
	}
	db, err := sql.Open("sqlite", databases[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE working_memory SET content='forged'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Recall(ctx, plan); err == nil {
		t.Fatal("tampered native BEAM row was admitted into recall")
	}
	observed, err = reopened.BlockHealth(ctx, []string{"stable_project_facts"})
	if err != nil {
		t.Fatal(err)
	}
	metrics = observed["block_metrics"].([]map[string]any)
	if len(metrics) != 1 || metrics[0]["recall_count"] != 3 || metrics[0]["failure_count"] != 1 {
		t.Fatalf("failed native recall observation was lost: %+v", metrics)
	}
}
