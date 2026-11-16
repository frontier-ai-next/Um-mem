package mcpserver

import (
	"context"
	"errors"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func bindMCPConsolidationLineage(request *engine.ConsolidateObservationsRequest,
	lineage map[string]string) error {
	for _, pair := range [][2]string{{request.SessionID, lineage["session_id"]},
		{request.TaskID, lineage["task_id"]}, {request.SubtaskID, lineage["subtask_id"]},
		{request.ProjectID, lineage["project_id"]}} {
		if pair[0] != "" && pair[0] != pair[1] {
			return errors.New("consolidation lineage claim is not bound to the MCP host")
		}
	}
	request.SessionID, request.TaskID = lineage["session_id"], lineage["task_id"]
	request.SubtaskID, request.ProjectID = lineage["subtask_id"], lineage["project_id"]
	return nil
}

func addConsolidationTool(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string, lineage map[string]string) {
	mcp.AddTool(server, &mcp.Tool{Name: "consolidate_observations",
		Description: "Consolidate current durable host observations after independent ledger and tribunal verification."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			args engine.ConsolidateObservationsRequest) (*mcp.CallToolResult, map[string]any, error) {
			if err := bindMCPConsolidationLineage(&args, lineage); err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			result, err := selected.ConsolidateObservations(ctx, args)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			result["workspace_id"], result["backend"] = workspaceID, backendID
			return nil, result, nil
		})
}
