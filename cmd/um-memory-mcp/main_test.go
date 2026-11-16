package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExecutableServesMCPOverStdio(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "um-memory-mcp")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Go MCP executable: %v\n%s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	process := exec.Command(binary, "--repo-root", t.TempDir(), "--workspace", "alpha", "--profile", "team", "--backend", "hindsight")
	process.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: process}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if len(names) != 46 || !names["export_obsidian"] || !names["memory_map"] || !names["commit_working_checkpoint"] || !names["consolidate_observations"] || !names["rebuild_indexes"] || !names["health"] || !names["merge_blocks"] || !names["split_block"] || !names["resume_topology_evolution"] || !names["abort_topology_evolution"] || !names["pending_topology_evolutions"] || !names["verification_subject"] || !names["promote"] || !names["procedure"] || !names["convert_block_to_index"] || !names["export_graph"] || !names["block_map"] || !names["expand_block"] || !names["navigation_envelope"] || !names["observe_maintenance"] || !names["add_block_edge"] || !names["remove_block_edge"] || !names["add_edge"] || !names["create_block"] || !names["amend_block"] || !names["archive_block"] || !names["revive_block"] || !names["move_block"] || !names["delete_block"] || !names["forget"] || !names["amend"] || !names["remember"] || !names["get_active_mask"] || !names["set_active_mask"] || !names["amend_active_mask"] || !names["resolve_active_mask"] || !names["transition_active_mask"] || !names["read_exact_record"] || !names["read_exact_records"] || !names["projection_barrier"] || !names["recall"] || !names["recent"] || !names["list_records"] || !names["build_context"] || !names["operation_status"] || !names["read_working_set"] {
		t.Fatalf("Go stdio MCP tool listing: %+v", tools)
	}
	rebuilt, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "rebuild_indexes", Arguments: map[string]any{}})
	if err != nil || rebuilt.IsError || rebuilt.StructuredContent.(map[string]any)["ok"] != true ||
		rebuilt.StructuredContent.(map[string]any)["record_count"] != float64(0) {
		t.Fatalf("Go stdio MCP index rebuild: %+v, %v", rebuilt, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_exact_records", Arguments: map[string]any{"record_ids": []string{"absent"}}})
	if err != nil || result.IsError || result.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("Go stdio MCP request/response: %+v, %v", result, err)
	}
}

func TestExecutableRecallsPythonPalaceOverStdio(t *testing.T) {
	root := os.Getenv("UM_MEM_TEST_PYTHON_PALACE_ROOT")
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if root == "" || modelDir == "" {
		t.Skip("Python MemPalace and MiniLM fixtures are unavailable")
	}
	binary := filepath.Join(t.TempDir(), "um-memory-mcp")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Go MCP executable: %v\n%s", err, output)
	}
	process := exec.CommandContext(ctx, binary, "--repo-root", root, "--workspace", "alpha",
		"--profile", "default", "--backend", "mempalace", "--minilm-model-dir", modelDir)
	client := mcp.NewClient(&mcp.Implementation{Name: "palace-stdio-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: process}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "recall", Arguments: map[string]any{
		"query": "blue comet protocol", "search_all": true, "max_tokens": 1024,
	}})
	if err != nil || result.IsError {
		t.Fatalf("Go stdio MCP MemPalace recall failed: %+v, %v", result, err)
	}
	payload := result.StructuredContent.(map[string]any)
	hits, ok := payload["hits"].([]any)
	if payload["ok"] != true || !ok || len(hits) == 0 {
		t.Fatalf("Go stdio MCP lost Python palace hits: %+v", payload)
	}
	found := false
	for _, hit := range hits {
		metadata, _ := hit.(map[string]any)["metadata"].(map[string]any)
		found = found || metadata["block_id"] == "mem_c3a6452e2bd27eca"
	}
	if !found {
		t.Fatalf("Go stdio MCP missed Python palace record: %+v", hits)
	}
}
