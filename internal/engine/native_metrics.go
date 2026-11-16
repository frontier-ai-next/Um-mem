package engine

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type nativePartitionIdentityProvider interface {
	PartitionIdentity(memory.MemoryRecord) (memory.NativePartitionIdentity, error)
}

// NativePartitionMetrics reports current record counts and durable observations
// for the selected backend's physical leaves.
func (engine *Engine) NativePartitionMetrics(ctx context.Context, health string,
	blockIDs ...string) ([]memory.NativePartitionMetrics, error) {
	provider, ok := engine.projector.(nativePartitionIdentityProvider)
	if !ok {
		return nil, errors.New("selected backend has no native partition identity")
	}
	memberships, err := engine.index.NativePartitionMemberships(ctx, blockIDs...)
	if err != nil {
		return nil, err
	}
	result := make([]memory.NativePartitionMetrics, 0, len(memberships))
	for _, membership := range memberships {
		record := memory.MemoryRecord{WorkspaceID: engine.config.WorkspaceID,
			BlockMembership: &memory.RecordBlockMembership{
				PrimaryBlockID:     membership.LogicalBlockID,
				CatalogRevisionID:  membership.CatalogRevisionID,
				BackendPartitionID: membership.BackendPartitionID}}
		identity, err := provider.PartitionIdentity(record)
		if err != nil {
			return nil, err
		}
		metrics, err := engine.index.PartitionMetrics(ctx, identity, membership.BackendPartitionID, health)
		if err != nil {
			return nil, err
		}
		result = append(result, metrics)
	}
	slices.SortFunc(result, func(a, b memory.NativePartitionMetrics) int {
		if a.Identity.PrimaryBlockID < b.Identity.PrimaryBlockID {
			return -1
		}
		if a.Identity.PrimaryBlockID > b.Identity.PrimaryBlockID {
			return 1
		}
		if a.Identity.PartitionID < b.Identity.PartitionID {
			return -1
		}
		if a.Identity.PartitionID > b.Identity.PartitionID {
			return 1
		}
		return 0
	})
	return result, nil
}

// BlockHealth reports stored observations for a bounded visible navigation
// page. It intentionally does not probe or open the native provider.
func (engine *Engine) BlockHealth(ctx context.Context, blockIDs []string) (map[string]any, error) {
	if len(blockIDs) < 1 || len(blockIDs) > 201 {
		return nil, errors.New("block health requires 1-201 selected block IDs")
	}
	for _, id := range blockIDs {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, errors.New("block health requires exact block IDs")
		}
	}
	observations, err := engine.NativePartitionMetrics(ctx, "not_probed", blockIDs...)
	if err != nil {
		return nil, err
	}
	partitions := make([]map[string]any, 0, len(observations))
	metrics := make([]map[string]any, 0, len(observations))
	for _, item := range observations {
		partitions = append(partitions, map[string]any{
			"backend_id": item.Identity.BackendID, "workspace_id": item.Identity.WorkspaceID,
			"primary_block_id":    item.Identity.PrimaryBlockID,
			"catalog_revision_id": item.Identity.CatalogRevisionID,
			"partition_id":        item.Identity.PartitionID,
			"isolation_class":     item.Identity.IsolationClass,
			"partition_mode":      item.Identity.PartitionMode})
		row := item.ToMap()
		row["profile_id"] = engine.config.ProfileID
		row["aggregates_catalog_revisions"] = true
		metrics = append(metrics, row)
	}
	return map[string]any{"ok": true, "readiness": "not_probed",
		"metrics_scope": "native_partition", "profile_id": engine.config.ProfileID,
		"native_partitions": partitions, "block_metrics": metrics}, nil
}

// RecordRenderedTokens attributes the final context budget to records owned by
// this selected workspace. Manager-layer records belong to their own ledger.
func (engine *Engine) RecordRenderedTokens(ctx context.Context,
	records []memory.MemoryRecord, allocations map[string]int) error {
	provider, ok := engine.projector.(nativePartitionIdentityProvider)
	if !ok {
		return errors.New("selected backend has no native partition identity")
	}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		count, selected := allocations[record.BlockID]
		if !selected || seen[record.BlockID] || record.WorkspaceID != engine.config.WorkspaceID ||
			record.ProfileID != engine.config.ProfileID {
			continue
		}
		if count < 0 || record.BlockMembership == nil {
			return errors.New("rendered token allocation is invalid")
		}
		seen[record.BlockID] = true
		identity, err := provider.PartitionIdentity(record)
		if err != nil {
			return err
		}
		if err := engine.writer.RecordPartitionOperation(ctx, identity,
			store.PartitionOperation{Operation: "render", RenderedTokens: count, Success: true}); err != nil {
			return err
		}
	}
	return nil
}
