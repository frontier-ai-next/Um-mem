package engine

import (
	"context"
	"errors"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
)

// RebuildIndexes reconstructs the selected backend's materialized SQLite
// indexes from canonical records and the current logical-block catalog.
func (engine *Engine) RebuildIndexes(ctx context.Context) (map[string]any, error) {
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	derived, err := engine.writer.RebuildDerivedRecordIndexes(ctx)
	if err != nil {
		return nil, err
	}
	checksum, err := engine.writer.RebuildBlockDescriptorIndexesWithSnapshots(ctx,
		engine.current.Load().catalog,
		func(ctx context.Context, profileID, blockID string) (store.BlockDescriptorSnapshot, error) {
			if profileID != engine.config.ProfileID {
				return store.BlockDescriptorSnapshot{}, errors.New("descriptor snapshot escaped selected profile")
			}
			return engine.index.LogicalBlockDescriptorSnapshot(ctx, blockID)
		})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "backend": engine.config.BackendID,
		"workspace_id": derived.WorkspaceID, "record_count": derived.RecordCount,
		"index_sha256": checksum}, nil
}
