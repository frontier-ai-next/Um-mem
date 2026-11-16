package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
)

func TestCreateBlockCanWriteWithoutRestartAndRejectsLeafParent(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	opened, err := catalog.Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	states, err := opened.ListBlocks(ctx, false)
	if err != nil || len(states) != 1 {
		t.Fatalf("workspace root is unavailable: %+v, %v", states, err)
	}
	branch, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Research",
		Synopsis: "Evidence and decisions", ParentID: states[0].Block.BlockID,
		Reason: "organize memory", IndexOnly: true})
	if err != nil || branch["ok"] != true || branch["index_only"] != true {
		t.Fatalf("index block creation failed: %+v, %v", branch, err)
	}
	leaf, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Findings",
		Synopsis: "Verified findings", ParentID: branch["block_id"].(string),
		Reason: "store findings", Scope: "workspace"})
	if err != nil || leaf["ok"] != true || leaf["backend_partition_id"] == "" {
		t.Fatalf("leaf creation failed: %+v, %v", leaf, err)
	}
	if _, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Nested",
		Synopsis: "Forbidden child", ParentID: leaf["block_id"].(string),
		Reason: "bad nesting"}); err == nil {
		t.Fatal("content leaf accepted a child")
	}
	updatedName, budget := "Updated findings", 256
	amended, err := selected.AmendBlock(ctx, AmendBlockRequest{BlockID: leaf["block_id"].(string),
		ExpectedRevision: 1, Reason: "rename after review", Name: &updatedName,
		MetadataPatch: map[string]any{"reviewed": true}, TokenBudget: &budget})
	if err != nil || amended["revision"] != 2 || amended["previous_revision"] != 1 ||
		amended["name"] != updatedName {
		t.Fatalf("dynamic leaf amendment failed: %+v, %v", amended, err)
	}
	if _, err := selected.AmendBlock(ctx, AmendBlockRequest{BlockID: leaf["block_id"].(string),
		ExpectedRevision: 1, Reason: "stale update", Name: &updatedName}); err == nil {
		t.Fatal("stale dynamic leaf amendment was accepted")
	}
	result, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content: "The current finding is durable.", RequestedBlockID: leaf["block_id"].(string),
		SessionID: "session-1", RunID: "session-1"})
	if err != nil || !result.Saved {
		t.Fatalf("new dynamic leaf could not accept a write without restart: %+v, %v", result, err)
	}
	record, err := selected.ReadStored(ctx, result.RecordID)
	if err != nil || record == nil || record.BlockMembership.PrimaryBlockID != leaf["block_id"] ||
		record.BlockMembership.BackendPartitionID != leaf["backend_partition_id"] {
		t.Fatalf("new block write lost its physical partition: %+v, %v", record, err)
	}
}

func TestCatalogRefreshPublishesWholeSnapshotsDuringConcurrentReads(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	rootID := selected.Blocks()[len(selected.Blocks())-1].BlockID
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
			break
		}
	}
	var readers sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if len(selected.Blocks()) == 0 || selected.CatalogRevisionID() == "" {
						t.Errorf("reader observed an incomplete catalog snapshot")
						return
					}
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		if _, err := selected.CreateBlock(ctx, CreateBlockRequest{
			Name: fmt.Sprintf("Leaf %d", i), Synopsis: "Concurrent publication",
			ParentID: rootID, Reason: "race check"}); err != nil {
			close(stop)
			readers.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	count := 0
	for _, block := range selected.Blocks() {
		if block.ParentID == rootID {
			count++
		}
	}
	if count != 8 {
		t.Fatalf("catalog refresh lost created blocks: %d", count)
	}
}

