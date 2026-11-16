package mcpserver

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type AddBlockEdgeArgs struct {
	SrcID    string `json:"src_id"`
	DstID    string `json:"dst_id"`
	EdgeType string `json:"edge_type"`
	Reason   string `json:"reason"`
}

type RemoveBlockEdgeArgs struct {
	EdgeID           string `json:"edge_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Reason           string `json:"reason"`
}

func AddBlockEdgePayload(ctx context.Context, selected *engine.Engine, args AddBlockEdgeArgs,
	workspaceID, backendID string) (map[string]any, error) {
	edge, err := selected.AddBlockEdge(ctx, args.SrcID, args.DstID, args.EdgeType, args.Reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "edge_id": edge.EdgeID, "src_id": edge.SrcID,
		"dst_id": edge.DstID, "edge_type": edge.EdgeType, "reason": edge.Reason,
		"revision": edge.Revision, "created_at": edge.CreatedAt,
		"workspace_id": workspaceID, "profile_id": edge.ProfileID, "backend": backendID}, nil
}

func RemoveBlockEdgePayload(ctx context.Context, selected *engine.Engine, args RemoveBlockEdgeArgs,
	workspaceID, backendID string) (map[string]any, error) {
	edge, err := selected.RemoveBlockEdge(ctx, args.EdgeID, args.ExpectedRevision, args.Reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "removed": edge, "workspace_id": workspaceID,
		"backend": backendID}, nil
}

func addBlockEdgeTools(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "add_block_edge", Description: "Add a typed relation between two logical blocks."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args AddBlockEdgeArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := AddBlockEdgePayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "remove_block_edge", Description: "Remove a logical block relation using its expected revision."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args RemoveBlockEdgeArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := RemoveBlockEdgePayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
