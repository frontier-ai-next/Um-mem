package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestMemoryMapKeepsExactRecordAndEmbeddedBrowserAssets(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	record, err := memory.NewMemoryRecord("exact map content </script> survives", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	record.ProfileID = "team"
	record.Title = "Map fixture"
	record.CanonicalKey, err = memory.DeriveCanonicalKey("alpha", "team", record.Kind, record.BlockID)
	if err != nil {
		t.Fatal(err)
	}
	record.DuplicateClusterID, err = memory.DuplicateClusterID(record)
	if err != nil {
		t.Fatal(err)
	}
	record.RevisionID, err = memory.RevisionIDForRecord(record.CanonicalKey, record, "insert", "")
	if err != nil {
		t.Fatal(err)
	}
	record.BlockID = memory.BlockIDForCanonicalKey(record.CanonicalKey)
	if err := selected.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PutRoot(ctx, record); err != nil {
		t.Fatal(err)
	}
	data, err := selected.CollectMemoryMap(ctx)
	if err != nil || data.RecordCount != 1 || data.Records[0].RecordID != record.BlockID ||
		data.Kinds["observation"] != 1 || data.BlockCount < 29 {
		t.Fatalf("memory map did not collect current scoped records: %+v, %v", data, err)
	}
	result, err := selected.WriteMemoryMap(ctx, "")
	if err != nil || result["ok"] != true {
		t.Fatalf("memory map HTML was not written: %+v, %v", result, err)
	}
	encoded, err := os.ReadFile(result["path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	page := string(encoded)
	if !strings.Contains(page, "exact map content <\\/script> survives") ||
		!strings.Contains(page, "const ") || strings.Contains(page, "__SCRIPT__") {
		t.Fatal("memory map lost its escaped exact content or browser script")
	}
	if _, err := selected.WriteMemoryMap(ctx, ""); err != nil {
		t.Fatalf("memory map could not replace its previous snapshot: %v", err)
	}
}
