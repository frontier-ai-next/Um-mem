package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// Check the real query plan instead of imposing a machine-dependent latency
// threshold. Without a block/revision index, the correlated journal lookup
// scans unrelated writes once per canonical record.
func assertProjectionJournalLookupIndexed(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+projectionRowsQuery, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
		if strings.Contains(detail, "write_outbox USING COVERING INDEX idx_write_outbox_block_revision (block_id=?)") {
			indexed = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatalf("projection lookup scans unrelated journal rows: %v", details)
	}
}

func TestProjectionJournalLookupIndexedOnFreshStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	assertProjectionJournalLookupIndexed(t, index.db)
}

func TestWriterAddsProjectionJournalIndexToExistingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce a version-16 store created before the performance fix.
	if _, err := db.Exec("DROP INDEX IF EXISTS idx_write_outbox_block_revision"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		writer, err := OpenWriter(path, "alpha", "team", "mnemosyne")
		if err != nil {
			t.Fatal(err)
		}
		assertProjectionJournalLookupIndexed(t, writer.db)
		var version int
		if err := writer.db.QueryRowContext(context.Background(),
			"SELECT value FROM schema_metadata WHERE key='schema_version'").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != IndexSchemaVersion {
			t.Fatalf("index installation changed schema version: %d", version)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
