package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type BlockMapArgs struct {
	Depth       *int `json:"depth,omitempty"`
	IncludeMeta bool `json:"include_meta,omitempty"`
	ChildLimit  *int `json:"child_limit,omitempty"`
}

type ExpandBlockArgs struct {
	BlockID string `json:"block_id"`
	Limit   *int   `json:"limit,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
}

func BlockMapPayload(ctx context.Context, selected *engine.Engine, args BlockMapArgs,
	workspaceID, backendID string) (map[string]any, error) {
	depth, limit := 1, 50
	if args.Depth != nil {
		depth = *args.Depth
	}
	if args.ChildLimit != nil {
		limit = *args.ChildLimit
	}
	roots, err := selected.BlockMap(ctx, depth, args.IncludeMeta, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "workspace_id": workspaceID, "backend": backendID, "roots": roots}, nil
}

func addBlockMapTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "block_map", Description: "Navigate the bounded logical memory catalog."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args BlockMapArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := BlockMapPayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}

func ExpandBlockPayload(ctx context.Context, selected *engine.Engine, args ExpandBlockArgs,
	workspaceID, backendID string) (map[string]any, error) {
	limit := 50
	if args.Limit != nil {
		limit = *args.Limit
	}
	page, err := selected.ExpandBlock(ctx, args.BlockID, limit, args.Cursor)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"ok": true, "workspace_id": workspaceID, "backend": backendID}
	for key, value := range page {
		payload[key] = value
	}
	return payload, nil
}

func addExpandBlockTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "expand_block", Description: "Read one paged logical memory branch and its related block edges."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ExpandBlockArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := ExpandBlockPayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
