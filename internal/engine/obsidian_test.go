package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestEngineObsidianExportReadsCommittedCanonicalRecord(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	record, err := memory.NewMemoryRecord("A durable exact fact for Obsidian", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	record.ProfileID = "team"
	record.Title = "Obsidian fact"
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
	vault := filepath.Join(repo, "vault")
	result, err := selected.ExportObsidian(ctx, vault, "Alpha", []string{"workspace:alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if result["blocks_exported"] != 0 {
		t.Fatalf("candidate was incorrectly promoted into the core tree: %+v", result)
	}
	note := filepath.Join(vault, "UM-MEM", "Projects", "alpha", "Evidence", "Candidate ideas",
		"Obsidian fact - "+record.BlockID+".md")
	data, err := os.ReadFile(note)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "A durable exact fact for Obsidian") ||
		!strings.Contains(string(data), "workspace_id: alpha") {
		t.Fatalf("canonical content changed: %s", data)
	}
}
