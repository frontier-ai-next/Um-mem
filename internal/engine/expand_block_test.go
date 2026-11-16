package engine

import (
	"context"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
)

func TestExpandBlockPagesNestedDynamicAndStaticCatalog(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	opened, err := catalog.Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	states, err := opened.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	root := states[0].Block
	branch := root
	branch.BlockID, branch.ParentID, branch.Name = "branch", root.BlockID, "Branch"
	branch.BlockType = "workspace_dynamic_index"
	branch.LifecyclePolicy = map[string]any{"initial_status": "candidate", "mutable": true,
		"terminal_statuses": []string{"superseded", "deprecated", "retracted"}}
	if _, err := opened.CreateBlock(ctx, branch, catalog.CreateOptions{Actor: "test", Reason: "branch"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		leaf := branch
		leaf.BlockID, leaf.ParentID, leaf.Name = id, branch.BlockID, id
		leaf.BlockType, leaf.IndexOnly, leaf.AgentWritable = "workspace_dynamic", false, true
		leaf.TokenBudget = 128
		if _, err := opened.CreateBlock(ctx, leaf, catalog.CreateOptions{
			Actor: "test", Reason: "child", BackendPartitionID: id + "-partition"}); err != nil {
			t.Fatal(err)
		}
	}
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	first, err := selected.ExpandBlock(ctx, branch.BlockID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if first["child_count"] != 3 || first["next_cursor"] == "" ||
		len(first["children"].([]map[string]any)) != 1 ||
		first["root"].(map[string]any)["child_ids"].([]string)[0] != "a" ||
		len(first["path_ids"].([]string)) != 2 {
		t.Fatalf("nested branch first page is invalid: %+v", first)
	}
	second, err := selected.ExpandBlock(ctx, branch.BlockID, 1, first["next_cursor"].(string))
	if err != nil || second["children"].([]map[string]any)[0]["block_id"] != "b" {
		t.Fatalf("nested branch second page is invalid: %+v, %v", second, err)
	}
	meta, err := selected.ExpandBlock(ctx, "memory_root_index", 1, "")
	if err != nil || meta["next_cursor"] == "" || len(meta["children"].([]map[string]any)) != 1 {
		t.Fatalf("protected taxonomy first page is invalid: %+v, %v", meta, err)
	}
	metaSecond, err := selected.ExpandBlock(ctx, "memory_root_index", 1, meta["next_cursor"].(string))
	if err != nil || metaSecond["children"].([]map[string]any)[0]["block_id"] ==
		meta["children"].([]map[string]any)[0]["block_id"] {
		t.Fatalf("protected taxonomy second page is invalid: %+v, %v", metaSecond, err)
	}
	if _, err := selected.ExpandBlock(ctx, root.BlockID, 1, first["next_cursor"].(string)); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("cursor from another branch was accepted: %v", err)
	}
	if _, err := opened.AddBlockEdge(ctx, "a", "b", "related_to", "revision", "test"); err != nil {
		t.Fatal(err)
	}
	linked, err := selected.ExpandBlock(ctx, "a", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	related := linked["root"].(map[string]any)["links"].(map[string]any)["related_block_ids"].([]string)
	if len(related) != 1 || related[0] != "b" ||
		linked["root"].(map[string]any)["related_block_ids_complete"] != true {
		t.Fatalf("expanded block lost cross-branch relation: %+v", linked)
	}
	if _, err := selected.ExpandBlock(ctx, branch.BlockID, 1, first["next_cursor"].(string)); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("stale branch cursor was accepted: %v", err)
	}
}
