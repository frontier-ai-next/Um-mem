package mcpserver

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type HealthArgs struct{}

func HealthPayload(ctx context.Context, selected *engine.Engine) (map[string]any, error) {
	payload, err := selected.Health(ctx)
	if err != nil {
		return nil, err
	}
	healthy := payload["ok"] == true
	if healthy {
		payload["effective_mode"] = "selected_native"
	}
	payload["integration_mode"] = "mcp_tools_only"
	payload["deep_agent_support"] = false
	payload["degraded_reasons"] = []string{"mcp_transport_has_no_host_lifecycle_capture"}
	payload["runtime"] = map[string]any{"requested_profile": "mcp_tools_only",
		"effective_profile": "mcp_tools_only", "integration_mode": "mcp_tools_only",
		"deep_agent_support": false, "health": healthy}
	return payload, nil
}

func addHealthTool(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "health",
		Description: "Report selected native backend and durable projection journal health."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			_ HealthArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := HealthPayload(ctx, selected)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
