package engine

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
)

// PendingTopologyEvolutions returns only the identities and states of
// unfinished split and merge generations in the selected workspace/profile.
func (engine *Engine) PendingTopologyEvolutions(ctx context.Context) ([]catalog.PendingTopologyEvolution, error) {
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID,
		engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	return opened.PendingTopologyEvolutions(ctx)
}

func (engine *Engine) AbortTopologyEvolution(ctx context.Context,
	operationID, reason string) (map[string]any, error) {
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID,
		engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	aborted, err := opened.AbortTopologyEvolution(ctx, operationID, reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "operation_id": aborted.OperationID,
		"state": aborted.State, "reason": aborted.Reason}, nil
}
