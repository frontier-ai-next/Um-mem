package engine

import (
	"context"
	"errors"
	"path/filepath"
)

// Health reports the selected Go backend's native reachability and durable
// write-journal state. It does not assert that the whole host service is ready.
func (engine *Engine) Health(ctx context.Context) (map[string]any, error) {
	projection, err := engine.index.NativeProjectionHealth(ctx)
	if err != nil {
		return nil, err
	}
	revisions, err := engine.index.OperationalRevisions(ctx)
	if err != nil {
		return nil, err
	}
	implementation := map[string]string{"hindsight": "hindsight_native",
		"mnemosyne": "mnemosyne_memory_beam", "mempalace": "mempalace_chroma",
		"zvec": "zvec_leaf_collections"}[engine.config.BackendID]
	if engine.config.BackendID == "mempalace" {
		probe, ok := engine.projector.(interface {
			ProbeWorkspace(context.Context, string) error
		})
		if !ok {
			err = errors.New("selected native backend has no workspace health probe")
		} else {
			err = probe.ProbeWorkspace(ctx, engine.config.WorkspaceID)
		}
	} else {
		probe, ok := engine.projector.(interface{ Probe(context.Context) error })
		if !ok {
			err = errors.New("selected native backend has no health probe")
		} else {
			err = probe.Probe(ctx)
		}
	}
	nativeOK := err == nil
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	mode := "native"
	if !nativeOK {
		mode = "unavailable"
	}
	metricHealth := "ready"
	if !nativeOK {
		metricHealth = "unavailable:" + reason
	}
	partitionMetrics, err := engine.NativePartitionMetrics(ctx, metricHealth)
	if err != nil {
		return nil, err
	}
	partitions := make([]map[string]any, 0, len(partitionMetrics))
	metrics := make([]map[string]any, 0, len(partitionMetrics))
	for _, item := range partitionMetrics {
		partitions = append(partitions, map[string]any{
			"backend_id": item.Identity.BackendID, "workspace_id": item.Identity.WorkspaceID,
			"primary_block_id":    item.Identity.PrimaryBlockID,
			"catalog_revision_id": item.Identity.CatalogRevisionID,
			"partition_id":        item.Identity.PartitionID,
			"isolation_class":     item.Identity.IsolationClass,
			"partition_mode":      item.Identity.PartitionMode})
		metrics = append(metrics, item.ToMap())
	}
	root, err := filepath.Abs(engine.config.RepoRoot)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"ok": nativeOK, "backend": engine.config.BackendID,
		"workspace_id": engine.config.WorkspaceID, "profile_id": engine.config.ProfileID,
		"repo_root": root, "selected": true, "native_ok": nativeOK,
		"strict_ok":                      nativeOK && projection.Failed == 0 && projection.Attention == 0,
		"native_block_scoping_supported": nativeOK,
		"effective_mode":                 mode, "reason": reason, "implementation": implementation,
		"projection": projection, "native_projection": projection,
		"native_partition_count": len(partitions), "native_partitions": partitions,
		"block_metrics": metrics}
	for key, value := range revisions {
		result[key] = value
	}
	return result, nil
}
