package mcpserver

import (
	"context"
	"errors"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addWorkingCheckpointTool(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string, lineage map[string]string) {
	mcp.AddTool(server, &mcp.Tool{Name: "commit_working_checkpoint",
		Description: "Commit a task-local working-state checkpoint from exact host sources or an independent verifier report."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			args engine.CommitWorkingCheckpointRequest) (*mcp.CallToolResult, map[string]any, error) {
			checkpoint := args.Checkpoint
			if checkpoint.SessionID != lineage["session_id"] || checkpoint.TaskID == "" ||
				checkpoint.TaskID != lineage["task_id"] ||
				checkpoint.ProjectID != lineage["project_id"] ||
				checkpoint.ParentSessionID != lineage["parent_session_id"] {
				return nil, responseError(errors.New("working checkpoint lineage is not bound to the MCP host"),
					workspaceID, backendID), nil
			}
			result, err := selected.CommitWorkingCheckpoint(ctx, args)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			result["workspace_id"], result["backend"] = workspaceID, backendID
			return nil, result, nil
		})
}
