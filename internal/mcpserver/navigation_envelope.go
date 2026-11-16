package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type NavigationEnvelopeArgs struct {
	BlockID       string `json:"block_id"`
	Limit         *int   `json:"limit,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
	RelatedCursor string `json:"related_cursor,omitempty"`
}

func NavigationEnvelopePayload(ctx context.Context, selected *engine.Engine,
	args NavigationEnvelopeArgs, workspaceID, backendID string) (map[string]any, error) {
	limit := 50
	if args.Limit != nil {
		limit = *args.Limit
	}
	payload, err := selected.NavigationEnvelope(ctx, args.BlockID, limit, args.Cursor, args.RelatedCursor)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"], payload["backend"] = workspaceID, backendID
	return payload, nil
}

func ObserveMaintenancePayload(ctx context.Context, selected *engine.Engine,
	args catalog.AddObservationRequest, workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.ObserveMaintenance(ctx, args)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"], payload["backend"] = workspaceID, backendID
	return payload, nil
}

func addNavigationEnvelopeTools(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "navigation_envelope", Description: "Inspect a catalog branch, related blocks, and advisory maintenance facts."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args NavigationEnvelopeArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := NavigationEnvelopePayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "observe_maintenance", Description: "Record numeric catalog maintenance evidence for a dynamic block."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args catalog.AddObservationRequest) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := ObserveMaintenancePayload(ctx, selected, args, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
