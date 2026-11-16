package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
)

// ProtectBlock makes an existing dynamic block host-owned. The catalog records
// the revisioned mutation, and a stale revision cannot protect a newer block.
func (engine *Engine) ProtectBlock(ctx context.Context, request BlockTransitionRequest) (map[string]any, error) {
	id, reason := strings.TrimSpace(request.BlockID), strings.TrimSpace(request.Reason)
	if id == "" || request.ExpectedRevision < 1 || reason == "" {
		return nil, errors.New("protect_block requires block_id, expected_revision, and reason")
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
	protected, err := opened.ProtectBlock(ctx, *state, request.ExpectedRevision, "system", reason)
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
	return blockStatePayload(protected, revision)
}
