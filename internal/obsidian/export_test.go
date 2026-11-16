package obsidian

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestExportBuildsScopedVaultAndRetainsOtherWorkspaces(t *testing.T) {
	vault := t.TempDir()
	otherProject := filepath.Join(vault, "UM-MEM", "Projects", "other")
	if err := os.MkdirAll(otherProject, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherProject, "Index.md"), []byte("other workspace"), 0644); err != nil {
		t.Fatal(err)
	}
	record := exportFixture()
	record.Title, record.BlockID, record.Content = "Scoped Recall", "mem-a", "Use scoped recall."
	linked := record
	linked.Title, linked.BlockID, linked.Content = "Isolation Rule", "mem-b", "Do not load unrelated memory."
	evidence := record
	evidence.BlockID, evidence.Title, evidence.Kind, evidence.Status = "session_summary_20260627_210403_ab12", "Session Summary", "session_summary", "candidate"
	protected := record
	protected.BlockID, protected.Sensitivity = "secret-block", "secret"
	foreign := record
	foreign.BlockID, foreign.WorkspaceID = "foreign-block", "other"
	edge := memory.EdgeRecord{SrcID: record.BlockID, DstID: linked.BlockID, EdgeType: "supports", Reason: "Exact reason"}
	result, err := Write(vault, "alpha", "Alpha Project", nil,
		[]memory.MemoryRecord{record, linked, evidence, protected, foreign}, []memory.EdgeRecord{edge})
	if err != nil {
		t.Fatal(err)
	}
	if result.BlocksExported != 2 || len(result.FilesWritten) < 10 {
		t.Fatalf("unexpected export result: %+v", result)
	}
	first := filepath.Join(vault, "UM-MEM", "Projects", "alpha", "Core", "Project knowledge", "Scoped Recall - mem-a.md")
	content, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Supports: [[Isolation Rule - mem-b]]") ||
		!strings.Contains(string(content), "Exact reason") {
		t.Fatalf("graph edge missing from note: %s", content)
	}
	index, err := os.ReadFile(result.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), "secret-block") || strings.Contains(string(index), "foreign-block") ||
		!strings.Contains(string(index), "Core/Core Index") {
		t.Fatalf("index leaked or lost scope: %s", index)
	}
	evidencePath := filepath.Join(vault, "UM-MEM", "Projects", "alpha", "Evidence", "Sessions", "Session Summary - session_summary_20260627_210403_ab12.md")
	if _, err := os.Stat(evidencePath); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(otherProject, "Index.md")); err != nil || string(data) != "other workspace" {
		t.Fatalf("other workspace changed: %s, %v", data, err)
	}
	if err := os.WriteFile(filepath.Join(vault, "UM-MEM", "Projects", "alpha", "Core", "stale.md"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(vault, "alpha", "Alpha Project", nil, []memory.MemoryRecord{record}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vault, "UM-MEM", "Projects", "alpha", "Core", "stale.md")); !os.IsNotExist(err) {
		t.Fatalf("stale generated note was retained: %v", err)
	}
}

func TestExportRejectsWorkspaceSymlink(t *testing.T) {
	vault := t.TempDir()
	outside := t.TempDir()
	projects := filepath.Join(vault, "UM-MEM", "Projects")
	if err := os.MkdirAll(projects, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projects, "alpha")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Write(vault, "alpha", "", nil, nil, nil); err == nil {
		t.Fatal("export followed workspace symlink")
	}
}
