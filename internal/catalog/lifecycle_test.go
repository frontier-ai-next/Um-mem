package catalog

import (
	"context"
	"testing"
)

func TestCatalogProtectionAndTombstoneLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	root := rows[0]
	if _, err := store.DeleteBlock(ctx, root, root.Revision, "test", "try root"); err == nil {
		t.Fatal("protected root was deleted")
	}
	protected, err := store.CreateBlock(ctx, leafBlock("protected-leaf", root.Block.BlockID),
		CreateOptions{Actor: "test", Reason: "create protected leaf"})
	if err != nil {
		t.Fatal(err)
	}
	protected, err = store.ProtectBlock(ctx, protected, 1, "test", "pin contract")
	if err != nil || !protected.Protected || protected.Revision != 2 {
		t.Fatalf("protection failed: %+v, %v", protected, err)
	}
	if _, err := store.ProtectBlock(ctx, protected, 1, "test", "stale"); err == nil {
		t.Fatal("stale protection was accepted")
	}
	if _, err := store.DeleteBlock(ctx, protected, 2, "test", "try protected"); err == nil {
		t.Fatal("protected block was deleted")
	}
	state, err := store.CreateBlock(ctx, leafBlock("delete-leaf", root.Block.BlockID),
		CreateOptions{Actor: "test", Reason: "create deletable leaf"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.ReplaceBlock(ctx, state, state.Block, ReplaceOptions{
		ExpectedRevision: 1, Status: "archived", Actor: "test", Reason: "archive",
		Operation: "archive_block",
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := blockStateMapping(state)
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint, err := fingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	previousRevision, err := store.CatalogRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteBlock(ctx, state, 1, "test", "stale delete"); err == nil {
		t.Fatal("stale delete was accepted")
	}
	if revision, err := store.CatalogRevision(ctx); err != nil || revision != previousRevision {
		t.Fatalf("stale delete changed catalog revision: %d, %v", revision, err)
	}
	tombstone, err := store.DeleteBlock(ctx, state, state.Revision, "test", "retire")
	if err != nil || tombstone.Fingerprint != wantFingerprint || tombstone.Revision != 2 {
		t.Fatalf("tombstone differs: %+v, %v", tombstone, err)
	}
	if found, err := store.GetBlock(ctx, state.Block.BlockID); err != nil || found != nil {
		t.Fatalf("deleted block remained visible: %+v, %v", found, err)
	}
	stored, err := store.GetTombstone(ctx, state.Block.BlockID)
	if err != nil || stored == nil || *stored != tombstone {
		t.Fatalf("tombstone did not persist: %+v, %v", stored, err)
	}
	if revision, err := store.CatalogRevision(ctx); err != nil || revision != previousRevision+1 {
		t.Fatalf("delete did not advance catalog revision: %d, %v", revision, err)
	}
}
