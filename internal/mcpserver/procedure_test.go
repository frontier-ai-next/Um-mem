package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProcedureToolReturnsSafeChecklist(t *testing.T) {
	t.Setenv("UM_MEM_SEMANTIC_MODEL", "")
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := RememberPayload(ctx, selected, RememberArgs{
		Content: "Restart the local service after changing its configuration.",
		Title:   "Restart local service", Kind: "procedure",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1",
		Metadata: map[string]any{"steps": []any{"stop service", "start service"},
			"verify": "health is ready", "verified_at": time.Now().UTC().Format(time.RFC3339)},
	}, "alpha", "mnemosyne", "parent", 1)
	if err != nil || created["saved"] != true {
		t.Fatalf("procedure candidate was not saved: %+v, %v", created, err)
	}
	stored, err := selected.ReadStored(ctx, created["memory_id"].(string))
	if err != nil || stored == nil {
		t.Fatalf("procedure record was not committed: %v", err)
	}
	fingerprint, ok := stored.Metadata["fingerprint"].(map[string]any)
	if !ok || fingerprint["workspace"] != "alpha" ||
		fingerprint["semantic_model"] != "intfloat/multilingual-e5-large" {
		t.Fatalf("procedure write lost its environment stamp: %+v", stored.Metadata)
	}
	server := New(selected, "alpha", "mnemosyne")
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
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
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "procedure",
		Arguments: map[string]any{"query": "restart local service"}})
	if err != nil || result.IsError {
		t.Fatalf("procedure tool call failed: %+v, %v", result, err)
	}
	payload := result.StructuredContent.(map[string]any)
	if payload["ok"] != true || payload["count"] != float64(1) {
		t.Fatalf("procedure tool missed a stored checklist: %+v", payload)
	}
	entry := payload["procedures"].([]any)[0].(map[string]any)
	if entry["record_id"] != created["memory_id"] || entry["freshness"] != "fresh" ||
		entry["verify"] != "health is ready" || len(entry["steps"].([]any)) != 2 {
		t.Fatalf("procedure response lost checklist or freshness: %+v", entry)
	}
	t.Setenv("UM_MEM_SEMANTIC_MODEL", "changed-model")
	changed, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "procedure",
		Arguments: map[string]any{"query": "restart local service"}})
	if err != nil || changed.IsError {
		t.Fatalf("procedure after environment change failed: %+v, %v", changed, err)
	}
	stale := changed.StructuredContent.(map[string]any)["procedures"].([]any)[0].(map[string]any)
	if stale["freshness"] != "stale" || !strings.Contains(stale["reason"].(string), "semantic_model") {
		t.Fatalf("changed environment did not stale a confirmed procedure: %+v", stale)
	}
}
