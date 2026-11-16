package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

type AddEdgeRequest struct {
	SrcID        string               `json:"src_id"`
	DstID        string               `json:"dst_id"`
	EdgeType     string               `json:"edge_type"`
	Reason       string               `json:"reason,omitempty"`
	Weight       json.Number          `json:"weight,omitempty"`
	EvidenceRefs []memory.EvidenceRef `json:"evidence_refs"`
	Metadata     map[string]any       `json:"metadata,omitempty"`
}

// AddEdge exposes the public record-graph boundary. The lower store also
// supports internal index-node links; public callers must name two retained
// canonical records and supply typed evidence.
func (engine *Engine) AddEdge(ctx context.Context, request AddEdgeRequest) (map[string]any, error) {
	if request.SrcID == "" || request.DstID == "" || request.SrcID != strings.TrimSpace(request.SrcID) ||
		request.DstID != strings.TrimSpace(request.DstID) || len(request.EvidenceRefs) == 0 {
		return nil, errors.New("public memory edges require exact record endpoints and typed evidence refs")
	}
	if request.EdgeType == "supersedes" {
		return nil, errors.New("supersession edges are owned by the canonical memory writer")
	}
	if request.Reason == "agent_authored_from_raw_witness" {
		return nil, errors.New("raw witness provenance edges are owned by the canonical memory writer")
	}
	for _, id := range []string{request.SrcID, request.DstID} {
		record, err := engine.ReadStored(ctx, id)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, errors.New("memory edge endpoints must exist in the selected workspace and profile")
		}
	}
	edge := memory.EdgeRecord{SrcID: request.SrcID, DstID: request.DstID,
		EdgeType: request.EdgeType, WorkspaceID: engine.config.WorkspaceID,
		Reason: request.Reason, Weight: request.Weight,
		EvidenceRefs: request.EvidenceRefs, Metadata: request.Metadata}
	receipt, err := engine.writer.AddEdge(ctx, edge)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "src_id": receipt.SrcID, "dst_id": receipt.DstID,
		"edge_type": receipt.EdgeType, "backend": engine.config.BackendID,
		"workspace_id": engine.config.WorkspaceID, "replayed": receipt.Replayed}, nil
}
