package blockcatalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestPackagedBlocksMatchPythonRegistry(t *testing.T) {
	blocks, err := Load(Options{RepoRoot: t.TempDir(), Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 28 {
		t.Fatalf("expected 28 packaged blocks, got %d", len(blocks))
	}
	actual, err := jsoncompat.MarshalPython(blocks, true)
	if err != nil {
		t.Fatal(err)
	}
	python, err := os.ReadFile("data/python_defaults_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(actual)) != strings.TrimSpace(string(python)) {
		t.Fatal("packaged Go catalog differs from Python registry fixture")
	}
}

func TestSemanticCatalogRevisionMatchesPython(t *testing.T) {
	blocks, err := Load(Options{RepoRoot: t.TempDir(), Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := memory.CompileSemanticCatalog(blocks)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := jsoncompat.MarshalPython(catalog, false)
	if err != nil {
		t.Fatal(err)
	}
	python, err := os.ReadFile("data/python_catalog_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(actual)) != strings.TrimSpace(string(python)) {
		t.Fatalf("semantic catalog differs from Python; Go revision: %s", catalog.RevisionID)
	}
}

func TestWorkspaceOverlayAndProfileScopedTopology(t *testing.T) {
	repo := t.TempDir()
	workspaceFile := filepath.Join(repo, "workspaces", "alpha", ".memory", "blocks.yaml")
	if err := os.MkdirAll(filepath.Dir(workspaceFile), 0o700); err != nil {
		t.Fatal(err)
	}
	override := `blocks:
  - block_id: stable_project_facts
    block_type: semantic_fact
    temperature: warm
    scope: workspace
    authority: advisory
    delivery_policy: on_demand
    retention: durable
    name: Scoped facts
    parent_id: semantic_memory_index
    producer: agent
    consumers: [memory_agent]
    recall_path: hybrid_recall
    lifecycle_policy: {initial_status: active, mutable: true, terminal_statuses: [archived]}
    isolation_policy: {dimension: workspace, cross_workspace: false}
  - block_id: custom_leaf
    block_type: custom
    temperature: warm
    scope: workspace
    authority: advisory
    delivery_policy: on_demand
    retention: durable
    parent_id: semantic_memory_index
    producer: agent
    consumers: [memory_agent]
    recall_path: hybrid_recall
    lifecycle_policy: {initial_status: active, mutable: true, terminal_statuses: [archived]}
    isolation_policy: {dimension: workspace, cross_workspace: false}
`
	if err := os.WriteFile(workspaceFile, []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	states, err := store.ListBlocks(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	rootID := states[0].Block.BlockID
	leaf := memory.Block{BlockID: "dynamic-leaf", BlockType: "workspace_dynamic", Name: "Dynamic",
		Temperature: "warm", Scope: "workspace", Authority: "advisory", DeliveryPolicy: "on_demand",
		Retention: "durable", ParentID: rootID, Producer: "agent", Consumers: []string{"memory_agent"},
		RecallPath: "hybrid_recall", LifecyclePolicy: map[string]any{"initial_status": "active", "mutable": true,
			"terminal_statuses": []string{"archived"}}, IsolationPolicy: map[string]any{"dimension": "workspace", "cross_workspace": false}}
	if _, err := store.CreateBlock(context.Background(), leaf, catalog.CreateOptions{Actor: "test", Reason: "dynamic"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	blocks, err := Load(Options{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team", Strict: true, IncludeWorkspace: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 31 {
		t.Fatalf("expected packaged, override, root and dynamic leaf: %d", len(blocks))
	}
	if blocks[15].BlockID != "stable_project_facts" || blocks[15].Name != "Scoped facts" {
		for i, block := range blocks {
			if block.BlockID == "stable_project_facts" {
				t.Fatalf("override order or value differs at %d: %+v", i, block)
			}
		}
		t.Fatal("overridden block missing")
	}
	if blocks[len(blocks)-1].BlockID != "dynamic-leaf" {
		t.Fatalf("dynamic leaf missing from end of overlay: %+v", blocks[len(blocks)-1])
	}
	other, err := Load(Options{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "other", Strict: true, IncludeWorkspace: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range other {
		if block.BlockID == "dynamic-leaf" {
			t.Fatal("dynamic profile block crossed profile boundary")
		}
	}
}

func TestHierarchyRejectsMissingChildAndCycle(t *testing.T) {
	left := memory.Block{BlockID: "left", ParentID: "right"}
	right := memory.Block{BlockID: "right", ParentID: "left"}
	if err := ValidateTree([]memory.Block{left, right}); err == nil {
		t.Fatal("containment cycle was accepted")
	}
	left.ParentID = ""
	left.ChildIDs = []string{"missing"}
	if err := ValidateTree([]memory.Block{left, right}); err == nil {
		t.Fatal("dangling materialized child was accepted")
	}
}

func TestYAMLRootListAndTagTokenization(t *testing.T) {
	data := []byte(`
- block_id: sample
  tags: ["comma, kept", spaced]
  consumers: [memory_agent]
  producer: agent
  recall_path: hybrid_recall
  lifecycle_policy: {initial_status: active, mutable: true, terminal_statuses: [archived]}
  isolation_policy: {dimension: workspace, cross_workspace: false}
`)
	blocks, err := parseBlocks(data, true)
	if err != nil || len(blocks) != 1 || len(blocks[0].Tags) != 2 || blocks[0].Tags[0] != "comma, kept" {
		t.Fatalf("YAML root list or tag tuple differs from Python: %+v, %v", blocks, err)
	}
}
