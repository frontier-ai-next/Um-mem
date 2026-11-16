package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestConsolidationRootReceiptCommitsAtomicallyAndReplays(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.CanonicalKey = "alpha:team:consolidation:test"
	receipt := memory.ConsolidationCommitReceipt{ReceiptID: "commit-transition-test",
		EvidenceRefs: []memory.EvidenceRef{{RefType: "verification_report", RefID: "semantic-proof",
			ProducedBy: "verifier", ProducerID: "reader"}}, CommittedIDs: []string{record.BlockID}}
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	wrong := receipt
	wrong.CommittedIDs = []string{"other"}
	if _, err := writer.PrepareConsolidationRootWrite(ctx, record, wrong); err == nil {
		t.Fatal("receipt for another successor was staged")
	}
	staged, err := writer.PrepareConsolidationRootWrite(ctx, record, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := writer.PrepareConsolidationRootWrite(ctx, record, receipt); err != nil ||
		!replay.Replayed || replay.CommitRevision != staged.CommitRevision {
		t.Fatalf("pending consolidation did not replay: %+v, %v", replay, err)
	}
	if _, err := writer.PrepareRootWrite(ctx, record); err == nil {
		t.Fatal("ordinary root retry dropped the staged consolidation receipt")
	}
	if _, err := writer.db.Exec(`CREATE TRIGGER fail_consolidation_receipt BEFORE INSERT ON canonical_mutation_receipts
		BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, staged.CommitRevision, skippedNativeProjector{}); err == nil {
		t.Fatal("consolidation receipt failure was ignored")
	}
	var headCount, receiptCount int
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, "alpha", sha256Hex(record.CanonicalKey)).Scan(&headCount); err != nil {
		t.Fatal(err)
	}
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM canonical_mutation_receipts
		WHERE receipt_id=?`, receipt.ReceiptID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if headCount != 0 || receiptCount != 0 {
		t.Fatal("failed receipt partly committed canonical state")
	}
	if _, err := writer.db.Exec("DROP TRIGGER fail_consolidation_receipt"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, staged.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	stored, err := index.ReadStoredRecord(ctx, record.BlockID)
	if err != nil || stored == nil || stored.CanonicalKey != record.CanonicalKey {
		t.Fatalf("committed consolidation missing: %+v, %v", stored, err)
	}
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM canonical_mutation_receipts
		WHERE receipt_id=?`, receipt.ReceiptID).Scan(&receiptCount); err != nil || receiptCount != 1 {
		t.Fatalf("consolidation receipt not committed once: %d, %v", receiptCount, err)
	}
	if _, err := writer.PrepareConsolidationRootWrite(ctx, record, receipt); err != nil {
		t.Fatal(err)
	}
}

func TestPendingConsolidationRejectsSourceHeadChangedBeforeCommit(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	base, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	makeRecord := func(id, key string) memory.MemoryRecord {
		record := base
		record.BlockID, record.CanonicalKey, record.RevisionID = id, key, "revision:"+id
		membership := *base.BlockMembership
		membership.RecordID, membership.RecordRevisionID = id, record.RevisionID
		membership.MembershipRevisionID = membership.RevisionHash()
		record.BlockMembership = &membership
		return record
	}
	first, second := makeRecord("source-first", "alpha:source:first"), makeRecord("source-second", "alpha:source:second")
	for _, source := range []memory.MemoryRecord{first, second} {
		staged, err := writer.PrepareRootWrite(ctx, source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.CompleteProjection(ctx, staged.CommitRevision, skippedNativeProjector{}); err != nil {
			t.Fatal(err)
		}
	}
	consolidated := makeRecord("consolidated", "alpha:consolidation:pending")
	consolidated.Metadata = map[string]any{"source_record_ids": []string{first.BlockID, second.BlockID}}
	receipt := memory.ConsolidationCommitReceipt{ReceiptID: "commit-pending-source-check",
		EvidenceRefs: []memory.EvidenceRef{{RefType: "verification_report", RefID: "semantic-proof",
			ProducedBy: "verifier", ProducerID: "reader"}}, CommittedIDs: []string{consolidated.BlockID}}
	staged, err := writer.PrepareConsolidationRootWrite(ctx, consolidated, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.db.ExecContext(ctx, `UPDATE canonical_heads SET block_id=?
		WHERE workspace_id=? AND canonical_key_sha256=?`, second.BlockID,
		"alpha", sha256Hex(first.CanonicalKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, staged.CommitRevision, skippedNativeProjector{}); err == nil {
		t.Fatal("stale source head allowed the pending consolidation to commit")
	}
	var receiptCount, headCount int
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM canonical_mutation_receipts
		WHERE receipt_id=?`, receipt.ReceiptID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, "alpha", sha256Hex(consolidated.CanonicalKey)).Scan(&headCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 0 || headCount != 0 {
		t.Fatal("stale consolidation partially committed")
	}
	if _, err := writer.db.ExecContext(ctx, `UPDATE canonical_heads SET block_id=?
		WHERE workspace_id=? AND canonical_key_sha256=?`, first.BlockID,
		"alpha", sha256Hex(first.CanonicalKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, staged.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatalf("pending consolidation did not recover after source head was restored: %v", err)
	}
}
