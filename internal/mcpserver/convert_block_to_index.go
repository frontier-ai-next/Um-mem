package mcpserver

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func ConvertBlockToIndexPayload(ctx context.Context, selected *engine.Engine,
	args engine.ConvertBlockToIndexRequest, workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.ConvertBlockToIndex(ctx, args)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"], payload["backend"] = workspaceID, backendID
	return payload, nil
}

func addConvertBlockToIndexTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "convert_block_to_index",
		Description: "Convert a never-used content leaf into an index-only parent."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args engine.ConvertBlockToIndexRequest) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := ConvertBlockToIndexPayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
