package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func ForgetPayload(ctx context.Context, selected *engine.Engine, args engine.ForgetRequest,
	workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.Forget(ctx, args)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"] = workspaceID
	payload["backend"] = backendID
	return payload, nil
}

func addForgetTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "forget", Description: "Retract a canonical memory by writing a verified immutable successor."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args engine.ForgetRequest) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := ForgetPayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
