package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type AddEdgeToolArgs struct {
	SrcID        string           `json:"src_id"`
	DstID        string           `json:"dst_id"`
	EdgeType     string           `json:"edge_type"`
	Reason       string           `json:"reason,omitempty"`
	Weight       *float64         `json:"weight,omitempty"`
	EvidenceRefs []map[string]any `json:"evidence_refs"`
	Metadata     map[string]any   `json:"metadata,omitempty"`
}

func AddEdgePayload(ctx context.Context, selected *engine.Engine, args engine.AddEdgeRequest,
	workspaceID, backendID string) (map[string]any, error) {
	payload, err := selected.AddEdge(ctx, args)
	if err != nil {
		return nil, err
	}
	payload["workspace_id"], payload["backend"] = workspaceID, backendID
	return payload, nil
}

func addRecordEdgeTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "add_edge", Description: "Link two retained canonical records with a typed, evidenced graph edge."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args AddEdgeToolArgs) (*mcp.CallToolResult, map[string]any, error) {
			refs := make([]memory.EvidenceRef, 0, len(args.EvidenceRefs))
			for _, raw := range args.EvidenceRefs {
				encoded, err := json.Marshal(raw)
				if err != nil {
					return nil, responseError(err, workspaceID, backendID), nil
				}
				var ref memory.EvidenceRef
				decoder := json.NewDecoder(bytes.NewReader(encoded))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&ref); err != nil {
					return nil, responseError(err, workspaceID, backendID), nil
				}
				refs = append(refs, ref)
			}
			weight := json.Number("")
			if args.Weight != nil {
				repr := strconv.FormatFloat(*args.Weight, 'f', -1, 64)
				if !strings.Contains(repr, ".") {
					repr += ".0"
				}
				weight = json.Number(repr)
			}
			request := engine.AddEdgeRequest{SrcID: args.SrcID, DstID: args.DstID,
				EdgeType: args.EdgeType, Reason: args.Reason, Weight: weight,
				EvidenceRefs: refs, Metadata: args.Metadata}
			payload, err := AddEdgePayload(ctx, selected, request, workspaceID, backendID)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
