package mcpserver

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ListRecordsArgs struct {
	Scope            string `json:"scope"`
	ScopeID          string `json:"scope_id"`
	LogicalBlockID   string `json:"logical_block_id,omitempty"`
	Limit            *int   `json:"limit,omitempty"`
	Cursor           string `json:"cursor,omitempty"`
	IncludeRetracted bool   `json:"include_retracted,omitempty"`
}

func addListRecordsTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_records",
		Description: "Enumerate bounded canonical record IDs in one exact scope with a frozen cursor.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args ListRecordsArgs) (*mcp.CallToolResult, map[string]any, error) {
		limit := 32
		if args.Limit != nil {
			limit = *args.Limit
		}
		page, err := selected.ListRecords(ctx, engine.ListRecordsRequest{Scope: args.Scope,
			ScopeID: args.ScopeID, LogicalBlockID: args.LogicalBlockID, Limit: limit,
			Cursor: args.Cursor, IncludeRetracted: args.IncludeRetracted})
		if err != nil {
			return nil, responseError(err, workspaceID, backendID), nil
		}
		return nil, map[string]any{"ok": true, "records": page.Records,
			"next_cursor": page.NextCursor, "snapshot_count": page.SnapshotCount,
			"scope": page.Scope, "scope_id": page.ScopeID,
			"logical_block_id": page.LogicalBlockID,
			"workspace_id":     workspaceID, "backend": backendID}, nil
	})
}
