package servicepolicy

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/blockcatalog"
)

func TestBaselineAndPathMatchPython(t *testing.T) {
	snapshot, err := NormalizeSnapshot([]map[string]any{{"block_id": "example", "name": "Example"}})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := Baseline(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.CatalogRevisionID != "catalog:um_mem.memory.blocks.registry.load_memory_blocks@1:sha256:7d217cf4d528296b67abb54221a6c7b31a512168c50cea00212d6fe1cf557452" ||
		bundle.RevisionID != "sha256:bc779c5c90046bd91581555c543931d7889eacc22cbdabf59eb9fe91a2ae7a73" {
		t.Fatalf("baseline policy identity differs from Python: %+v", bundle)
	}
	blockRevision, err := BlockRevision(snapshot)
	if err != nil || blockRevision != "blocks:um_mem.memory.blocks.registry@1:sha256:7d217cf4d528296b67abb54221a6c7b31a512168c50cea00212d6fe1cf557452" {
		t.Fatalf("block revision differs from Python: %s %v", blockRevision, err)
	}
	projectionRevision, err := ProjectionRevision(snapshot)
	if err != nil || projectionRevision != "projection:um_mem.memory.blocks.catalog@1:sha256:7d217cf4d528296b67abb54221a6c7b31a512168c50cea00212d6fe1cf557452" {
		t.Fatalf("projection revision differs from Python: %s %v", projectionRevision, err)
	}
	if runtime.GOOS != "windows" {
		return
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil || !strings.EqualFold(root, `C:\Projects\UM MEM`) {
		return
	}
	path, err := StatePath(Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "mempalace"})
	if err != nil || filepath.Base(path) != "9d8014eceff2af3acacab7eb7eebaf9b8e402bde6a0727bcdfc59cc9cb66a35b.json" {
		t.Fatalf("policy state path differs from Python: %s %v", path, err)
	}
}

func TestPackagedCatalogPolicySnapshotMatchesPython(t *testing.T) {
	blocks, err := blockcatalog.Load(blockcatalog.Options{RepoRoot: t.TempDir(), Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotFromBlocks(blocks)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := CatalogRevision(snapshot)
	if err != nil || revision != "catalog:um_mem.memory.blocks.registry.load_memory_blocks@1:sha256:102d16817c4fe9ee0dc792461e9a49d448806bfa0d9084a6aae2ce1fc20e8c61" {
		t.Fatalf("packaged policy snapshot differs from Python: %s %v", revision, err)
	}
}

func TestSnapshotRejectsContentAndDuplicates(t *testing.T) {
	if _, err := NormalizeSnapshot([]map[string]any{{"block_id": "a", "prompt": "private"}}); err == nil {
		t.Fatal("content entered policy snapshot")
	}
	if _, err := NormalizeSnapshot([]map[string]any{{"block_id": "a"}, {"block_id": "a"}}); err == nil {
		t.Fatal("duplicate block IDs entered policy snapshot")
	}
}
