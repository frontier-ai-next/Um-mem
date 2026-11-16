package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestReconcileInventoryRejectsDanglingJournalRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "UM_MEM_index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO write_outbox(block_id,workspace_id,store,payload_id,
		payload_sha256,status,error,created_at,updated_at)
		VALUES('missing','alpha','hindsight','record:missing','missing','pending','','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := index.UnresolvedOutbox(ctx); err == nil {
		t.Fatal("dangling native outbox was treated as a clean inventory")
	}
	if _, err := db.Exec(`INSERT INTO canonical_head_outbox(workspace_id,canonical_key_sha256,
		predecessor_id,successor_id,successor_status,successor_authority,mutation_action,
		rollback_target_id,payload_id,payload_sha256,status,error,created_at,updated_at)
		VALUES('alpha','hash','','missing','candidate','advisory','insert','','mutation:missing',
		'missing','pending','','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := index.HeadInventory(ctx); err == nil {
		t.Fatal("dangling canonical head was treated as a clean inventory")
	}
}
