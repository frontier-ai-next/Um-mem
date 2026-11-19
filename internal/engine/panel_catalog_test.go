package engine

import (
	"context"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestPanelSourceEdgesUseExactScopedHumanEvidence(t *testing.T) {
	ref := memory.EvidenceRef{RefType: "message", RefID: "original-message", ProducedBy: "human", ProducerID: "host-user"}
	quote := memory.MemoryRecord{BlockID: "quote", Kind: "source_excerpt", WorkspaceID: "alpha",
		ProfileID: "team", Scope: "workspace", EvidenceRefs: []memory.EvidenceRef{ref}}
	note := quote
	note.BlockID, note.Kind = "note", "note"
	foreign := note
	foreign.BlockID, foreign.ProfileID = "foreign", "other"
	mismatch := note
	mismatch.BlockID = "unrelated"
	differentRef := ref
	differentRef.ProducerID = "other-user"
	mismatch.EvidenceRefs = []memory.EvidenceRef{differentRef}

	edges := panelSourceEdges([]memory.MemoryRecord{quote, note, foreign, mismatch}, 10)
	if len(edges) != 1 || edges[0].SrcID != "note" || edges[0].DstID != "quote" ||
		edges[0].EdgeType != "cites" || len(edges[0].EvidenceRefs) != 1 {
		t.Fatalf("unexpected evidence projection: %+v", edges)
	}
	if edges := panelSourceEdges([]memory.MemoryRecord{quote, note}, 0); len(edges) != 0 {
		t.Fatalf("projection ignored the edge budget: %+v", edges)
	}
}

func TestPanelCatalogMergesPackagedTaxonomy(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	selected.projector = verifiedProjector{}
	written, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "A durable project fact",
		Title: "Packaged block fact", RequestedBlockID: "stable_project_facts",
		Tags: []string{"source"}, SourceID: "source-1", EvidenceKind: "agent_tool_claim"})
	if err != nil || !written.Saved {
		t.Fatalf("packaged block write failed: %+v, %v", written, err)
	}
	catalog, err := selected.PanelCatalog(ctx, "workspace", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	blocks, ok := catalog["blocks"].([]map[string]any)
	if !ok || len(blocks) < 2 {
		t.Fatalf("panel catalog must merge packaged blocks: %+v", catalog["blocks"])
	}
	byID := make(map[string]map[string]any, len(blocks))
	for _, block := range blocks {
		id, _ := block["block_id"].(string)
		if id == "" || byID[id] != nil {
			t.Fatalf("panel catalog block identity is not unique: %q", id)
		}
		byID[id] = block
	}
	if root := byID["memory_root_index"]; root == nil || root["meta"] != true ||
		root["node_kind"] != "meta" || root["writable"] != false {
		t.Fatalf("packaged taxonomy root is missing or writable: %+v", root)
	}
	fact := byID["stable_project_facts"]
	if fact == nil || fact["meta"] != true || fact["scope"] != "workspace" ||
		fact["canonical_record_count"] != 1 || fact["record_count"] != 1 {
		t.Fatalf("packaged leaf lost its canonical coverage: %+v", fact)
	}
	project := byID["project_scope_stable_project_facts"]
	if project == nil || project["meta"] != true || project["scope"] != "project" ||
		project["canonical_record_count"] != 0 {
		t.Fatalf("packaged project leaf is missing: %+v", project)
	}
	dynamic := 0
	for _, block := range blocks {
		if block["node_kind"] == "workspace" && block["parent_id"] == "" {
			dynamic++
		}
	}
	if dynamic != 1 {
		t.Fatalf("panel catalog lost the dynamic workspace root: %d", dynamic)
	}
	projectWritten, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content: "A scoped project fact", Title: "Scoped fact",
		RequestedBlockID: "project_scope_stable_project_facts", ProjectID: "project-a",
		SourceID: "source-2", EvidenceKind: "agent_tool_claim"})
	if err != nil || !projectWritten.Saved {
		t.Fatalf("project write failed: %+v, %v", projectWritten, err)
	}
	workspaceCatalog, err := selected.PanelCatalog(ctx, "workspace", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	projectCatalog, err := selected.PanelCatalog(ctx, "project", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []struct {
		name       string
		catalog    map[string]any
		workspaceN int
		projectN   int
	}{
		{"workspace", workspaceCatalog, 1, 0},
		{"project", projectCatalog, 0, 1},
	} {
		counts := map[string]int{}
		for _, block := range snapshot.catalog["blocks"].([]map[string]any) {
			counts[block["block_id"].(string)] = block["canonical_record_count"].(int)
		}
		if counts["stable_project_facts"] != snapshot.workspaceN ||
			counts["project_scope_stable_project_facts"] != snapshot.projectN {
			t.Fatalf("%s catalog mixed scopes: %+v", snapshot.name, counts)
		}
		ids := snapshot.catalog["project_ids"].([]string)
		if len(ids) != 1 || ids[0] != "project-a" {
			t.Fatalf("%s catalog lost discoverable project: %+v", snapshot.name, ids)
		}
	}
}
