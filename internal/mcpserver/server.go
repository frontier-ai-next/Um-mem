package mcpserver

import (
	"context"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ReadExactRecordArgs struct {
	RecordID    string `json:"record_id,omitempty"`
	EvidenceRef string `json:"evidence_ref,omitempty"`
}

type ReadExactRecordsArgs struct {
	RecordIDs []string `json:"record_ids"`
}

type ProjectionBarrierArgs struct {
	RecordID       string   `json:"record_id"`
	CommitRevision int64    `json:"commit_revision,omitempty"`
	TimeoutSeconds *float64 `json:"timeout_seconds,omitempty"`
}

func responseError(err error, workspaceID, backendID string) map[string]any {
	return map[string]any{"ok": false, "error": err.Error(), "workspace_id": workspaceID, "backend": backendID}
}

// New exposes only public MCP tools whose Go behavior is implemented. The
// engine scope is fixed at process startup; callers cannot select another
// workspace, profile or backend through tool arguments.
func New(selected *engine.Engine, workspaceID, backendID string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "um_mem", Version: "go-port"}, nil)
	addHealthTool(server, selected, workspaceID, backendID)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_exact_record",
		Description: "Read one canonical record by ID or an unambiguous exact evidence reference.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args ReadExactRecordArgs) (*mcp.CallToolResult, map[string]any, error) {
		id := strings.TrimSpace(args.RecordID)
		ref := strings.TrimSpace(args.EvidenceRef)
		if (id == "") == (ref == "") {
			return nil, responseError(errors.New("read_exact_record requires exactly one record_id or evidence_ref"), workspaceID, backendID), nil
		}
		var record *memory.MemoryRecord
		var err error
		if id != "" {
			record, err = selected.ReadStored(ctx, id)
		} else {
			record, err = selected.ReadEvidence(ctx, ref)
		}
		if err != nil {
			return nil, responseError(err, workspaceID, backendID), nil
		}
		if record == nil {
			return nil, responseError(errors.New("exact record was not found"), workspaceID, backendID), nil
		}
		return nil, map[string]any{"ok": true, "record": record, "workspace_id": workspaceID, "backend": backendID}, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_exact_records",
		Description: "Read at most 128 distinct canonical record IDs in caller order.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args ReadExactRecordsArgs) (*mcp.CallToolResult, map[string]any, error) {
		if len(args.RecordIDs) < 1 || len(args.RecordIDs) > 128 {
			return nil, responseError(errors.New("read_exact_records requires 1-128 distinct record IDs"), workspaceID, backendID), nil
		}
		seen := make(map[string]bool, len(args.RecordIDs))
		result := make([]memory.MemoryRecord, 0, len(args.RecordIDs))
		for _, id := range args.RecordIDs {
			if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) || seen[id] {
				return nil, responseError(errors.New("read_exact_records requires 1-128 distinct record IDs"), workspaceID, backendID), nil
			}
			seen[id] = true
			record, err := selected.ReadStored(ctx, id)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			if record == nil {
				return nil, responseError(errors.New("exact record was not found"), workspaceID, backendID), nil
			}
			result = append(result, *record)
		}
		return nil, map[string]any{"ok": true, "records": result, "workspace_id": workspaceID, "backend": backendID}, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "projection_barrier",
		Description: "Observe one canonical record's native projection through a requested deadline.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args ProjectionBarrierArgs) (*mcp.CallToolResult, map[string]any, error) {
		timeout := 30.0
		if args.TimeoutSeconds != nil {
			timeout = *args.TimeoutSeconds
		}
		result, err := selected.ProjectionBarrier(ctx, args.RecordID, args.CommitRevision, timeout)
		if err != nil {
			return nil, responseError(err, workspaceID, backendID), nil
		}
		result["workspace_id"] = workspaceID
		result["backend"] = backendID
		return nil, result, nil
	})
	lineage := mcpHostLineage()
	addRecallTool(server, selected, workspaceID, backendID, lineage)
	addRecentTool(server, selected, workspaceID, backendID)
	addProcedureTool(server, selected, workspaceID, backendID)
	addBlockMapTool(server, selected, workspaceID, backendID)
	addMemoryMapTool(server, selected, workspaceID, backendID)
	addExportObsidianTool(server, selected, workspaceID, backendID)
	addExpandBlockTool(server, selected, workspaceID, backendID)
	addNavigationEnvelopeTools(server, selected, workspaceID, backendID)
	addPendingTopologyEvolutionsTool(server, selected, workspaceID, backendID)
	addAbortTopologyEvolutionTool(server, selected, workspaceID, backendID)
	addResumeTopologyEvolutionTool(server, selected, workspaceID, backendID)
	addSplitBlockTool(server, selected, workspaceID, backendID)
	addMergeBlocksTool(server, selected, workspaceID, backendID)
	addBlockEdgeTools(server, selected, workspaceID, backendID)
	addRecordEdgeTool(server, selected, workspaceID, backendID)
	addExportGraphTool(server, selected, workspaceID, backendID)
	addRebuildIndexesTool(server, selected, workspaceID, backendID)
	addCreateBlockTool(server, selected, workspaceID, backendID)
	addAmendBlockTool(server, selected, workspaceID, backendID)
	addConvertBlockToIndexTool(server, selected, workspaceID, backendID)
	addArchiveBlockTool(server, selected, workspaceID, backendID)
	addReviveBlockTool(server, selected, workspaceID, backendID)
	addMoveBlockTool(server, selected, workspaceID, backendID)
	addDeleteBlockTool(server, selected, workspaceID, backendID)
	addForgetTool(server, selected, workspaceID, backendID)
	addAmendTool(server, selected, workspaceID, backendID, lineage)
	addPromotionTools(server, selected, workspaceID, backendID, lineage)
	addConsolidationTool(server, selected, workspaceID, backendID, lineage)
	addWorkingCheckpointTool(server, selected, workspaceID, backendID, lineage)
	addListRecordsTool(server, selected, workspaceID, backendID)
	addBuildContextTool(server, selected, workspaceID, backendID, lineage)
	addOperationStatusTool(server, selected, workspaceID, backendID)
	addWorkingSetTool(server, selected, workspaceID, backendID)
	addRememberToolForHost(server, selected, workspaceID, backendID, lineage)
	addActiveMaskTools(server, selected, workspaceID, backendID, lineage)
	return server
}
