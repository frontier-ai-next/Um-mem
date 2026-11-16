package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type MemoryMapArgs struct {
	OutputPath string `json:"output_path,omitempty"`
}

func addMemoryMapTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "memory_map",
		Description: "Refresh a self-contained HTML map of exact memory blocks, records, and available semantic links."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			args MemoryMapArgs) (*mcp.CallToolResult, map[string]any, error) {
			result, err := selected.WriteMemoryMap(ctx, args.OutputPath)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			result["workspace_id"], result["backend"] = workspaceID, backendID
			return nil, result, nil
		})
}
