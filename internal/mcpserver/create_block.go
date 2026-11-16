package mcpserver

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func CreateBlockPayload(ctx context.Context, selected *engine.Engine, args engine.CreateBlockRequest,
	workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.CreateBlock(ctx, args)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"] = workspaceID
	payload["backend"] = backendID
	return payload, nil
}

func addCreateBlockTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "create_block", Description: "Create an index or writable leaf under an active workspace-owned index block."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args engine.CreateBlockRequest) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := CreateBlockPayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
