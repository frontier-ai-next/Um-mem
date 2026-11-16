package mcpserver

import (
	"context"
	"errors"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func bindMCPPromotionLineage(request *engine.VerificationSubjectRequest,
	lineage map[string]string) error {
	bound := engine.VerificationLineage{UserID: lineage["user_id"],
		ProjectID: lineage["project_id"], SessionID: lineage["session_id"],
		TaskID: lineage["task_id"], SubtaskID: lineage["subtask_id"]}
	for _, pair := range [][2]string{{request.Lineage.UserID, bound.UserID},
		{request.Lineage.ProjectID, bound.ProjectID}, {request.Lineage.SessionID, bound.SessionID},
		{request.Lineage.TaskID, bound.TaskID}, {request.Lineage.SubtaskID, bound.SubtaskID}} {
		if pair[0] != "" && pair[0] != pair[1] {
			return errors.New("promotion scope claim is not bound to the MCP host")
		}
	}
	request.Lineage = bound
	return nil
}

func addPromotionTools(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string, lineage map[string]string) {
	mcp.AddTool(server, &mcp.Tool{Name: "verification_subject",
		Description: "Bind one current candidate revision, evidence and publication target for independent review."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			args engine.VerificationSubjectRequest) (*mcp.CallToolResult, map[string]any, error) {
			if err := bindMCPPromotionLineage(&args, lineage); err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			subject, err := selected.VerificationSubject(ctx, args)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, map[string]any{"ok": true, "subject": subject,
				"workspace_id": workspaceID, "backend": backendID}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "promote",
		Description: "Publish an independently verified candidate with a durable operation ID."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			args engine.PromoteRequest) (*mcp.CallToolResult, map[string]any, error) {
			if err := bindMCPPromotionLineage(&args.VerificationSubjectRequest, lineage); err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			result, err := selected.Promote(ctx, args)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			result["workspace_id"] = workspaceID
			return nil, result, nil
		})
}