func TestArchiveBlockChecksChildrenAndIncomingEdges(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	rootID := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
		}
	}
	branch, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Archive branch",
		Synopsis: "Archive guard", ParentID: rootID, Reason: "test", IndexOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	branchID := branch["block_id"].(string)
	leaf, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Archive leaf",
		Synopsis: "Archive guard", ParentID: branchID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	leafID := leaf["block_id"].(string)
	if _, err := selected.ArchiveBlock(ctx, BlockTransitionRequest{
		BlockID: branchID, ExpectedRevision: 1, Reason: "branch cleanup"}); err == nil {
		t.Fatal("branch with active child was archived")
	}
	edge, err := selected.AddBlockEdge(ctx, rootID, leafID, "related_to", "hold leaf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selected.ArchiveBlock(ctx, BlockTransitionRequest{
		BlockID: leafID, ExpectedRevision: 1, Reason: "leaf cleanup"}); err == nil {
		t.Fatal("leaf with incoming graph relation was archived")
	}
	if _, err := selected.RemoveBlockEdge(ctx, edge.EdgeID, 1, "release leaf"); err != nil {
		t.Fatal(err)
	}
	archived, err := selected.ArchiveBlock(ctx, BlockTransitionRequest{
		BlockID: leafID, ExpectedRevision: 1, Reason: "leaf cleanup"})
	if err != nil || archived["archived"] != true || archived["status"] != "archived" {
		t.Fatalf("eligible leaf archive failed: %+v, %v", archived, err)
	}
	if _, err := selected.ArchiveBlock(ctx, BlockTransitionRequest{
		BlockID: leafID, ExpectedRevision: 1, Reason: "stale archive"}); err == nil {
		t.Fatal("stale archive revision was accepted")
	}
	revived, err := selected.ReviveBlock(ctx, BlockTransitionRequest{
		BlockID: leafID, ExpectedRevision: 2, Reason: "use category again"})
	if err != nil || revived["status"] != "active" || revived["revision"] != 3 {
		t.Fatalf("eligible leaf revival failed: %+v, %v", revived, err)
	}
	remembered, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content: "Revived category is writable.", RequestedBlockID: leafID,
		SessionID: "session-1", RunID: "session-1"})
	if err != nil || !remembered.Saved {
		t.Fatalf("revived leaf could not accept a new record: %+v, %v", remembered, err)
	}
	if _, err := selected.ArchiveBlock(ctx, BlockTransitionRequest{
		BlockID: leafID, ExpectedRevision: 3, Reason: "archive occupied leaf"}); err != nil {
		t.Fatal(err)
	}
	refused, err := selected.DeleteBlock(ctx, DeleteBlockRequest{BlockID: leafID,
		ExpectedRevision: 4, Reason: "try delete occupied leaf"})
	if err != nil || refused["deleted"] != false || refused["records_remaining"] != 1 {
		t.Fatalf("occupied block deletion did not preserve the record: %+v, %v", refused, err)
	}
	current, err := selected.ReadStored(ctx, remembered.RecordID)
	if err != nil || current == nil {
		t.Fatalf("current record was not readable before retraction: %+v, %v", current, err)
	}
	retracted, err := selected.Forget(ctx, ForgetRequest{MemoryID: remembered.RecordID,
		ExpectedRevision: current.RevisionID, Reason: "retire occupied leaf"})
	if err != nil || retracted["saved"] != true {
		t.Fatalf("archived block record retraction failed: %+v, %v", retracted, err)
	}
	oldVersion, err := selected.ReadStored(ctx, remembered.RecordID)
	if err != nil || oldVersion == nil || oldVersion.Status == "retracted" {
		t.Fatalf("retraction erased the historical record: %+v, %v", oldVersion, err)
	}
	newVersion, err := selected.ReadStored(ctx, retracted["block_id"].(string))
	if err != nil || newVersion == nil || newVersion.Status != "retracted" ||
		newVersion.RevisionOf != remembered.RecordID || newVersion.DeliveryPolicy != "hidden" {
		t.Fatalf("retraction did not create a hidden successor: %+v, %v", newVersion, err)
	}
	deleted, err := selected.DeleteBlock(ctx, DeleteBlockRequest{BlockID: leafID,
		ExpectedRevision: 4, Reason: "remove retracted leaf"})
	if err != nil || deleted["deleted"] != true {
		t.Fatalf("retracted block could not be deleted: %+v, %v", deleted, err)
	}
}

func TestDeleteEmptyArchivedBlockWritesTombstone(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	rootID := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
		}
	}
	created, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Disposable",
		Synopsis: "No records", ParentID: rootID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	id := created["block_id"].(string)
	if _, err := selected.DeleteBlock(ctx, DeleteBlockRequest{BlockID: id,
		ExpectedRevision: 1, Reason: "must archive first"}); err == nil {
		t.Fatal("active block was hard deleted")
	}
	if _, err := selected.ArchiveBlock(ctx, BlockTransitionRequest{
		BlockID: id, ExpectedRevision: 1, Reason: "empty leaf"}); err != nil {
		t.Fatal(err)
	}
	deleted, err := selected.DeleteBlock(ctx, DeleteBlockRequest{BlockID: id,
		ExpectedRevision: 2, Reason: "empty leaf"})
	if err != nil || deleted["deleted"] != true || deleted["records_remaining"] != 0 {
		t.Fatalf("empty archived block deletion failed: %+v, %v", deleted, err)
	}
	if _, err := selected.DeleteBlock(ctx, DeleteBlockRequest{BlockID: id,
		ExpectedRevision: 2, Reason: "repeat"}); err == nil {
		t.Fatal("deleted block was accepted again")
	}
}

func TestMoveBlockPreservesTreeAndRejectsCycle(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	rootID := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
		}
	}
	left, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Left", Synopsis: "Left branch",
		ParentID: rootID, Reason: "test", IndexOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	right, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Right", Synopsis: "Right branch",
		ParentID: rootID, Reason: "test", IndexOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Movable", Synopsis: "Movable leaf",
		ParentID: left["block_id"].(string), Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := selected.MoveBlock(ctx, MoveBlockRequest{BlockID: leaf["block_id"].(string),
		ParentID: right["block_id"].(string), ExpectedRevision: 1, Reason: "new category"})
	if err != nil || moved["parent_id"] != right["block_id"] || moved["revision"] != 2 {
		t.Fatalf("leaf move failed: %+v, %v", moved, err)
	}
	if _, err := selected.MoveBlock(ctx, MoveBlockRequest{BlockID: leaf["block_id"].(string),
		ParentID: left["block_id"].(string), ExpectedRevision: 1, Reason: "stale move"}); err == nil {
		t.Fatal("stale move was accepted")
	}
	nested, err := selected.MoveBlock(ctx, MoveBlockRequest{BlockID: right["block_id"].(string),
		ParentID: left["block_id"].(string), ExpectedRevision: 1, Reason: "nest right"})
	if err != nil || nested["parent_id"] != left["block_id"] {
		t.Fatalf("index branch move failed: %+v, %v", nested, err)
	}
	if _, err := selected.MoveBlock(ctx, MoveBlockRequest{BlockID: left["block_id"].(string),
		ParentID: right["block_id"].(string), ExpectedRevision: 1, Reason: "cycle"}); err == nil {
		t.Fatal("cycle-making move was accepted")
	}
	page, err := selected.ExpandBlock(ctx, leaf["block_id"].(string), 50, "")
	if err != nil || len(page["path_ids"].([]string)) != 4 {
		t.Fatalf("moved leaf lost its new containment path: %+v, %v", page, err)
	}
}
