package engine

import (
	"context"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/blockcatalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"github.com/google/uuid"
)

type BlockMergeSource struct {
	BlockID          string `json:"block_id"`
	ExpectedRevision int    `json:"expected_revision"`
}

type MergeBlocksRequest struct {
	Sources     []BlockMergeSource   `json:"sources"`
	Target      BlockEvolutionTarget `json:"target"`
	ParentID    string               `json:"parent_id"`
	Reason      string               `json:"reason"`
	OperationID string               `json:"operation_id,omitempty"`
}

func (engine *Engine) MergeBlocks(ctx context.Context,
	request MergeBlocksRequest) (map[string]any, error) {
	if len(request.Sources) < 2 || request.ParentID == "" ||
		request.ParentID != strings.TrimSpace(request.ParentID) ||
		strings.TrimSpace(request.Reason) == "" ||
		request.OperationID != strings.TrimSpace(request.OperationID) ||
		len(request.OperationID) > 256 {
		return nil, errors.New("merge requires at least two sources, a target parent, reason, and bounded operation_id")
	}
	seen := make(map[string]bool, len(request.Sources))
	sourceRequest := make([]any, 0, len(request.Sources))
	sourceRevisions := make(map[string]int, len(request.Sources))
	sourcePayload := make(map[string]any, len(request.Sources))
	for _, source := range request.Sources {
		if source.BlockID == "" || source.BlockID != strings.TrimSpace(source.BlockID) ||
			source.ExpectedRevision < 1 || seen[source.BlockID] {
			return nil, errors.New("merge source blocks must be unique with positive expected revisions")
		}
		seen[source.BlockID] = true
		sourceRevisions[source.BlockID] = source.ExpectedRevision
		sourcePayload[source.BlockID] = source.ExpectedRevision
		sourceRequest = append(sourceRequest, map[string]any{
			"block_id": source.BlockID, "expected_revision": source.ExpectedRevision})
	}
	target, err := normalizeEvolutionTarget(request.Target)
	if err != nil {
		return nil, err
	}
	requestHash, err := memory.PythonHashValue(map[string]any{
		"operation": "merge", "sources": sourceRequest,
		"target":    topologyTargetRequestMapping(target),
		"parent_id": request.ParentID, "reason": request.Reason})
	if err != nil {
		return nil, err
	}
	operationID := request.OperationID
	if operationID == "" {
		operationID = uuid.NewString()
	} else {
		existing, err := engine.topology.TopologyEvolution(ctx, operationID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if existing.Payload["request_hash"] != requestHash {
				return nil, errors.New("operation_id already binds a different topology request")
			}
			return engine.ResumeTopologyEvolution(ctx, operationID)
		}
	}
	parent, err := engine.topology.GetBlock(ctx, request.ParentID)
	if err != nil {
		return nil, err
	}
	if parent == nil || parent.Status != "active" || !parent.Block.IndexOnly {
		return nil, errors.New("merge target parent must be an active index block")
	}
	sourceBlocks := make([]memory.Block, 0, len(request.Sources))
	for _, source := range request.Sources {
		state, err := engine.topology.GetBlock(ctx, source.BlockID)
		if err != nil {
			return nil, err
		}
		if state == nil || state.Status != "active" || state.Block.IndexOnly {
			return nil, errors.New("merge sources must be active leaves in this profile")
		}
		if state.Revision != source.ExpectedRevision {
			return nil, errors.New("expected_revision does not match a merge source")
		}
		cursor := ""
		for {
			page, err := engine.topology.PageEdges(ctx, source.BlockID, 200, cursor, nil)
			if err != nil {
				return nil, err
			}
			for _, edge := range page.Edges {
				if edge.DstID == source.BlockID {
					return nil, errors.New("merge source incoming edges must be relocated first")
				}
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		masked, err := engine.topology.BlockHasActiveMaskReference(ctx, source.BlockID)
		if err != nil {
			return nil, err
		}
		if masked {
			return nil, errors.New("merge source has active mask references")
		}
		sourceBlocks = append(sourceBlocks, state.Block)
	}
	for _, block := range sourceBlocks[1:] {
		first := sourceBlocks[0]
		if block.Authority != first.Authority || block.Scope != first.Scope ||
			block.Retention != first.Retention {
			return nil, errors.New("merge sources have incompatible governance")
		}
	}
	current, err := engine.topologyCurrentRows(ctx, sourceRevisions)
	if err != nil {
		return nil, err
	}
	if len(target.recordIDs) != len(current) {
		return nil, errors.New("merge target record mapping must exactly cover all current records")
	}
	for _, id := range target.recordIDs {
		if _, found := current[id]; !found {
			return nil, errors.New("merge target record mapping must exactly cover all current records")
		}
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	block, partitionID, err := engine.evolutionTargetBlock(sourceBlocks[0], target,
		request.ParentID, operationID, 0)
	if err != nil {
		return nil, err
	}
	candidate := append(engine.Blocks(), block)
	if err := blockcatalog.ValidateTree(candidate); err != nil {
		return nil, err
	}
	if err := blockcatalog.ValidateLeaves(candidate); err != nil {
		return nil, err
	}
	assignments := make(map[string]any, len(current))
	for _, id := range target.recordIDs {
		assignments[id] = map[string]any{"record_revision_id": current[id].Record.RevisionID,
			"target_block_id": block.BlockID}
	}
	payload := map[string]any{"operation": "merge", "sources": sourcePayload,
		"targets": []any{map[string]any{"block": block,
			"backend_partition_id": partitionID}},
		"assignments": assignments, "reason": request.Reason,
		"actor": "llm", "request_hash": requestHash}
	if _, err := engine.topology.PrepareTopologyEvolution(ctx, operationID, payload); err != nil {
		return nil, err
	}
	return engine.ResumeTopologyEvolution(ctx, operationID)
}
