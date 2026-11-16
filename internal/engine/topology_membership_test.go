package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/native/mnemosyne"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type failTopologyProjector struct{ delegate store.NativeProjector }

func (projector failTopologyProjector) BackendID() string {
	return projector.delegate.BackendID()
}

func (projector failTopologyProjector) RequiresNative(record memory.MemoryRecord) bool {
	return projector.delegate.RequiresNative(record)
}

func (projector failTopologyProjector) Project(ctx context.Context,
	record memory.MemoryRecord, payload string, revision int64) (store.ProjectionResult, error) {
	if record.BlockMembership != nil &&
		strings.HasPrefix(record.BlockMembership.CatalogRevisionID, "topology:") {
		return store.ProjectionResult{}, errors.New("injected topology projection failure")
	}
	return projector.delegate.Project(ctx, record, payload, revision)
}

func TestPublishedTopologyMembershipFlowsThroughEngineReaders(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	if health, err := selected.Health(ctx); err != nil || health["native_ok"] != true {
		t.Fatalf("Mnemosyne health did not probe native partitions: %+v, %v", health, err)
	}
	blocks, err := selected.topology.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	created, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Source topic",
		Synopsis: "Original record topic", ParentID: blocks[0].Block.BlockID,
		Reason: "prepare topic split", Scope: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := created["block_id"].(string)
	written, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "A durable source fact",
		RequestedBlockID: sourceID, SessionID: "session-1", RunID: "session-1"})
	if err != nil || !written.Saved {
		t.Fatalf("source record write failed: %+v, %v", written, err)
	}
	original, err := selected.ReadStored(ctx, written.RecordID)
	if err != nil || original == nil || original.BlockMembership.PrimaryBlockID != sourceID {
		t.Fatalf("committed source record is unavailable: %+v, %v", original, err)
	}
	source, err := selected.topology.GetBlock(ctx, sourceID)
	if err != nil || source == nil {
		t.Fatalf("source catalog block is unavailable: %+v, %v", source, err)
	}
	target := source.Block
	target.BlockID = "published-target"
	target.Name = "Published target"
	payload := map[string]any{"operation": "split",
		"sources": map[string]any{sourceID: source.Revision},
		"targets": []any{map[string]any{"block": target,
			"backend_partition_id": "published-partition"}},
		"assignments": map[string]any{written.RecordID: map[string]any{
			"record_revision_id": original.RevisionID,
			"target_block_id":    target.BlockID}},
		"reason": "publish exact record mapping", "actor": "test"}
	if _, err := selected.topology.PrepareTopologyEvolution(ctx, "split-one", payload); err != nil {
		t.Fatal(err)
	}
	stagedRows, err := selected.index.ProjectionRows(ctx)
	if err != nil || len(stagedRows) != 1 {
		t.Fatalf("committed source projection is unavailable: %+v, %v", stagedRows, err)
	}
	if stagedRows[0].CanonicalJSON == "" {
		t.Fatal("unchanged projection lost its validated canonical payload")
	}
	nativeProjectorBeforeFailure := selected.projector
	selected.projector = failTopologyProjector{delegate: nativeProjectorBeforeFailure}
	if _, err := selected.ResumeTopologyEvolution(ctx, "split-one"); err == nil ||
		!strings.Contains(err.Error(), "injected topology projection failure") {
		t.Fatalf("native staging failure was not preserved: %v", err)
	}
	journal, err := selected.topology.TopologyEvolution(ctx, "split-one")
	if err != nil || journal == nil || journal.State != "prepared" {
		t.Fatalf("failed native staging changed the durable generation: %+v, %v", journal, err)
	}
	selected.projector = nativeProjectorBeforeFailure
	receipt, err := selected.ResumeTopologyEvolution(ctx, "split-one")
	if err != nil || receipt["generation_status"] != "published" ||
		receipt["operation"] != "split" {
		t.Fatalf("durable split mapping was not projected and published: %+v, %v", receipt, err)
	}
	sourceSnapshot, err := selected.index.LogicalBlockDescriptorSnapshot(ctx, sourceID)
	if err != nil || sourceSnapshot.RecordCount != 0 {
		t.Fatalf("archived source descriptor still counts moved record: %+v, %v", sourceSnapshot, err)
	}
	targetSnapshot, err := selected.index.LogicalBlockDescriptorSnapshot(ctx, target.BlockID)
	if err != nil || targetSnapshot.RecordCount != 1 ||
		len(targetSnapshot.RelatedBlockIDs) != 1 ||
		targetSnapshot.RelatedBlockIDs[0] != written.RecordID ||
		len(targetSnapshot.SourceRevisionIDs) != 1 ||
		targetSnapshot.SourceRevisionIDs[0] != original.RevisionID {
		t.Fatalf("published target descriptor missed moved record: %+v, %v", targetSnapshot, err)
	}
	firstRebuild, err := selected.RebuildIndexes(ctx)
	if err != nil {
		t.Fatalf("published index rebuild failed: %v", err)
	}
	secondRebuild, err := selected.RebuildIndexes(ctx)
	if err != nil || secondRebuild["index_sha256"] != firstRebuild["index_sha256"] ||
		firstRebuild["record_count"] != 1 {
		t.Fatalf("published index rebuild was unstable: %+v, %+v, %v",
			firstRebuild, secondRebuild, err)
	}
	var targetCoverage int
	inspectDB, err := sql.Open("sqlite", selected.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	defer inspectDB.Close()
	if err := inspectDB.QueryRowContext(ctx, `SELECT json_extract(payload_json,
		'$.metadata.coverage.canonical_record_count') FROM index_nodes
		WHERE node_id=?`, "block_descriptor:alpha:"+target.BlockID).Scan(&targetCoverage); err != nil ||
		targetCoverage != 1 {
		t.Fatalf("rebuild omitted the published target descriptor: count=%d err=%v", targetCoverage, err)
	}
	replayed, err := selected.ResumeTopologyEvolution(ctx, "split-one")
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("published split replay changed its receipt: %+v, %v", replayed, err)
	}
	read, err := selected.ReadStored(ctx, written.RecordID)
	if err != nil || read == nil || read.BlockMembership.PrimaryBlockID != target.BlockID ||
		read.BlockMembership.BackendPartitionID != "published-partition" {
		t.Fatalf("stored read missed published membership: %+v, %v", read, err)
	}
	measured, err := selected.BlockHealth(ctx, []string{sourceID, target.BlockID})
	if err != nil {
		t.Fatal(err)
	}
	measuredRows := measured["block_metrics"].([]map[string]any)
	if len(measuredRows) != 1 || measuredRows[0]["primary_block_id"] != target.BlockID ||
		measuredRows[0]["record_count"] != 1 {
		t.Fatalf("published target metrics missed effective ownership: %+v", measuredRows)
	}
	projection, err := selected.index.ProjectionRows(ctx)
	if err != nil || len(projection) != 1 ||
		projection[0].Record.BlockMembership.PrimaryBlockID != target.BlockID {
		t.Fatalf("projection rows missed published membership: %+v, %v", projection, err)
	}
	if projection[0].CanonicalJSON != "" {
		t.Fatal("published overlay retained a payload for the old membership")
	}
	recent, err := selected.Recent(ctx, RecentRequest{Limit: 10, Scope: "workspace",
		ScopeID: "alpha", LogicalBlockID: target.BlockID, IncludeCandidates: true})
	if err != nil || len(recent) != 1 || recent[0].BlockID != written.RecordID {
		t.Fatalf("recent target read missed published record: %+v, %v", recent, err)
	}
	old, err := selected.Recent(ctx, RecentRequest{Limit: 10, Scope: "workspace",
		ScopeID: "alpha", LogicalBlockID: sourceID, IncludeCandidates: true})
	if err != nil || len(old) != 0 {
		t.Fatalf("archived source still claims published record: %+v, %v", old, err)
	}
	page, err := selected.ListRecords(ctx, ListRecordsRequest{Scope: "workspace",
		ScopeID: "alpha", LogicalBlockID: target.BlockID, Limit: 10})
	if err != nil || len(page.Records) != 1 ||
		page.Records[0]["logical_block_id"] != target.BlockID {
		t.Fatalf("list_records target read missed published record: %+v, %v", page, err)
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID, plan.RunID = "alpha", "team", "session-1"
	plan.RecordIDs = []string{written.RecordID}
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	plan.LogicalBlockIDs = []string{target.BlockID}
	exact, err := selected.index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockMembership.PrimaryBlockID != target.BlockID {
		t.Fatalf("exact recall missed published target: %+v, %v", exact, err)
	}
	plan.LogicalBlockIDs = []string{sourceID}
	oldExact, err := selected.index.ReadExactRecords(ctx, plan)
	if err != nil || len(oldExact) != 0 {
		t.Fatalf("exact recall returned archived source: %+v, %v", oldExact, err)
	}
	partition, err := mnemosyne.PartitionID(*read)
	if err != nil {
		t.Fatal(err)
	}
	partitionHash := sha256.Sum256([]byte(partition))
	nativeProjector, err := mnemosyne.NewProjector(repo, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	nativePath := filepath.Join(nativeProjector.DataDir, "partitions",
		hex.EncodeToString(partitionHash[:]), "mnemosyne.db")
	nativeDB, err := sql.Open("sqlite", nativePath)
	if err != nil {
		t.Fatal(err)
	}
	var targetNativeID, targetRecordID string
	err = nativeDB.QueryRowContext(ctx, `SELECT id,json_extract(metadata_json,'$.block_id')
		FROM working_memory WHERE source=?`, "um_mem:block:"+target.BlockID).Scan(&targetNativeID, &targetRecordID)
	var targetRows int
	countErr := nativeDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM working_memory WHERE source=?`,
		"um_mem:block:"+target.BlockID).Scan(&targetRows)
	nativeDB.Close()
	if err != nil || countErr != nil || targetRows != 1 {
		t.Fatalf("staged target native row is unavailable or duplicated: %d, %v, %v", targetRows, err, countErr)
	}
	if targetNativeID == stagedRows[0].NativeID || targetRecordID != written.RecordID {
		t.Fatalf("staged native target identity differs: source=%q target=%q record=%q",
			stagedRows[0].NativeID, targetNativeID, targetRecordID)
	}
	semantic := memory.NewRecallPlan("durable source fact")
	semantic.WorkspaceID, semantic.ProfileID, semantic.RunID = "alpha", "team", "session-1"
	semantic.ScopeChain = []string{"workspace:alpha"}
	semantic.LogicalBlockIDs = []string{target.BlockID}
	semantic.IncludeCandidates = true
	remembered, err := selected.Recall(ctx, semantic)
	if err != nil || len(remembered) != 1 ||
		remembered[0].BlockMembership.PrimaryBlockID != target.BlockID {
		t.Fatalf("native target recall missed staged projection: %+v, %v", remembered, err)
	}
	raw, err := store.OpenIndex(selected.IndexPath(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	canonical, err := raw.ReadStoredRecord(ctx, written.RecordID)
	if err != nil || canonical == nil || canonical.BlockMembership.PrimaryBlockID != sourceID {
		t.Fatalf("published pointer rewrote immutable canonical payload: %+v, %v", canonical, err)
	}
	reopened, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resumed, err := reopened.ReadStored(ctx, written.RecordID)
	if err != nil || resumed == nil || resumed.BlockMembership.PrimaryBlockID != target.BlockID {
		t.Fatalf("published membership was lost after restart: %+v, %v", resumed, err)
	}
	if state, err := reopened.topology.GetBlock(ctx, target.BlockID); err != nil ||
		state == nil || state.Status != "active" {
		t.Fatalf("published target is unavailable after restart: %+v, %v", state, err)
	}
	if pointer, err := reopened.topology.PublishedRecordMembership(ctx, written.RecordID,
		original.RevisionID); err != nil || pointer == nil || pointer.BlockID != target.BlockID {
		t.Fatalf("exact published pointer is unavailable after restart: %+v, %v", pointer, err)
	}
}
