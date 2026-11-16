package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOperationalRevisionsChangeAfterExternalDurableWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`CREATE TABLE schema_metadata (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
		INSERT INTO schema_metadata VALUES ('schema_version', 16);
		CREATE TABLE index_nodes (node_id TEXT PRIMARY KEY);`); err != nil {
		t.Fatal(err)
	}
	index, err := OpenIndex(path, "alpha", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	before, err := index.OperationalRevisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 || before["schema_revision"] != "um-mem-backend-index-schema:16" ||
		before["canonical_snapshot_sha256"] != before["index_revision"] ||
		!strings.HasPrefix(before["index_revision"], "sha256:") ||
		len(before["index_revision"]) != len("sha256:")+64 {
		t.Fatalf("unexpected operational revisions: %+v", before)
	}
	if _, err := writer.Exec("INSERT INTO index_nodes VALUES ('descriptor:release')"); err != nil {
		t.Fatal(err)
	}
	after, err := index.OperationalRevisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after["schema_revision"] != before["schema_revision"] ||
		after["index_revision"] == before["index_revision"] ||
		after["canonical_snapshot_sha256"] != after["index_revision"] ||
		strings.Contains(after["index_revision"], "release") {
		t.Fatalf("external write was not captured without content exposure: before=%+v after=%+v", before, after)
	}
}
