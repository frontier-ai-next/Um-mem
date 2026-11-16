package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestCommittedRecordIndexHealthDetectsMissingDerivedRows(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "UM_MEM_index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	staged, err := writer.PrepareRootWrite(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if _, err := index.RecordDerivedIndexesComplete(ctx, record.BlockID); err == nil {
		t.Fatal("pending native projection was accepted as a committed index")
	}
	if _, err := writer.CompleteProjection(ctx, staged.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	complete, err := index.RecordDerivedIndexesComplete(ctx, record.BlockID)
	if err != nil || !complete {
		t.Fatalf("atomic write's derived index was incomplete: %v, %v", complete, err)
	}
	if _, err := writer.db.ExecContext(ctx, `DELETE FROM memory_exact_terms
 WHERE workspace_id=? AND block_id=?`, "alpha", record.BlockID); err != nil {
		t.Fatal(err)
	}
	complete, err = index.RecordDerivedIndexesComplete(ctx, record.BlockID)
	if err != nil || complete {
		t.Fatalf("missing exact terms were accepted: %v, %v", complete, err)
	}
	if _, err := writer.RebuildDerivedRecordIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	complete, err = index.RecordDerivedIndexesComplete(ctx, record.BlockID)
	if err != nil || !complete {
		t.Fatalf("rebuild did not restore record index: %v, %v", complete, err)
	}
	if _, err := writer.db.ExecContext(ctx, `UPDATE memory_fts SET content='corrupted'
 WHERE workspace_id=? AND block_id=?`, "alpha", record.BlockID); err != nil {
		t.Fatal(err)
	}
	complete, err = index.RecordDerivedIndexesComplete(ctx, record.BlockID)
	if err != nil || complete {
		t.Fatalf("corrupted text index was accepted: %v, %v", complete, err)
	}
}
