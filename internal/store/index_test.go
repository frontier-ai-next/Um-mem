package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
	_ "modernc.org/sqlite"
)

func TestIndexReadIsScopedAndReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE schema_metadata (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
		INSERT INTO schema_metadata VALUES ('schema_version', 16);
		CREATE TABLE blocks (block_id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, payload_json TEXT NOT NULL);
		CREATE TABLE canonical_head_outbox (commit_revision INTEGER PRIMARY KEY, successor_id TEXT NOT NULL, status TEXT NOT NULL);
		CREATE TABLE write_outbox (commit_revision INTEGER PRIMARY KEY, block_id TEXT NOT NULL, status TEXT NOT NULL);
	`); err != nil {
		t.Fatal(err)
	}
	record, err := memory.NewMemoryRecord("durable fact", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO blocks VALUES (?, ?, ?)", record.BlockID, "alpha", string(payload)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	index, err := OpenIndex(path, "alpha", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if _, err := index.db.Exec("INSERT INTO blocks VALUES ('forbidden', 'alpha', '{}')"); err == nil {
		t.Fatal("read-only index accepted a write")
	}
	got, err := index.ReadStoredRecord(context.Background(), record.BlockID)
	if err != nil || got == nil || got.Content != record.Content {
		t.Fatalf("read = %v, %v", got, err)
	}
	foreign, err := OpenIndex(path, "beta", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	got, err = foreign.ReadStoredRecord(context.Background(), record.BlockID)
	if err != nil || got != nil {
		t.Fatalf("cross-workspace read = %v, %v", got, err)
	}
	wrongProfile, err := OpenIndex(path, "alpha", "other")
	if err != nil {
		t.Fatal(err)
	}
	defer wrongProfile.Close()
	got, err = wrongProfile.ReadStoredRecord(context.Background(), record.BlockID)
	if err != nil || got != nil {
		t.Fatalf("cross-profile read = %v, %v", got, err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec("INSERT INTO write_outbox VALUES (1, ?, 'failed')", record.BlockID); err != nil {
		t.Fatal(err)
	}
	got, err = index.ReadStoredRecord(context.Background(), record.BlockID)
	if err != nil || got != nil {
		t.Fatalf("failed outbox became visible: %v, %v", got, err)
	}
}
