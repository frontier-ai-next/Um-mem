package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/blockcatalog"
	"github.com/frontier-ai-next/Um-mem/internal/catalog"
)

type MoveBlockRequest struct {
	BlockID          string `json:"block_id"`
	ParentID         string `json:"parent_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Reason           string `json:"reason"`
}

func (engine *Engine) MoveBlock(ctx context.Context, request MoveBlockRequest) (map[string]any, error) {
	id, parentID := strings.TrimSpace(request.BlockID), strings.TrimSpace(request.ParentID)
	reason := strings.TrimSpace(request.Reason)
	if id == "" || parentID == "" || request.ExpectedRevision < 1 || reason == "" {
		return nil, errors.New("move_block requires block_id, parent_id, integer expected_revision, and reason")
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
		return nil, errors.New("protected root cannot be moved")
	}
	if state.Revision != request.ExpectedRevision {
		return nil, errors.New("expected_revision does not match current block revision")
	}
	parent, err := opened.GetBlock(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if parent == nil || parent.Status != "active" || !parent.Block.IndexOnly {
		return nil, errors.New("dynamic memory blocks require an active index-only parent")
	}
	block := state.Block
	block.ParentID = parentID
	if state.Status == "active" {
		if err := engine.RefreshCatalog(); err != nil {
			return nil, err
		}
		candidate := engine.Blocks()
		found := false
		for i := range candidate {
			if candidate[i].BlockID == id {
				candidate[i] = block
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("active memory block is missing from semantic catalog")
		}
		if err := blockcatalog.ValidateTree(candidate); err != nil {
			return nil, err
		}
		if err := blockcatalog.ValidateLeaves(candidate); err != nil {
			return nil, err
		}
	}
	updated, err := opened.ReplaceBlock(ctx, *state, block, catalog.ReplaceOptions{
		ExpectedRevision: request.ExpectedRevision, Status: state.Status,
		Actor: "llm", Reason: reason, Operation: "amend_block"})
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
	payload["archived"] = state.Status == "archived"
	return payload, nil
}
