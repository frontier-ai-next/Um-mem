package mcpserver

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type PendingTopologyEvolutionsArgs struct{}

type AbortTopologyEvolutionArgs struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
}

type ResumeTopologyEvolutionArgs struct {
	OperationID string `json:"operation_id"`
}

type SplitBlockArgs = engine.SplitBlockRequest
type MergeBlocksArgs = engine.MergeBlocksRequest

func PendingTopologyEvolutionsPayload(ctx context.Context, selected *engine.Engine,
	workspaceID string) (map[string]any, error) {
	pending, err := selected.PendingTopologyEvolutions(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "workspace_id": workspaceID, "pending": pending}, nil
}

func addPendingTopologyEvolutionsTool(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "pending_topology_evolutions",
		Description: "List unfinished split and merge generation IDs without record text or mappings."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			_ PendingTopologyEvolutionsArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := PendingTopologyEvolutionsPayload(ctx, selected, workspaceID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}

func AbortTopologyEvolutionPayload(ctx context.Context, selected *engine.Engine,
	args AbortTopologyEvolutionArgs) (map[string]any, error) {
	return selected.AbortTopologyEvolution(ctx, args.OperationID, args.Reason)
}

func addAbortTopologyEvolutionTool(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "abort_topology_evolution",
		Description: "Abandon an unpublished split or merge while retaining its audit journal."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			args AbortTopologyEvolutionArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := AbortTopologyEvolutionPayload(ctx, selected, args)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}

func ResumeTopologyEvolutionPayload(ctx context.Context, selected *engine.Engine,
	args ResumeTopologyEvolutionArgs) (map[string]any, error) {
	return selected.ResumeTopologyEvolution(ctx, args.OperationID)
}

func addResumeTopologyEvolutionTool(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "resume_topology_evolution",
		Description: "Resume a previously prepared exact split or merge mapping."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			args ResumeTopologyEvolutionArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := ResumeTopologyEvolutionPayload(ctx, selected, args)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}

func SplitBlockPayload(ctx context.Context, selected *engine.Engine,
	args SplitBlockArgs) (map[string]any, error) {
	return selected.SplitBlock(ctx, args)
}

func addSplitBlockTool(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "split_block",
		Description: "Split an active leaf into new blocks with an exact assignment of every current record."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			args SplitBlockArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := SplitBlockPayload(ctx, selected, args)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}

func MergeBlocksPayload(ctx context.Context, selected *engine.Engine,
	args MergeBlocksArgs) (map[string]any, error) {
	return selected.MergeBlocks(ctx, args)
}

func addMergeBlocksTool(server *mcp.Server, selected *engine.Engine,
	workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "merge_blocks",
		Description: "Merge active leaves into one new block with an exact assignment of every current record."},
		func(ctx context.Context, _ *mcp.CallToolRequest,
			args MergeBlocksArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := MergeBlocksPayload(ctx, selected, args)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
