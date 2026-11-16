package engine

import (
	"context"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/blockcatalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
)

func (engine *Engine) ReviveBlock(ctx context.Context, request BlockTransitionRequest) (map[string]any, error) {
	id, reason := strings.TrimSpace(request.BlockID), strings.TrimSpace(request.Reason)
	if id == "" || request.ExpectedRevision < 1 || reason == "" {
		return nil, errors.New("revive_block requires block_id, integer expected_revision, and reason")
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
		return nil, errors.New("protected root cannot be revived")
	}
	if state.Revision != request.ExpectedRevision {
		return nil, errors.New("expected_revision does not match current block revision")
	}
	parent, err := opened.GetBlock(ctx, state.Block.ParentID)
	if err != nil {
		return nil, err
	}
	if parent == nil || parent.Status != "active" || !parent.Block.IndexOnly {
		return nil, errors.New("revive_block requires an active index-only parent")
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	candidate := engine.Blocks()
	if state.Status == "archived" {
		candidate = append(candidate, state.Block)
	}
	if err := blockcatalog.ValidateTree(candidate); err != nil {
		return nil, err
	}
	if err := blockcatalog.ValidateLeaves(candidate); err != nil {
		return nil, err
	}
	operation := "amend_block"
	if state.Status == "archived" {
		operation = "revive_block"
	}
	updated, err := opened.ReplaceBlock(ctx, *state, state.Block, catalog.ReplaceOptions{
		ExpectedRevision: request.ExpectedRevision, Status: "active", Actor: "llm",
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
	payload["archived"] = false
	return payload, nil
}
