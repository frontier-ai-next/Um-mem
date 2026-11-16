package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func MoveBlockPayload(ctx context.Context, selected *engine.Engine, args engine.MoveBlockRequest,
	workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.MoveBlock(ctx, args)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"] = workspaceID
	payload["backend"] = backendID
	return payload, nil
}

func addMoveBlockTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "move_block", Description: "Move a logical block to an active index-only parent using its expected revision."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args engine.MoveBlockRequest) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := MoveBlockPayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
