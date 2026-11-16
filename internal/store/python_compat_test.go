package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// The fixture was created by BackendSQLiteIndex.retain_block in the Python
// version 16 store. Go tests only need the resulting database, not Python.
func TestReadAndExtendPythonV16Index(t *testing.T) {
	fixture, err := os.ReadFile("testdata/python_v16.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "python_v16.sqlite")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	stored, err := index.ReadStoredRecord(ctx, "mem_bound")
	if err != nil || stored == nil || stored.Content != "Bound fact" {
		t.Fatalf("Go could not read Python canonical payload: %+v, %v", stored, err)
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	plan.RecordIDs = []string{"mem_bound"}
	selected, err := index.ReadExactRecords(ctx, plan)
	if err != nil || len(selected) != 1 || selected[0].BlockID != "mem_bound" {
		t.Fatalf("Go could not recall Python record: %+v, %v", selected, err)
	}
	indexed, err := index.ExactIndexTerms(ctx, "mem_bound")
	if err != nil || len(indexed["profile_id"]) != 1 || indexed["profile_id"][0] != "team" {
		t.Fatalf("Go could not read Python exact terms: %+v, %v", indexed, err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	next := *stored
	next.BlockID = "mem_go_after_python"
	next.RevisionID = "rev_go_after_python"
	next.Content = "Go write after Python"
	membership := *stored.BlockMembership
	membership.RecordID = next.BlockID
	membership.RecordRevisionID = next.RevisionID
	membership.MembershipRevisionID = membership.RevisionHash()
	next.BlockMembership = &membership
	pending, err := writer.PrepareRootWrite(ctx, next)
	if err != nil {
		t.Fatalf("Go could not stage write in Python store: %v", err)
	}
	if _, err := writer.CompleteProjection(ctx, pending.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatalf("Go could not commit write in Python store: %v", err)
	}
	plan.RecordIDs = []string{next.BlockID}
	selected, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(selected) != 1 || selected[0].Content != next.Content {
		t.Fatalf("Go write to Python store was not visible: %+v, %v", selected, err)
	}
	if _, err := writer.db.ExecContext(ctx, "DROP TABLE memory_fts"); err != nil {
		t.Fatal(err)
	}
	plan.RecordIDs = nil
	plan.Query = "Bound"
	plan.TemporalMode = "historical"
	plan.AsOf = "2024-01-15T00:00:00Z"
	selected, err = index.RecallCanonical(ctx, plan, "hindsight")
	if err != nil || len(selected) != 1 || selected[0].BlockID != "mem_bound" ||
		selected[0].Metadata["retrieval_channel"] != "recent_substring_fallback" {
		t.Fatalf("bounded substring fallback failed without FTS: %+v, %v", selected, err)
	}
}
