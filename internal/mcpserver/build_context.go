package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type BuildContextArgs struct {
	RecallArgs
	RenderContext bool `json:"render_context,omitempty"`
}

func addBuildContextTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string, lineage map[string]string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "build_context",
		Description: "Build a bounded, non-directive context from overlay-eligible records.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args BuildContextArgs) (*mcp.CallToolResult, map[string]any, error) {
		payload, err := BuildContextPayload(ctx, selected, args.RecallArgs, workspaceID, backendID,
			boundHostScopeChain(workspaceID, lineage), lineage["run_id"], args.RenderContext)
		if err != nil {
			return nil, responseError(err, workspaceID, backendID), nil
		}
		return nil, payload, nil
	})
}
