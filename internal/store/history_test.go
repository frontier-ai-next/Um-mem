package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

type skippedNativeProjector struct{}

func (skippedNativeProjector) BackendID() string                       { return "hindsight" }
func (skippedNativeProjector) RequiresNative(memory.MemoryRecord) bool { return false }
func (skippedNativeProjector) Project(context.Context, memory.MemoryRecord, string, int64) (ProjectionResult, error) {
	return ProjectionResult{Verified: true, Skipped: true}, nil
}

func TestCanonicalRootHistoryIsAtomicWithWriteCompletion(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.CanonicalKey = "alpha:observation:root"
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
	pending, err := writer.PrepareRootWrite(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	var committedAt string
	if err := writer.db.QueryRowContext(ctx, "SELECT committed_at FROM blocks WHERE block_id=?", record.BlockID).Scan(&committedAt); err != nil || committedAt != "" {
		t.Fatalf("staged root has a commit timestamp: %q, %v", committedAt, err)
	}
	if visible, err := index.ReadStoredRecord(ctx, record.BlockID); err != nil || visible != nil {
		t.Fatalf("pending root was visible: %v, %v", visible, err)
	}
	if history, err := index.CanonicalHistory(ctx, record.CanonicalKey); err != nil || len(history) != 0 {
		t.Fatalf("pending root entered history: %v, %v", history, err)
	}
	if _, err := writer.db.Exec(`CREATE TRIGGER fail_canonical_mutation BEFORE INSERT ON canonical_mutations
		BEGIN SELECT RAISE(ABORT, 'injected head commit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, pending.CommitRevision, skippedNativeProjector{}); err == nil {
		t.Fatal("head commit failure was ignored")
	}
	var status string
	if err := writer.db.QueryRowContext(ctx, "SELECT status FROM write_outbox WHERE commit_revision=?", pending.CommitRevision).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("write journal changed after head rollback: %q, %v", status, err)
	}
	if _, err := writer.db.Exec("DROP TRIGGER fail_canonical_mutation"); err != nil {
		t.Fatal(err)
	}
	committed, err := writer.CompleteProjection(ctx, pending.CommitRevision, skippedNativeProjector{})
	if err != nil || committed.ProjectionPending {
		t.Fatalf("root commit failed: %+v, %v", committed, err)
	}
	if err := writer.db.QueryRowContext(ctx, "SELECT committed_at FROM blocks WHERE block_id=?", record.BlockID).Scan(&committedAt); err != nil || committedAt == "" {
		t.Fatalf("committed root has no commit timestamp: %q, %v", committedAt, err)
	}
	history, err := index.CanonicalHistory(ctx, record.CanonicalKey)
	if err != nil || len(history) != 1 || history[0].BlockID != record.BlockID {
		t.Fatalf("canonical root history missing: %+v, %v", history, err)
	}
	var headID string
	if err := writer.db.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, "alpha", sha256Hex(record.CanonicalKey)).Scan(&headID); err != nil || headID != record.BlockID {
		t.Fatalf("canonical head missing: %q, %v", headID, err)
	}
	if _, err := writer.CompleteProjection(ctx, pending.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatalf("committed root did not replay: %v", err)
	}
	conflict := record
	conflict.BlockID = "other-root"
	membership := *record.BlockMembership
	membership.RecordID = conflict.BlockID
	membership.MembershipRevisionID = membership.RevisionHash()
	conflict.BlockMembership = &membership
	if _, err := writer.PrepareRootWrite(ctx, conflict); err == nil {
		t.Fatal("conflicting canonical root was accepted")
	}
	if _, err := writer.db.ExecContext(ctx, `UPDATE canonical_mutations
		SET record_payload_id='record:missing' WHERE successor_id=?`, record.BlockID); err != nil {
		t.Fatal(err)
	}
	if _, err := index.CanonicalHistory(ctx, record.CanonicalKey); err == nil {
		t.Fatal("canonical history silently omitted a missing payload")
	}
}

func TestCanonicalMutationPayloadMatchesPythonShape(t *testing.T) {
	got, err := canonicalMutationJSON("record:abc", "abc", "insert", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"action":"insert","affected_view_ids":[],"amendment_receipt":null,"consolidation_receipt":null,"edge":null,"publication_receipt":null,"record_payload_id":"record:abc","record_payload_sha256":"abc","rollback_target_id":"","superseded_block_id":""}`
	if got != want {
		t.Fatalf("canonical mutation payload changed: %s", got)
	}
}

func TestCanonicalSuccessorCompareAndSwapAndHistory(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	root.CanonicalKey = "alpha:observation:chain"
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
	rootPending, err := writer.PrepareRootWrite(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, rootPending.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	var firstCommit string
	if err := writer.db.QueryRowContext(ctx, `SELECT committed_at FROM canonical_mutations
		WHERE successor_id=?`, root.BlockID).Scan(&firstCommit); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	successor := root
	successor.BlockID = "mem_successor"
	successor.RevisionID = "rev_2"
	successor.RevisionOf = root.BlockID
	successor.SupersedesIDs = []string{root.BlockID}
	successor.Content = "Corrected bound fact"
	successor.ValidFrom = "2024-02-01T00:00:00Z"
	successor.RecordedAt = successor.ValidFrom
	successor.UpdatedAt = successor.ValidFrom
	membership := *root.BlockMembership
	membership.RecordID = successor.BlockID
	membership.RecordRevisionID = successor.RevisionID
	membership.ValidFrom = successor.ValidFrom
	membership.MembershipRevisionID = membership.RevisionHash()
	successor.BlockMembership = &membership
	pending, err := writer.PrepareSuccessorWrite(ctx, successor, "supersede", "")
	if err != nil {
		t.Fatal(err)
	}
	if visible, err := index.ReadStoredRecord(ctx, successor.BlockID); err != nil || visible != nil {
		t.Fatalf("pending successor became visible: %v, %v", visible, err)
	}
	stale := successor
	stale.BlockID = "stale-successor"
	staleMembership := *successor.BlockMembership
	staleMembership.RecordID = stale.BlockID
	staleMembership.MembershipRevisionID = staleMembership.RevisionHash()
	stale.BlockMembership = &staleMembership
	if _, err := writer.PrepareSuccessorWrite(ctx, stale, "supersede", ""); err == nil {
		t.Fatal("competing successor was staged while head reservation was pending")
	}
	if _, err := writer.CompleteProjection(ctx, pending.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	var secondCommit string
	if err := writer.db.QueryRowContext(ctx, `SELECT committed_at FROM canonical_mutations
		WHERE successor_id=?`, successor.BlockID).Scan(&secondCommit); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.PrepareSuccessorWrite(ctx, stale, "supersede", ""); err == nil {
		t.Fatal("stale predecessor passed compare-and-swap after successor commit")
	}
	rebuilt, err := writer.RebuildDerivedRecordIndexes(ctx)
	if err != nil || rebuilt.RecordCount != 2 {
		t.Fatalf("canonical correction failed derived-index rebuild: %+v, %v", rebuilt, err)
	}
	var oldBoundary string
	var oldCurrent, newCurrent int
	if err := writer.db.QueryRowContext(ctx, `SELECT valid_to,is_current FROM memory_temporal_index
		WHERE block_id=?`, root.BlockID).Scan(&oldBoundary, &oldCurrent); err != nil {
		t.Fatal(err)
	}
	if err := writer.db.QueryRowContext(ctx, `SELECT is_current FROM memory_temporal_index
		WHERE block_id=?`, successor.BlockID).Scan(&newCurrent); err != nil ||
		oldBoundary != successor.ValidFrom || oldCurrent != 0 || newCurrent != 1 {
		t.Fatalf("rebuild lost correction boundary: old=%s/%d new=%d err=%v",
			oldBoundary, oldCurrent, newCurrent, err)
	}
	history, err := index.CanonicalHistory(ctx, root.CanonicalKey)
	if err != nil || len(history) != 2 || history[0].BlockID != root.BlockID ||
		history[0].ValidTo != successor.ValidFrom || history[1].BlockID != successor.BlockID {
		t.Fatalf("canonical revision history invalid: %+v, %v", history, err)
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.RecordIDs = []string{root.BlockID, successor.BlockID}
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	exact, err := index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockID != successor.BlockID {
		t.Fatalf("old canonical head leaked through exact recall: %+v, %v", exact, err)
	}
	plan.TemporalMode = "historical"
	plan.AsOf = "2024-01-15T00:00:00Z"
	exact, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockID != root.BlockID {
		t.Fatalf("historical pre-correction event is wrong: %+v, %v", exact, err)
	}
	plan.AsOf = successor.ValidFrom
	exact, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockID != successor.BlockID {
		t.Fatalf("historical correction boundary is wrong: %+v, %v", exact, err)
	}
	semanticPlan := plan
	semanticPlan.RecordIDs = nil
	semanticPlan.Query = "bound"
	semanticPlan.NormalizedQuery = ""
	semanticPlan.QueryType = ""
	semanticPlan.AsOf = "2024-01-15T00:00:00Z"
	semantic, err := index.RecallCanonical(ctx, semanticPlan, "hindsight")
	if err != nil || len(semantic) != 1 || semantic[0].BlockID != root.BlockID ||
		semantic[0].Metadata["retrieval_channel"] != "sqlite_fts5_bm25" {
		t.Fatalf("historical text recall before correction differs: %+v, %v", semantic, err)
	}
	semanticPlan.AsOf = successor.ValidFrom
	semantic, err = index.RecallCanonical(ctx, semanticPlan, "hindsight")
	if err != nil || len(semantic) != 1 || semantic[0].BlockID != successor.BlockID {
		t.Fatalf("historical text recall at correction differs: %+v, %v", semantic, err)
	}
	plan.KnownAsOf = firstCommit
	exact, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockID != root.BlockID || exact[0].ValidTo != "" {
		t.Fatalf("knowledge snapshot leaked future correction: %+v, %v", exact, err)
	}
	plan.AsOf = "2023-12-31T00:00:00Z"
	exact, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 0 {
		t.Fatalf("knowledge snapshot ignored event start: %+v, %v", exact, err)
	}
	plan.AsOf = successor.ValidFrom
	plan.KnownAsOf = secondCommit
	exact, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockID != successor.BlockID {
		t.Fatalf("knowledge snapshot missed committed correction: %+v, %v", exact, err)
	}
	plan.KnownAsOf = ""
	plan.AsOf = ""
	plan.RevisionID = successor.RevisionID
	exact, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockID != successor.BlockID {
		t.Fatalf("historical revision filter is wrong: %+v, %v", exact, err)
	}
	plan.RevisionID = ""
	plan.TemporalMode = "current"
	plan.AsOf = "2024-01-15T00:00:00Z"
	exact, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockID != root.BlockID {
		t.Fatalf("as_of current head before correction is wrong: %+v, %v", exact, err)
	}
	plan.AsOf = successor.ValidFrom
	exact, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockID != successor.BlockID {
		t.Fatalf("as_of current head at correction is wrong: %+v, %v", exact, err)
	}
	plan.AsOf = ""
	var headID, edgePayload string
	if err := writer.db.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, "alpha", sha256Hex(root.CanonicalKey)).Scan(&headID); err != nil || headID != successor.BlockID {
		t.Fatalf("canonical head did not advance: %q, %v", headID, err)
	}
	if err := writer.db.QueryRowContext(ctx, `SELECT payload_json FROM edges WHERE workspace_id=?
		AND src_id=? AND dst_id=? AND edge_type='supersedes'`, "alpha", successor.BlockID, root.BlockID).Scan(&edgePayload); err != nil {
		t.Fatalf("supersedes edge missing: %v", err)
	}
	if edgePayload == "" {
		t.Fatal("supersedes edge has no payload")
	}
	if !strings.Contains(edgePayload, `"weight":1.0`) {
		t.Fatalf("supersedes edge lost Python float representation: %s", edgePayload)
	}
	var stagedCount int
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM write_outbox WHERE block_id=?`, stale.BlockID).Scan(&stagedCount); err != nil || stagedCount != 0 {
		t.Fatalf("stale successor reached native write journal: %d, %v", stagedCount, err)
	}
	retracted := successor
	retracted.BlockID = "mem_retracted"
	retracted.RevisionID = "rev_3"
	retracted.RevisionOf = successor.BlockID
	retracted.SupersedesIDs = []string{successor.BlockID}
	retracted.Status = "retracted"
	retracted.Retention = "archive"
	retracted.DeliveryPolicy = "hidden"
	retracted.ValidFrom = "2024-03-01T00:00:00Z"
	retracted.RecordedAt = retracted.ValidFrom
	retracted.UpdatedAt = retracted.ValidFrom
	retractedMembership := *successor.BlockMembership
	retractedMembership.RecordID = retracted.BlockID
	retractedMembership.RecordRevisionID = retracted.RevisionID
	retractedMembership.ValidFrom = retracted.ValidFrom
	retractedMembership.MembershipRevisionID = retractedMembership.RevisionHash()
	retracted.BlockMembership = &retractedMembership
	retractPending, err := writer.PrepareSuccessorWrite(ctx, retracted, "retract", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, retractPending.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	plan.RecordIDs = []string{root.BlockID, successor.BlockID, retracted.BlockID}
	exact, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 0 {
		t.Fatalf("retracted canonical chain leaked through current recall: %+v, %v", exact, err)
	}
	history, err = index.CanonicalHistory(ctx, root.CanonicalKey)
	if err != nil || len(history) != 3 || history[1].ValidTo != retracted.ValidFrom ||
		history[2].Status != "retracted" {
		t.Fatalf("retraction history is incomplete: %+v, %v", history, err)
	}
}

func TestUndatedPredecessorHasNoInventedValidityStartOrEnd(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	root.CanonicalKey = "alpha:undated"
	root.ValidFrom = ""
	membership := *root.BlockMembership
	membership.ValidFrom = ""
	membership.MembershipRevisionID = membership.RevisionHash()
	root.BlockMembership = &membership
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
	rootPending, err := writer.PrepareRootWrite(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, rootPending.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	var firstCommit string
	if err := writer.db.QueryRowContext(ctx, `SELECT committed_at FROM canonical_mutations
		WHERE successor_id=?`, root.BlockID).Scan(&firstCommit); err != nil {
		t.Fatal(err)
	}
	successor := root
	successor.BlockID = "dated-successor"
	successor.RevisionID = "rev_dated"
	successor.RevisionOf = root.BlockID
	successor.SupersedesIDs = []string{root.BlockID}
	successor.ValidFrom = "2024-02-01T00:00:00Z"
	successor.RecordedAt = successor.ValidFrom
	successor.Content = "Dated correction"
	successorMembership := *root.BlockMembership
	successorMembership.RecordID = successor.BlockID
	successorMembership.RecordRevisionID = successor.RevisionID
	successorMembership.ValidFrom = successor.ValidFrom
	successorMembership.MembershipRevisionID = successorMembership.RevisionHash()
	successor.BlockMembership = &successorMembership
	pending, err := writer.PrepareSuccessorWrite(ctx, successor, "correction", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, pending.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	history, err := index.CanonicalHistory(ctx, root.CanonicalKey)
	if err != nil || len(history) != 2 || history[0].ValidFrom != "" || history[0].ValidTo != "" {
		t.Fatalf("undated predecessor gained invented bounds: %+v, %v", history, err)
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.RecordIDs = []string{root.BlockID, successor.BlockID}
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	plan.TemporalMode = "historical"
	plan.AsOf = "2024-01-01T00:00:00Z"
	earlier, err := index.ReadExactRecords(ctx, plan)
	if err != nil || len(earlier) != 1 || earlier[0].BlockID != root.BlockID {
		t.Fatalf("undated predecessor missing before correction: %+v, %v", earlier, err)
	}
	plan.AsOf = "2024-03-01T00:00:00Z"
	later, err := index.ReadExactRecords(ctx, plan)
	if err != nil || len(later) != 1 || later[0].BlockID != successor.BlockID {
		t.Fatalf("undated predecessor leaked after correction: %+v, %v", later, err)
	}
	plan.KnownAsOf = firstCommit
	known, err := index.ReadExactRecords(ctx, plan)
	if err != nil || len(known) != 1 || known[0].BlockID != root.BlockID || known[0].ValidTo != "" {
		t.Fatalf("knowledge snapshot lost undated predecessor: %+v, %v", known, err)
	}
}
