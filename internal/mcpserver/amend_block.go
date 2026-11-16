package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func AmendBlockPayload(ctx context.Context, selected *engine.Engine, args engine.AmendBlockRequest,
	workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.AmendBlock(ctx, args)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"] = workspaceID
	payload["backend"] = backendID
	return payload, nil
}

func addAmendBlockTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "amend_block", Description: "CAS-amend one workspace-owned logical block."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args engine.AmendBlockRequest) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := AmendBlockPayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
