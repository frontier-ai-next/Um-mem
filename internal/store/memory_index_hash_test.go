package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMemoryIndexSHA256MatchesEmptyPythonPayloadAndTracksDerivedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "UM_MEM_index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	empty, err := index.MemoryIndexSHA256(ctx)
	if err != nil || empty != "9c76e95ba6a78daa62089d5917cdc922c1b0b3f7c84f65f128dd7c862e604955" {
		t.Fatalf("empty Python index digest differs: %q, %v", empty, err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `INSERT INTO memory_exact_terms(
		workspace_id,term_type,term_value,block_id) VALUES('alpha','path','src/main.go','record-1')`); err != nil {
		t.Fatal(err)
	}
	changed, err := index.MemoryIndexSHA256(ctx)
	if err != nil || changed == empty {
		t.Fatalf("derived write did not change index digest: %q, %v", changed, err)
	}
	foreign, err := OpenIndex(path, "beta", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	isolated, err := foreign.MemoryIndexSHA256(ctx)
	if err != nil || isolated != empty {
		t.Fatalf("foreign workspace digest included alpha rows: %q, %v", isolated, err)
	}
}
