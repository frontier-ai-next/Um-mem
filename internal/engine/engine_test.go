package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type verifiedProjector struct{}

func (verifiedProjector) BackendID() string                       { return "hindsight" }
func (verifiedProjector) RequiresNative(memory.MemoryRecord) bool { return true }
func (verifiedProjector) Project(_ context.Context, record memory.MemoryRecord, _ string, _ int64) (store.ProjectionResult, error) {
	return store.ProjectionResult{NativeID: "UM-MEM:block:" + record.BlockID, Verified: true}, nil
}

func TestHindsightEngineRootWriteExactReadAndRestart(t *testing.T) {
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
	config := Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"}
	engine, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if filepath.Base(engine.IndexPath()) != "UM_MEM_index.sqlite" {
		t.Fatalf("canonical index path differs: %s", engine.IndexPath())
	}
	if len(engine.Blocks()) != 29 || engine.CatalogRevisionID() == "" {
		t.Fatal("engine did not bind packaged and workspace catalog")
	}
	if err := engine.BindRootRecord(ctx, &record, "memory_root_index"); err == nil {
		t.Fatal("index-only block admitted a canonical write")
	}
	if err := engine.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	if record.BlockMembership.CatalogRevisionID != engine.CatalogRevisionID() ||
		record.BlockMembership.BackendPartitionID != memory.StaticBackendPartitionID("team", "stable_project_facts") {
		t.Fatalf("record was bound to wrong semantic catalog or partition: %+v", record.BlockMembership)
	}
	engine.projector = verifiedProjector{}
	stored, err := engine.PutRoot(ctx, record)
	if err != nil || stored.CommitRevision < 1 || stored.ProjectionPending {
		t.Fatalf("canonical/native write did not commit: %+v, %v", stored, err)
	}
	partitions, err := engine.index.NativePartitionMemberships(ctx)
	if err != nil || len(partitions) != 1 ||
		partitions[0].LogicalBlockID != "stable_project_facts" ||
		partitions[0].BackendPartitionID != record.BlockMembership.BackendPartitionID {
		t.Fatalf("packaged static leaf lost its native partition: %+v, %v", partitions, err)
	}
	grantedIDs, err := engine.index.RecordIDsForLogicalBlocks(ctx, []string{"stable_project_facts"})
	if err != nil || len(grantedIDs) != 1 || grantedIDs[0] != record.BlockID {
		t.Fatalf("exact static leaf grant lost the committed record: %+v, %v", grantedIDs, err)
	}
	read, err := engine.ReadStored(ctx, record.BlockID)
	if err != nil || read == nil || read.Content != record.Content {
		t.Fatalf("committed record was not readable: %+v, %v", read, err)
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.RecordIDs = []string{record.BlockID}
	plan.SearchAll = true
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	hits, err := engine.Recall(ctx, plan)
	if err != nil || len(hits) != 1 || hits[0].BlockID != record.BlockID {
		t.Fatalf("exact engine recall failed: %+v, %v", hits, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.CatalogRevisionID() != record.BlockMembership.CatalogRevisionID {
		t.Fatal("catalog revision changed after restart")
	}
	read, err = reopened.ReadStored(ctx, record.BlockID)
	if err != nil || read == nil || read.RevisionID != record.RevisionID {
		t.Fatalf("Go engine lost canonical record after restart: %+v, %v", read, err)
	}
	other, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "other", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if read, err := other.ReadStored(ctx, record.BlockID); err != nil || read != nil {
		t.Fatalf("record crossed profile boundary: %+v, %v", read, err)
	}
	if ids, err := other.index.RecordIDsForLogicalBlocks(ctx, []string{"stable_project_facts"}); err != nil || len(ids) != 0 {
		t.Fatalf("exact block grant crossed profile boundary: %+v, %v", ids, err)
	}
}

func TestEngineFailsClosedForUnknownNativeBackend(t *testing.T) {
	_, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "unknown"})
	if err == nil {
		t.Fatal("unknown native backend was advertised as operational")
	}
}

func TestEngineReconcilesInterruptedNativeProjection(t *testing.T) {
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
	record.CanonicalKey = "alpha:reconcile"
	engine, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx := context.Background()
	if err := engine.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.writer.PrepareRootWrite(ctx, record); err != nil {
		t.Fatal(err)
	}
	before, err := engine.Reconcile(ctx, true)
	if err != nil || before.PendingCanonicalHeads != 1 || before.PendingWriteOutbox != 1 || before.Repaired != 0 || before.ResultCode != "inventory" {
		t.Fatalf("pending inventory differs: %+v, %v", before, err)
	}
	if visible, err := engine.ReadStored(ctx, record.BlockID); err != nil || visible != nil {
		t.Fatalf("staged record became visible before native repair: %+v, %v", visible, err)
	}
	engine.projector = verifiedProjector{}
	after, err := engine.Reconcile(ctx, false)
	if err != nil || after.Repaired != 2 || after.Remaining != 0 || after.ResultCode != "repaired" {
		t.Fatalf("pending native projection was not repaired: %+v, %v", after, err)
	}
	if visible, err := engine.ReadStored(ctx, record.BlockID); err != nil || visible == nil {
		t.Fatalf("repaired record remained invisible: %+v, %v", visible, err)
	}
}
