package mcpserver

import (
	"context"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNavigationEnvelopeMCPShowsObservedMaintenance(t *testing.T) {
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	rootID := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
		}
	}
	leaf, err := selected.CreateBlock(ctx, engine.CreateBlockRequest{Name: "MCP maintenance",
		Synopsis: "Maintenance test", ParentID: rootID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	id := leaf["block_id"].(string)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(selected, "alpha", "mnemosyne").Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "go-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	observed, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "observe_maintenance",
		Arguments: map[string]any{"block_id": id, "observation_type": "low_usage",
			"values": map[string]any{"reads": 0}, "threshold_revision": "threshold-1",
			"evidence_ids": []string{"run-1"}}})
	if err != nil || observed.IsError || observed.StructuredContent.(map[string]any)["ok"] != true {
		t.Fatalf("MCP maintenance observation failed: %+v, %v", observed, err)
	}
	envelope, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "navigation_envelope",
		Arguments: map[string]any{"block_id": id}})
	if err != nil || envelope.IsError {
		t.Fatalf("MCP navigation envelope failed: %+v, %v", envelope, err)
	}
	payload := envelope.StructuredContent.(map[string]any)
	if payload["ok"] != true || payload["mutation_performed"] != false ||
		len(payload["maintenance_observations"].([]any)) != 1 ||
		payload["proposal_options"].([]any)[0] != "propose_merge" {
		t.Fatalf("MCP navigation envelope lost maintenance facts: %+v", payload)
	}
	converted, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "convert_block_to_index",
		Arguments: map[string]any{"block_id": id, "expected_revision": 1,
			"reason": "organize observed categories"}})
	if err != nil || converted.IsError || converted.StructuredContent.(map[string]any)["index_only"] != true ||
		converted.StructuredContent.(map[string]any)["backend_partition_id"] != "" {
		t.Fatalf("MCP leaf conversion failed: %+v, %v", converted, err)
	}
}
