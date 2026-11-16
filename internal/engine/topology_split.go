package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/blockcatalog"
	"github.com/frontier-ai-next/Um-mem/memory"
	"github.com/google/uuid"
)

type BlockEvolutionTarget struct {
	Name        string         `json:"name"`
	Synopsis    string         `json:"synopsis"`
	RecordIDs   []string       `json:"record_ids"`
	Tags        []string       `json:"tags,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	TokenBudget *int           `json:"token_budget,omitempty"`
}

type SplitBlockRequest struct {
	BlockID          string                 `json:"block_id"`
	ExpectedRevision int                    `json:"expected_revision"`
	Targets          []BlockEvolutionTarget `json:"targets"`
	Reason           string                 `json:"reason"`
	OperationID      string                 `json:"operation_id,omitempty"`
}

type normalizedEvolutionTarget struct {
	name        string
	synopsis    string
	recordIDs   []string
	tags        []string
	metadata    map[string]any
	tokenBudget int
}

func (engine *Engine) evolutionTargetBlock(source memory.Block,
	target normalizedEvolutionTarget, parentID, operationID string,
	ordinal int) (memory.Block, string, error) {
	namespace := fmt.Sprintf("um-mem:%s:%s:%s:%d", engine.config.WorkspaceID,
		engine.config.ProfileID, operationID, ordinal)
	block := source
	block.BlockID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(namespace+":block")).String()
	block.ParentID = parentID
	block.Name = target.name
	block.Guidance = map[string]any{"synopsis": target.synopsis,
		"use_when": []any{}, "avoid_when": []any{}}
	block.Tags = append([]string{}, target.tags...)
	block.Metadata = target.metadata
	block.TokenBudget = target.tokenBudget
	block.Normalize()
	if err := block.Validate(); err != nil {
		return memory.Block{}, "", err
	}
	partitionID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(namespace+":partition")).String()
	return block, partitionID, nil
}

func exactUniqueTexts(values []string, field string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("topology %s requires nonempty values", field)
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result, nil
}

func normalizeEvolutionTarget(target BlockEvolutionTarget) (normalizedEvolutionTarget, error) {
	name, synopsis := strings.TrimSpace(target.Name), strings.TrimSpace(target.Synopsis)
	if name == "" || synopsis == "" {
		return normalizedEvolutionTarget{}, errors.New("topology target requires name and synopsis")
	}
	recordIDs, err := exactUniqueTexts(target.RecordIDs, "record_ids")
	if err != nil {
		return normalizedEvolutionTarget{}, err
	}
	tags, err := exactUniqueTexts(target.Tags, "tags")
	if err != nil {
		return normalizedEvolutionTarget{}, err
	}
	metadata := make(map[string]any, len(target.Metadata))
	for key, value := range target.Metadata {
		metadata[key] = value
	}
	if _, err := memory.PythonHashValue(metadata); err != nil {
		return normalizedEvolutionTarget{}, errors.New("topology target metadata is not JSON serializable")
	}
	budget := 1000
	if target.TokenBudget != nil {
		budget = *target.TokenBudget
	}
	if budget < 0 {
		return normalizedEvolutionTarget{}, errors.New("topology target token_budget must be nonnegative")
	}
	return normalizedEvolutionTarget{name: name, synopsis: synopsis,
		recordIDs: recordIDs, tags: tags, metadata: metadata, tokenBudget: budget}, nil
}

func topologyTargetRequestMapping(target normalizedEvolutionTarget) map[string]any {
	return map[string]any{"name": target.name, "synopsis": target.synopsis,
		"record_ids": target.recordIDs, "tags": target.tags,
		"metadata": target.metadata, "token_budget": target.tokenBudget}
}

func (engine *Engine) splitSourceReady(ctx context.Context, blockID string,
	expectedRevision int) (memory.Block, error) {
	source, err := engine.topology.GetBlock(ctx, blockID)
	if err != nil {
		return memory.Block{}, err
	}
	if source == nil || source.Status != "active" || source.Protected || source.Block.IndexOnly ||
		source.Revision != expectedRevision {
		return memory.Block{}, errors.New("split source must be an active agent-writable leaf at expected_revision")
	}
	states, err := engine.topology.ListBlocks(ctx, false)
	if err != nil {
		return memory.Block{}, err
	}
	for _, state := range states {
		if state.Block.ParentID == blockID {
			return memory.Block{}, errors.New("split source must not have active children")
		}
	}
	cursor := ""
	for {
		page, err := engine.topology.PageEdges(ctx, blockID, 200, cursor, nil)
		if err != nil {
			return memory.Block{}, err
		}
		for _, edge := range page.Edges {
			if edge.SrcID != blockID || edge.EdgeType != "derived_from" {
				return memory.Block{}, errors.New("split source graph edges must be relocated first")
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	masked, err := engine.topology.BlockHasActiveMaskReference(ctx, blockID)
	if err != nil {
		return memory.Block{}, err
	}
	if masked {
		return memory.Block{}, errors.New("split source has active mask references")
	}
	return source.Block, nil
}

func (engine *Engine) SplitBlock(ctx context.Context,
	request SplitBlockRequest) (map[string]any, error) {
	if request.BlockID == "" || request.BlockID != strings.TrimSpace(request.BlockID) ||
		request.ExpectedRevision < 1 || len(request.Targets) == 0 ||
		strings.TrimSpace(request.Reason) == "" ||
		request.OperationID != strings.TrimSpace(request.OperationID) || len(request.OperationID) > 256 {
		return nil, errors.New("split requires an exact source, revision, targets, reason, and bounded operation_id")
	}
	targets := make([]normalizedEvolutionTarget, 0, len(request.Targets))
	requestTargets := make([]any, 0, len(request.Targets))
	for _, target := range request.Targets {
		normalized, err := normalizeEvolutionTarget(target)
		if err != nil {
			return nil, err
		}
		targets = append(targets, normalized)
		requestTargets = append(requestTargets, topologyTargetRequestMapping(normalized))
	}
	requestHash, err := memory.PythonHashValue(map[string]any{
		"operation": "split", "source": request.BlockID,
		"expected_revision": request.ExpectedRevision,
		"targets":           requestTargets, "reason": request.Reason})
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
	source, err := engine.splitSourceReady(ctx, request.BlockID, request.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	current, err := engine.topologyCurrentRows(ctx, map[string]int{request.BlockID: request.ExpectedRevision})
	if err != nil {
		return nil, err
	}
	assigned := make(map[string]bool)
	for _, target := range targets {
		for _, id := range target.recordIDs {
			if assigned[id] {
				return nil, errors.New("split must assign every current record exactly once")
			}
			assigned[id] = true
		}
	}
	if len(assigned) != len(current) {
		return nil, errors.New("split record mapping must exactly cover current records")
	}
	for id := range assigned {
		if _, found := current[id]; !found {
			return nil, errors.New("split record mapping must exactly cover current records")
		}
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	targetRows := make([]any, 0, len(targets))
	assignments := make(map[string]any, len(current))
	candidate := engine.Blocks()
	for ordinal, target := range targets {
		block, partitionID, err := engine.evolutionTargetBlock(source, target,
			source.ParentID, operationID, ordinal)
		if err != nil {
			return nil, err
		}
		targetRows = append(targetRows, map[string]any{"block": block,
			"backend_partition_id": partitionID})
		candidate = append(candidate, block)
		for _, id := range target.recordIDs {
			assignments[id] = map[string]any{"record_revision_id": current[id].Record.RevisionID,
				"target_block_id": block.BlockID}
		}
	}
	if err := blockcatalog.ValidateTree(candidate); err != nil {
		return nil, err
	}
	if err := blockcatalog.ValidateLeaves(candidate); err != nil {
		return nil, err
	}
	payload := map[string]any{"operation": "split",
		"sources": map[string]any{request.BlockID: request.ExpectedRevision},
		"targets": targetRows, "assignments": assignments,
		"reason": request.Reason, "actor": "llm", "request_hash": requestHash}
	if _, err := engine.topology.PrepareTopologyEvolution(ctx, operationID, payload); err != nil {
		return nil, err
	}
	return engine.ResumeTopologyEvolution(ctx, operationID)
}
