package mcpserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPendingTopologyEvolutionsMCPReadsPythonJournal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	path, err := catalog.Path(root, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	payload := `{"assignments":{},"operation":"split","sources":{},"targets":[]}`
	digest := sha256.Sum256([]byte(payload))
	if _, err := db.ExecContext(ctx, `INSERT INTO topology_evolutions(
		operation_id,workspace_id,profile_id,state,payload_json,payload_sha256,
		abort_reason,created_at,updated_at) VALUES(?,?,?,?,?,?,'',?,?)`,
		"split-1", "alpha", "team", "prepared", payload,
		hex.EncodeToString(digest[:]), "2026-01-01", "2026-01-01"); err != nil {
		t.Fatal(err)
	}
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
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "pending_topology_evolutions", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("MCP pending topology read failed: %+v, %v", result, err)
	}
	response := result.StructuredContent.(map[string]any)
	pending := response["pending"].([]any)
	if response["ok"] != true || len(pending) != 1 ||
		pending[0].(map[string]any)["operation_id"] != "split-1" ||
		pending[0].(map[string]any)["operation"] != "split" {
		t.Fatalf("MCP pending topology journal differs: %+v", response)
	}
	aborted, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "abort_topology_evolution", Arguments: map[string]any{
			"operation_id": "split-1", "reason": "native projection failed"}})
	if err != nil || aborted.IsError {
		t.Fatalf("MCP topology abort failed: %+v, %v", aborted, err)
	}
	abortPayload := aborted.StructuredContent.(map[string]any)
	if abortPayload["ok"] != true || abortPayload["state"] != "aborted" ||
		abortPayload["reason"] != "native projection failed" {
		t.Fatalf("MCP topology abort receipt differs: %+v", abortPayload)
	}
	after, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "pending_topology_evolutions", Arguments: map[string]any{}})
	if err != nil || after.IsError || len(after.StructuredContent.(map[string]any)["pending"].([]any)) != 0 {
		t.Fatalf("aborted topology remains pending over MCP: %+v, %v", after, err)
	}
	resumed, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "resume_topology_evolution", Arguments: map[string]any{"operation_id": "split-1"}})
	if err != nil || resumed.IsError || resumed.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("MCP resume did not reject an aborted mapping: %+v, %v", resumed, err)
	}
	split, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "split_block", Arguments: map[string]any{
			"block_id": "missing-source", "expected_revision": 1,
			"targets": []any{map[string]any{"name": "Target", "synopsis": "Target",
				"record_ids": []any{"missing-record"}}}, "reason": "reorganize"}})
	if err != nil || split.IsError || split.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("MCP split route did not reject a missing source: %+v, %v", split, err)
	}
	merge, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "merge_blocks", Arguments: map[string]any{
			"sources": []any{map[string]any{"block_id": "source-a", "expected_revision": 1},
				map[string]any{"block_id": "source-b", "expected_revision": 1}},
			"target": map[string]any{"name": "Target", "synopsis": "Target",
				"record_ids": []any{"missing-record"}},
			"parent_id": "missing-parent", "reason": "reorganize"}})
	if err != nil || merge.IsError || merge.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("MCP merge route did not reject a missing parent: %+v, %v", merge, err)
	}
}
