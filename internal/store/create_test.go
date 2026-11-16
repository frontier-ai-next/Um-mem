package store

import (
	"path/filepath"
	"testing"
)

func TestCreateIndexUsesCanonicalSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "UM_MEM_index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	if err := CreateIndex(path); err == nil {
		t.Fatal("existing canonical store was reinitialized")
	}
	index, err := OpenIndex(path, "alpha", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	var count int
	if err := index.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('blocks', 'write_outbox', 'record_block_memberships', 'memory_fts')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("canonical schema has %d required tables", count)
	}
}
