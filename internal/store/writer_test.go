package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestPrepareRootWriteIsAtomicAndIdempotent(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := canonicalRecordJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	if serialized != string(bytes.TrimSpace(fixture)) {
		t.Fatal("Go canonical JSON differs from the Python record fixture")
	}
	path := filepath.Join(t.TempDir(), "UM_MEM_index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	first, err := writer.PrepareRootWrite(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	if first.CommitRevision < 1 || !first.ProjectionPending || first.Replayed {
		t.Fatalf("unexpected first receipt: %+v", first)
	}
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	got, err := index.ReadStoredRecord(ctx, record.BlockID)
	if err != nil || got != nil {
		t.Fatalf("pending projection became visible: %v, %v", got, err)
	}
	replayed, err := writer.PrepareRootWrite(ctx, record)
	if err != nil || !replayed.Replayed || replayed.CommitRevision != first.CommitRevision {
		t.Fatalf("idempotent replay = %+v, %v", replayed, err)
	}
	changed := record
	changed.Content = "different content"
	if _, err := writer.PrepareRootWrite(ctx, changed); err == nil {
		t.Fatal("same record ID accepted different content")
	}
	var blockCount, journalCount int
	if err := writer.db.QueryRow("SELECT count(*) FROM blocks").Scan(&blockCount); err != nil {
		t.Fatal(err)
	}
	if err := writer.db.QueryRow("SELECT count(*) FROM write_outbox").Scan(&journalCount); err != nil {
		t.Fatal(err)
	}
	if blockCount != 1 || journalCount != 1 {
		t.Fatalf("failed replay changed canonical rows: blocks=%d journal=%d", blockCount, journalCount)
	}
	if _, err := writer.db.Exec("UPDATE write_outbox SET status='committed' WHERE commit_revision=?", first.CommitRevision); err != nil {
		t.Fatal(err)
	}
	got, err = index.ReadStoredRecord(ctx, record.BlockID)
	if err != nil || got == nil || got.Content != record.Content {
		t.Fatalf("committed record = %v, %v", got, err)
	}
}

func TestPrepareRootWriteRollsBackOnJournalFailure(t *testing.T) {
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
	writer, err := OpenWriter(path, "alpha", "team", "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.db.Exec(`CREATE TRIGGER fail_journal BEFORE INSERT ON write_outbox
		BEGIN SELECT RAISE(ABORT, 'injected journal failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.PrepareRootWrite(context.Background(), record); err == nil {
		t.Fatal("injected journal failure was ignored")
	}
	for _, table := range []string{"blocks", "canonical_payloads", "write_outbox", "memory_temporal_index", "record_block_memberships"} {
		var count int
		if err := writer.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d rows after rollback", table, count)
		}
	}
}

func TestJSONReplayComparisonPreservesLargeIntegerIdentity(t *testing.T) {
	if sameJSON(`{"metadata":{"counter":9007199254740992}}`, `{"metadata":{"counter":9007199254740993}}`) {
		t.Fatal("immutable replay treated distinct large integers as equal")
	}
}
