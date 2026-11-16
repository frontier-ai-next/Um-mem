package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestNativeProjectionHealthCountsPendingAttentionAndFailed(t *testing.T) {
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
	writer, err := OpenWriter(path, record.WorkspaceID, record.ProfileID, "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	index, err := OpenIndex(path, record.WorkspaceID, record.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	clean, err := index.NativeProjectionHealth(ctx)
	if err != nil || clean.Pending != 0 || clean.Failed != 0 || clean.Recovery != "reconcile" {
		t.Fatalf("clean projection health differs: %+v, %v", clean, err)
	}
	staged, err := writer.PrepareRootWrite(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.db.ExecContext(ctx, `UPDATE write_outbox SET error=? WHERE commit_revision=?`,
		`{"kind":"native_projection","attempts":3}`, staged.CommitRevision); err != nil {
		t.Fatal(err)
	}
	pending, err := index.NativeProjectionHealth(ctx)
	if err != nil || pending.Pending != 1 || pending.Attention != 1 ||
		pending.Failed != 0 || pending.OldestPendingMS < 0 || pending.ActivePartitions != 0 {
		t.Fatalf("pending projection health differs: %+v, %v", pending, err)
	}
	if _, err := writer.db.ExecContext(ctx, `UPDATE write_outbox SET status='failed'
		WHERE commit_revision=?`, staged.CommitRevision); err != nil {
		t.Fatal(err)
	}
	failed, err := index.NativeProjectionHealth(ctx)
	if err != nil || failed.Pending != 0 || failed.Attention != 0 ||
		failed.Failed != 1 || failed.OldestPendingMS != 0 {
		t.Fatalf("failed projection health differs: %+v, %v", failed, err)
	}
}
