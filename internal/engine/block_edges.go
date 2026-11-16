package engine

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
)

func (engine *Engine) AddBlockEdge(ctx context.Context, srcID, dstID, edgeType, reason string) (catalog.BlockGraphEdge, error) {
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return catalog.BlockGraphEdge{}, err
	}
	defer opened.Close()
	return opened.AddBlockEdge(ctx, srcID, dstID, edgeType, reason, "llm")
}

func (engine *Engine) RemoveBlockEdge(ctx context.Context, edgeID string, expectedRevision int, reason string) (catalog.BlockGraphEdge, error) {
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return catalog.BlockGraphEdge{}, err
	}
	defer opened.Close()
	return opened.RemoveBlockEdge(ctx, edgeID, expectedRevision, reason, "llm")
}
