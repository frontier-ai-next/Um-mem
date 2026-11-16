package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type RecentArgs struct {
	Limit             *int   `json:"limit,omitempty"`
	Since             string `json:"since,omitempty"`
	IncludeCandidates *bool  `json:"include_candidates,omitempty"`
	LogicalBlockID    string `json:"logical_block_id,omitempty"`
}

func addRecentTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "recent",
		Description: "Return the newest current canonical records for session continuity.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args RecentArgs) (*mcp.CallToolResult, map[string]any, error) {
		limit := 8
		if args.Limit != nil {
			limit = *args.Limit
		}
		includeCandidates := true
		if args.IncludeCandidates != nil {
			includeCandidates = *args.IncludeCandidates
		}
		records, err := selected.Recent(ctx, engine.RecentRequest{Limit: limit, Since: args.Since,
			Scope: "workspace", ScopeID: workspaceID, LogicalBlockID: args.LogicalBlockID,
			IncludeCandidates: includeCandidates})
		if err != nil {
			return nil, responseError(err, workspaceID, backendID), nil
		}
		return nil, map[string]any{"ok": true, "records": records, "count": len(records),
			"scope": "workspace", "scope_id": workspaceID, "logical_block_id": args.LogicalBlockID,
			"since": args.Since, "workspace_id": workspaceID, "backend": backendID}, nil
	})
}
