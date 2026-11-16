package mcpserver

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func AmendPayload(ctx context.Context, selected *engine.Engine, args engine.AmendRequest,
	workspaceID, backendID, callerRole string, protocolVersion int,
	scopeLineage map[string]string) (map[string]any, error) {
	payload, err := selected.AmendFor(ctx, args, callerRole, protocolVersion, scopeLineage)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"] = workspaceID
	payload["backend"] = backendID
	return payload, nil
}

func addAmendTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string,
	lineage map[string]string) {
	mcp.AddTool(server, &mcp.Tool{Name: "amend", Description: "Append a compare-and-swap correction to an advisory memory."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args engine.AmendRequest) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := AmendPayload(ctx, selected, args, workspaceID, backendID,
				"memory_client", 2, lineage)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
