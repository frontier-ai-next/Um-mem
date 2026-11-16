package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
)

type BlockTransitionRequest struct {
	BlockID          string `json:"block_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Reason           string `json:"reason"`
}

func (engine *Engine) ArchiveBlock(ctx context.Context, request BlockTransitionRequest) (map[string]any, error) {
	id, reason := strings.TrimSpace(request.BlockID), strings.TrimSpace(request.Reason)
	if id == "" || request.ExpectedRevision < 1 || reason == "" {
		return nil, errors.New("archive_block requires block_id, integer expected_revision, and reason")
	}
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	state, err := opened.GetBlock(ctx, id)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, errors.New("dynamic memory block was not found in this profile")
	}
	if state.Protected {
		return nil, errors.New("protected root cannot be archived")
	}
	if state.Revision != request.ExpectedRevision {
		return nil, errors.New("expected_revision does not match current block revision")
	}
	block := state.Block
	block.Metadata = make(map[string]any, len(state.Block.Metadata)+1)
	for key, value := range state.Block.Metadata {
		block.Metadata[key] = value
	}
	block.Metadata["archive_reason"] = reason
	operation := "archive_block"
	if state.Status == "archived" {
		operation = "amend_block"
	}
	updated, err := opened.ReplaceBlock(ctx, *state, block, catalog.ReplaceOptions{
		ExpectedRevision: request.ExpectedRevision, Status: "archived", Actor: "llm",
		Reason: reason, Operation: operation})
	if err != nil {
		return nil, err
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	revision, err := opened.CatalogRevision(ctx)
	if err != nil {
		return nil, err
	}
	payload, err := blockStatePayload(updated, revision)
	if err != nil {
		return nil, err
	}
	payload["previous_revision"] = state.Revision
	payload["archived"] = true
	payload["reason"] = reason
	return payload, nil
}
