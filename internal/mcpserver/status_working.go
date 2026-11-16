package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type OperationStatusArgs struct {
	OperationID string `json:"operation_id"`
}

type ReadWorkingSetArgs struct {
	OwnerID      string   `json:"owner_id"`
	GenerationID string   `json:"generation_id"`
	RecordIDs    []string `json:"record_ids"`
	MaxTokens    *int     `json:"max_tokens,omitempty"`
	MaxBytes     *int     `json:"max_bytes,omitempty"`
}

func addOperationStatusTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "operation_status",
		Description: "Read the original operation receipt and current native projection state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args OperationStatusArgs) (*mcp.CallToolResult, map[string]any, error) {
		result, err := selected.OperationStatusFor(ctx, args.OperationID, "memory_client", 2)
		if err != nil {
			return nil, responseError(err, workspaceID, backendID), nil
		}
		result["workspace_id"] = workspaceID
		result["backend"] = backendID
		return nil, result, nil
	})
}

func addWorkingSetTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_working_set",
		Description: "Read exact owner-bound candidates as unverified working context.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args ReadWorkingSetArgs) (*mcp.CallToolResult, map[string]any, error) {
		maxTokens, maxBytes := 2048, 32768
		if args.MaxTokens != nil {
			maxTokens = *args.MaxTokens
		}
		if args.MaxBytes != nil {
			maxBytes = *args.MaxBytes
		}
		working, err := selected.ReadWorkingSet(ctx, args.OwnerID, args.GenerationID,
			args.RecordIDs, maxTokens, maxBytes, []string{"workspace:" + workspaceID})
		if err != nil {
			return nil, responseError(err, workspaceID, backendID), nil
		}
		return nil, map[string]any{"ok": true, "records": working.Records,
			"rendered_context": working.RenderedContext, "token_budget": working.TokenBudget,
			"estimated_tokens": working.EstimatedTokens, "byte_budget": working.ByteBudget,
			"rendered_bytes": working.RenderedBytes, "truncated": working.Truncated,
			"omitted_record_ids":     working.OmittedRecordIDs,
			"unavailable_record_ids": working.UnavailableRecordIDs,
			"token_accounting":       working.TokenAccounting, "visibility": working.Visibility,
			"workspace_id": workspaceID, "backend": backendID}, nil
	})
}
