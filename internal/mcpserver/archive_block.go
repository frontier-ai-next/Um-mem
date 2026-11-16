package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func ArchiveBlockPayload(ctx context.Context, selected *engine.Engine, args engine.BlockTransitionRequest,
	workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.ArchiveBlock(ctx, args)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"] = workspaceID
	payload["backend"] = backendID
	return payload, nil
}

func addArchiveBlockTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "archive_block", Description: "Archive a block after checking children, incoming edges and active masks."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args engine.BlockTransitionRequest) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := ArchiveBlockPayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}

func ReviveBlockPayload(ctx context.Context, selected *engine.Engine, args engine.BlockTransitionRequest,
	workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.ReviveBlock(ctx, args)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"] = workspaceID
	payload["backend"] = backendID
	return payload, nil
}

func addReviveBlockTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "revive_block", Description: "Revive an archived block beneath an active index-only parent."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args engine.BlockTransitionRequest) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := ReviveBlockPayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
