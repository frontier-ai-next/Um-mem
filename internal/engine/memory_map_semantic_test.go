package engine

import (
	"context"
	"database/sql"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/memorymap"
	"github.com/frontier-ai-next/Um-mem/internal/paths"
)

func TestMemoryMapReadsLegacySemanticVectorsWithoutPython(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	root, err := paths.WorkspaceMemoryRoot(selected.config.RepoRoot, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "semantic")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "embeddings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE semantic_vectors (
		model TEXT, profile TEXT, record_id TEXT, variant TEXT, vector BLOB)`); err != nil {
		t.Fatal(err)
	}
	vector := make([]byte, 8)
	binary.LittleEndian.PutUint32(vector[:4], math.Float32bits(1))
	for _, id := range []string{"first", "second"} {
		if _, err := db.Exec(`INSERT INTO semantic_vectors VALUES (?,?,?,?,?)`,
			semanticMapModel(), "team", id, "full", vector); err != nil {
			t.Fatal(err)
		}
	}
	records := []memorymap.RecordNode{{RecordID: "first"}, {RecordID: "second"}}
	model, indexed, edges, err := selected.memoryMapSemantic(ctx, records)
	if err != nil || model != semanticMapModel() || indexed != 2 || len(edges) != 1 ||
		edges[0].Weight != 1 || !edges[0].Draw {
		t.Fatalf("legacy semantic sidecar was not read: %q %d %+v, %v", model, indexed, edges, err)
	}
	t.Setenv("UM_MEM_SEMANTIC", "off")
	model, indexed, edges, err = selected.memoryMapSemantic(ctx, records)
	if err != nil || model != "" || indexed != 0 || len(edges) != 0 {
		t.Fatalf("disabled semantic tier leaked cached links: %q %d %+v, %v", model, indexed, edges, err)
	}
}
