package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/blockcatalog"
	"github.com/frontier-ai-next/Um-mem/internal/catalog"
)

type AmendBlockRequest struct {
	BlockID          string         `json:"block_id"`
	ExpectedRevision int            `json:"expected_revision"`
	Reason           string         `json:"reason"`
	Name             *string        `json:"name,omitempty"`
	Synopsis         *string        `json:"synopsis,omitempty"`
	Tags             *[]string      `json:"tags,omitempty"`
	MetadataPatch    map[string]any `json:"metadata_patch,omitempty"`
	TokenBudget      *int           `json:"token_budget,omitempty"`
	ExpiresAt        *string        `json:"expires_at,omitempty"`
}

func (engine *Engine) AmendBlock(ctx context.Context, request AmendBlockRequest) (map[string]any, error) {
	id, reason := strings.TrimSpace(request.BlockID), strings.TrimSpace(request.Reason)
	if id == "" || request.ExpectedRevision < 1 || reason == "" {
		return nil, errors.New("amend_block requires block_id, integer expected_revision, and reason")
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
		return nil, errors.New("protected root cannot be amended")
	}
	if state.Revision != request.ExpectedRevision {
		return nil, errors.New("expected_revision does not match current block revision")
	}
	block := state.Block
	if request.Name != nil {
		block.Name = strings.TrimSpace(*request.Name)
		if block.Name == "" {
			return nil, errors.New("dynamic memory block name cannot be empty")
		}
	}
	block.Guidance = make(map[string]any, len(state.Block.Guidance))
	for key, value := range state.Block.Guidance {
		block.Guidance[key] = value
	}
	if request.Synopsis != nil {
		synopsis := strings.TrimSpace(*request.Synopsis)
		if synopsis == "" {
			return nil, errors.New("dynamic memory block synopsis cannot be empty")
		}
		block.Guidance["synopsis"] = synopsis
	}
	if request.Tags != nil {
		block.Tags = append([]string{}, (*request.Tags)...)
	}
	block.Metadata = make(map[string]any, len(state.Block.Metadata)+len(request.MetadataPatch))
	for key, value := range state.Block.Metadata {
		block.Metadata[key] = value
	}
	for key, value := range request.MetadataPatch {
		if value == nil {
			delete(block.Metadata, key)
		} else {
			block.Metadata[key] = value
		}
	}
	if request.TokenBudget != nil {
		if *request.TokenBudget < 0 {
			return nil, errors.New("dynamic memory block token_budget must be non-negative")
		}
		block.TokenBudget = *request.TokenBudget
	}
	block.LifecyclePolicy = make(map[string]any, len(state.Block.LifecyclePolicy))
	for key, value := range state.Block.LifecyclePolicy {
		block.LifecyclePolicy[key] = value
	}
	if request.ExpiresAt != nil {
		if expiry := strings.TrimSpace(*request.ExpiresAt); expiry != "" {
			block.LifecyclePolicy["expires_at"] = expiry
		} else {
			delete(block.LifecyclePolicy, "expires_at")
		}
	}
	block.Normalize()
	if err := block.Validate(); err != nil {
		return nil, err
	}
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
		ExpectedRevision: request.ExpectedRevision, Status: state.Status, Actor: "llm",
		Reason: reason, Operation: "amend_block"})
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
	payload["archived"] = updated.Status == "archived"
	return payload, nil
}
