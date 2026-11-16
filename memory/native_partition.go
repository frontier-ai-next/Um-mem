package memory

import (
	"errors"
	"strings"
)

// NativePartitionIdentity describes one selected backend's physical leaf.
type NativePartitionIdentity struct {
	BackendID         string `json:"backend_id"`
	WorkspaceID       string `json:"workspace_id"`
	PrimaryBlockID    string `json:"primary_block_id"`
	CatalogRevisionID string `json:"catalog_revision_id"`
	PartitionID       string `json:"partition_id"`
	IsolationClass    string `json:"isolation_class"`
	PartitionMode     string `json:"partition_mode"`
}

func (identity NativePartitionIdentity) Validate() error {
	switch identity.BackendID {
	case "mempalace", "hindsight", "mnemosyne", "zvec":
	default:
		return errors.New("native partition backend is unsupported")
	}
	if strings.TrimSpace(identity.WorkspaceID) == "" || strings.TrimSpace(identity.PrimaryBlockID) == "" ||
		strings.TrimSpace(identity.CatalogRevisionID) == "" || strings.TrimSpace(identity.PartitionID) == "" ||
		strings.TrimSpace(identity.PartitionMode) == "" {
		return errors.New("native partition identity is incomplete")
	}
	if identity.IsolationClass != "physical" && identity.IsolationClass != "native_tag" {
		return errors.New("native partition isolation class is unsupported")
	}
	return nil
}

// NativePartitionMetrics aggregates durable observations for a physical leaf.
type NativePartitionMetrics struct {
	Identity           NativePartitionIdentity `json:"identity"`
	RecordCount        int                     `json:"record_count"`
	WriteCount         int                     `json:"write_count"`
	RecallCount        int                     `json:"recall_count"`
	HitCount           int                     `json:"hit_count"`
	ZeroHitCount       int                     `json:"zero_hit_count"`
	FailureCount       int                     `json:"failure_count"`
	RecordBytes        *int64                  `json:"record_bytes"`
	LatencyP50MS       float64                 `json:"latency_p50_ms"`
	LatencyP95MS       float64                 `json:"latency_p95_ms"`
	RenderedTokens     int                     `json:"rendered_tokens"`
	ConsolidationCount int                     `json:"consolidation_count"`
	CompactionCount    int                     `json:"compaction_count"`
	EvictionCount      int                     `json:"eviction_count"`
	ExpiryCount        int                     `json:"expiry_count"`
	ScopedRecallCount  int                     `json:"scoped_recall_count"`
	BroadRecallCount   int                     `json:"broad_recall_count"`
	LastAccessedAt     string                  `json:"last_accessed_at"`
	Health             string                  `json:"health"`
}

func (metrics NativePartitionMetrics) ToMap() map[string]any {
	var recordBytes any
	if metrics.RecordBytes != nil {
		recordBytes = *metrics.RecordBytes
	}
	return map[string]any{
		"backend_id": metrics.Identity.BackendID, "workspace_id": metrics.Identity.WorkspaceID,
		"primary_block_id":    metrics.Identity.PrimaryBlockID,
		"catalog_revision_id": metrics.Identity.CatalogRevisionID,
		"partition_id":        metrics.Identity.PartitionID,
		"isolation_class":     metrics.Identity.IsolationClass,
		"partition_mode":      metrics.Identity.PartitionMode,
		"record_count":        metrics.RecordCount, "write_count": metrics.WriteCount,
		"recall_count": metrics.RecallCount, "hit_count": metrics.HitCount,
		"zero_hit_count": metrics.ZeroHitCount, "failure_count": metrics.FailureCount,
		"record_bytes": recordBytes, "latency_p50_ms": metrics.LatencyP50MS,
		"latency_p95_ms": metrics.LatencyP95MS, "rendered_tokens": metrics.RenderedTokens,
		"consolidation_count": metrics.ConsolidationCount,
		"compaction_count":    metrics.CompactionCount, "eviction_count": metrics.EvictionCount,
		"expiry_count": metrics.ExpiryCount, "scoped_recall_count": metrics.ScopedRecallCount,
		"broad_recall_count": metrics.BroadRecallCount,
		"last_accessed_at":   metrics.LastAccessedAt, "health": metrics.Health,
	}
}
