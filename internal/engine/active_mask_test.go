package engine

import (
	"context"
	"os"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestLatestMaskSelectorLimitsWithinBoundScope(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	opened, err := catalog.Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := opened.ListBlocks(ctx, false)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("catalog root unavailable: %+v, %v", blocks, err)
	}
	leaf := memory.Block{BlockID: "project-focus", BlockType: "semantic_leaf", Temperature: "warm",
		Scope: "project", Authority: "advisory", DeliveryPolicy: "on_demand", Retention: "durable",
		Name: "Project focus", ParentID: blocks[0].Block.BlockID, AgentWritable: true,
		Owner: "test", Producer: "test", Consumers: []string{"memory_agent"}, RecallPath: "hybrid_recall",
		LifecyclePolicy: map[string]any{"initial_status": "active", "mutable": true,
			"terminal_statuses": []string{"archived"}},
		IsolationPolicy: map[string]any{"dimension": "project", "cross_workspace": false}, TokenBudget: 128}
	if _, err := opened.CreateBlock(ctx, leaf, catalog.CreateOptions{
		BackendPartitionID: "project-focus-partition", Actor: "test", Reason: "scoped mask"}); err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	selected.projector = verifiedProjector{}
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	base, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	base.BlockMembership, base.SemanticCatalogVersion = nil, ""
	base.Scope, base.RunID = "project", "session-1"
	for _, entry := range []struct {
		id, project, updated string
	}{
		{"mem_own", "project-own", "2025-01-01T00:00:00Z"},
		{"mem_foreign", "project-foreign", "2026-01-01T00:00:00Z"},
	} {
		record := base
		record.BlockID, record.ProjectID = entry.id, entry.project
		record.RevisionID, record.UpdatedAt = "rev_"+entry.id, entry.updated
		record.Content = "Fact for " + entry.project
		if err := selected.BindRootRecord(ctx, &record, "project-focus"); err != nil {
			t.Fatal(err)
		}
		if _, err := selected.PutRoot(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	mask, err := selected.SetActiveMask(ctx, memory.ActiveMemoryMask{
		SelectedBlockIDs: []string{"project-focus"}, TokenBudget: 512,
		ActivationReason: "bounded project focus", Selectors: []memory.ActiveMemorySelector{{
			BlockID: "project-focus", SelectorType: "latest_records", Limit: 1}}},
		"session-1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	result, err := selected.ResolveActiveMask(ctx, ActiveMaskRequest{
		MaskID: mask.MaskID, SessionID: "session-1", TaskID: "task-1",
		ScopeChain: []string{"project:project-own"}})
	if err != nil || result.Status != "resolved" || len(result.Records) != 1 || result.Records[0].ProjectID != "project-own" {
		t.Fatalf("foreign newest record crowded out bound project: %+v, %v", result, err)
	}
}
