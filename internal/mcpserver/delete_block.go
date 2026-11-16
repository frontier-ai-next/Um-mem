package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func DeleteBlockPayload(ctx context.Context, selected *engine.Engine, args engine.DeleteBlockRequest,
	workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.DeleteBlock(ctx, args)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"] = workspaceID
	payload["backend"] = backendID
	return payload, nil
}

func addDeleteBlockTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "delete_block", Description: "Delete an empty archived block after verifying catalog references and current records."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args engine.DeleteBlockRequest) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := DeleteBlockPayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
