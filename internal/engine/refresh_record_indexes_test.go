package engine

import (
	"context"
	"database/sql"
	"testing"
)

func TestRefreshRecordIndexesSkipsCompleteRowsAndRepairsCorruption(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content: "An indexed project fact", RequestedBlockID: "stable_project_facts",
		SourceID: "indexed-source-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	db, err := sql.Open("sqlite", selected.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_rebuild BEFORE INSERT ON memory_exact_terms
 BEGIN SELECT RAISE(ABORT, 'full rebuild was unnecessary'); END`); err != nil {
		t.Fatal(err)
	}
	if err := selected.RefreshRecordIndexes(ctx, created.RecordID); err != nil {
		t.Fatalf("complete atomic index triggered a full rebuild: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM memory_exact_terms WHERE block_id=?`, created.RecordID); err != nil {
		t.Fatal(err)
	}
	if err := selected.RefreshRecordIndexes(ctx, created.RecordID); err == nil {
		t.Fatal("broken derived index was accepted despite failed rebuild")
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_rebuild`); err != nil {
		t.Fatal(err)
	}
	if err := selected.RefreshRecordIndexes(ctx, created.RecordID); err != nil {
		t.Fatalf("derived index recovery failed: %v", err)
	}
	complete, err := selected.index.RecordDerivedIndexesComplete(ctx, created.RecordID)
	if err != nil || !complete {
		t.Fatalf("record indexes remained incomplete: %v, %v", complete, err)
	}
}
