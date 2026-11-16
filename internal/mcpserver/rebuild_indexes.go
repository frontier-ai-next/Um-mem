package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type RebuildIndexesArgs struct{}

func addRebuildIndexesTool(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "rebuild_indexes",
		Description: "Rebuild selected-backend materialized indexes from canonical records."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ RebuildIndexesArgs) (*mcp.CallToolResult, map[string]any, error) {
			result, err := selected.RebuildIndexes(ctx)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, result, nil
		})
}
