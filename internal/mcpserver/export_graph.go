package mcpserver

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ExportGraphArgs struct{}

func ExportGraphPayload(ctx context.Context, selected *engine.Engine,
	workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.ExportGraph(ctx)
	if err != nil {
		return nil, err
	}
	payload["ok"], payload["workspace_id"], payload["backend"] = true, workspaceID, backendID
	return payload, nil
}

func addExportGraphTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "export_graph",
		Description: "Export scoped records, links and machine-readable graph integrity."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ ExportGraphArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := ExportGraphPayload(ctx, selected, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
