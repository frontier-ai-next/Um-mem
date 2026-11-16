package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHealthMCPReportsBackendProbeAndTransportScope(t *testing.T) {
	ctx := context.Background()
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" {
			t.Errorf("unexpected native health path: %s", request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer native.Close()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "hindsight", HindsightBaseURL: native.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(selected, "alpha", "hindsight").Connect(ctx, serverTransport, nil)
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
		Name: "health", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("MCP health failed: %+v, %v", result, err)
	}
	payload := result.StructuredContent.(map[string]any)
	if payload["ok"] != true || payload["native_ok"] != true ||
		payload["effective_mode"] != "selected_native" ||
		payload["integration_mode"] != "mcp_tools_only" ||
		payload["deep_agent_support"] != false ||
		payload["runtime"].(map[string]any)["health"] != true {
		t.Fatalf("MCP health scope differs: %+v", payload)
	}
}
