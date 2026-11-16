package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ProcedureArgs struct {
	Query string `json:"query"`
	Limit *int   `json:"limit,omitempty"`
}

func ProcedurePayload(ctx context.Context, selected *engine.Engine, args ProcedureArgs,
	workspaceID, runID string) (map[string]any, error) {
	limit := 3
	if args.Limit != nil {
		if *args.Limit != 0 {
			limit = *args.Limit
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 128 {
		limit = 128
	}
	procedures, err := selected.Procedures(ctx, engine.ProcedureRequest{Query: args.Query,
		Limit: limit, RunID: runID})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "workspace_id": workspaceID,
		"query": args.Query, "count": len(procedures), "procedures": procedures}, nil
}

func addProcedureTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "procedure",
		Description: "Return stored workspace procedures as checklists with freshness."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ProcedureArgs) (*mcp.CallToolResult, map[string]any, error) {
			lineage := mcpHostLineage()
			payload, err := ProcedurePayload(ctx, selected, args, workspaceID, lineage["session_id"])
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
