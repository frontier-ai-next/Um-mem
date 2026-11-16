package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type PartitionOperation struct {
	Operation      string
	LatencyMS      float64
	HitCount       int
	RecordBytes    *int64
	RenderedTokens int
	Success        bool
	SearchMode     string
}

func (writer *Writer) RecordPartitionOperation(ctx context.Context,
	identity memory.NativePartitionIdentity, event PartitionOperation) error {
	if identity.BackendID != writer.backendID || identity.WorkspaceID != writer.workspaceID {
		return errors.New("native partition metrics differ from selected writer")
	}
	return recordPartitionOperation(ctx, writer.db, identity, event)
}

// RecordPartitionOperation uses the selected engine writer when one is bound.
// A standalone read-only Index has no observation sink.
func (index *Index) RecordPartitionOperation(ctx context.Context,
	identity memory.NativePartitionIdentity, event PartitionOperation) error {
	if index.metricsWriter == nil {
		return nil
	}
	return index.metricsWriter.RecordPartitionOperation(ctx, identity, event)
}

type partitionEventWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func recordPartitionOperation(ctx context.Context, db partitionEventWriter,
	identity memory.NativePartitionIdentity, event PartitionOperation) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	switch event.Operation {
	case "write", "recall", "render", "consolidation", "compaction", "eviction", "expiry":
	default:
		return errors.New("native partition metrics operation is unsupported")
	}
	if event.SearchMode != "" && (event.Operation != "recall" ||
		event.SearchMode != "scoped" && event.SearchMode != "search_all") {
		return errors.New("native partition metrics search mode is unsupported")
	}
	if math.IsNaN(event.LatencyMS) || math.IsInf(event.LatencyMS, 0) || event.LatencyMS < 0 ||
		event.HitCount < 0 || event.RenderedTokens < 0 || event.RecordBytes != nil && *event.RecordBytes < 0 {
		return errors.New("native partition operation metrics cannot be negative or non-finite")
	}
	var recordBytes any
	if event.RecordBytes != nil {
		recordBytes = *event.RecordBytes
	}
	success := 0
	if event.Success {
		success = 1
	}
	_, err := db.ExecContext(ctx, `INSERT INTO native_partition_events(
		workspace_id,primary_block_id,catalog_revision_id,partition_id,isolation_class,
		partition_mode,operation,latency_ms,hit_count,record_bytes,rendered_tokens,
		success,search_mode,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		identity.WorkspaceID, identity.PrimaryBlockID, identity.CatalogRevisionID,
		identity.PartitionID, identity.IsolationClass, identity.PartitionMode,
		event.Operation, event.LatencyMS, event.HitCount, recordBytes, event.RenderedTokens,
		success, event.SearchMode, time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	return err
}

// PartitionMetrics counts current records through published membership and
// aggregates native events across catalog revisions of the same partition.
func (index *Index) PartitionMetrics(ctx context.Context, identity memory.NativePartitionIdentity,
	backendPartitionID, health string) (memory.NativePartitionMetrics, error) {
	if err := identity.Validate(); err != nil {
		return memory.NativePartitionMetrics{}, err
	}
	if identity.WorkspaceID != index.workspaceID || backendPartitionID == "" {
		return memory.NativePartitionMetrics{}, errors.New("native partition metrics scope is incomplete")
	}
	metrics := memory.NativePartitionMetrics{Identity: identity, Health: health}
	ids, err := index.RecordIDsForLogicalBlocks(ctx, []string{identity.PrimaryBlockID})
	if err != nil {
		return memory.NativePartitionMetrics{}, err
	}
	for _, id := range ids {
		record, err := index.ReadStoredRecord(ctx, id)
		if err != nil {
			return memory.NativePartitionMetrics{}, err
		}
		if record == nil || record.BlockMembership == nil ||
			slices.Contains([]string{"superseded", "deprecated", "retracted", "quarantined", "expired"}, record.Status) {
			continue
		}
		blockID, partitionID := record.BlockMembership.PrimaryBlockID,
			record.BlockMembership.BackendPartitionID
		if index.topology != nil {
			published, err := index.topology.PublishedRecordMembership(ctx, id, record.RevisionID)
			if err != nil {
				return memory.NativePartitionMetrics{}, err
			}
			if published != nil {
				blockID, partitionID = published.BlockID, published.BackendPartitionID
			}
		}
		if blockID == identity.PrimaryBlockID && partitionID == backendPartitionID {
			metrics.RecordCount++
		}
	}
	where := `workspace_id=? AND primary_block_id=? AND partition_id=?`
	args := []any{identity.WorkspaceID, identity.PrimaryBlockID, identity.PartitionID}
	var recordBytes sql.NullInt64
	var lastAccessed sql.NullString
	var positiveLatencies int
	err = index.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(operation='write'),0), COALESCE(SUM(operation='recall'),0),
		COALESCE(SUM(CASE WHEN operation='recall' AND success=1 THEN hit_count ELSE 0 END),0),
		COALESCE(SUM(operation='recall' AND success=1 AND hit_count=0),0),
		COALESCE(SUM(success=0),0), SUM(record_bytes), COALESCE(SUM(rendered_tokens),0),
		COALESCE(SUM(operation='consolidation'),0), COALESCE(SUM(operation='compaction'),0),
		COALESCE(SUM(operation='eviction'),0), COALESCE(SUM(operation='expiry'),0),
		COALESCE(SUM(operation='recall' AND search_mode='scoped'),0),
		COALESCE(SUM(operation='recall' AND search_mode='search_all'),0),
		COALESCE(SUM(latency_ms>0),0),
		(SELECT recorded_at FROM native_partition_events WHERE `+where+` ORDER BY id DESC LIMIT 1)
		FROM native_partition_events WHERE `+where,
		append(append([]any{}, args...), args...)...).Scan(
		&metrics.WriteCount, &metrics.RecallCount, &metrics.HitCount,
		&metrics.ZeroHitCount, &metrics.FailureCount, &recordBytes,
		&metrics.RenderedTokens, &metrics.ConsolidationCount, &metrics.CompactionCount,
		&metrics.EvictionCount, &metrics.ExpiryCount, &metrics.ScopedRecallCount,
		&metrics.BroadRecallCount, &positiveLatencies, &lastAccessed)
	if err != nil {
		return memory.NativePartitionMetrics{}, err
	}
	if recordBytes.Valid {
		metrics.RecordBytes = &recordBytes.Int64
	}
	if lastAccessed.Valid {
		metrics.LastAccessedAt = lastAccessed.String
	}
	if positiveLatencies > 0 {
		p50, p95 := (positiveLatencies+1)/2-1, (positiveLatencies*95+99)/100-1
		percentileQuery := `SELECT
			(SELECT latency_ms FROM native_partition_events WHERE ` + where + `
			AND latency_ms>0 ORDER BY latency_ms,id LIMIT 1 OFFSET ?),
			(SELECT latency_ms FROM native_partition_events WHERE ` + where + `
			AND latency_ms>0 ORDER BY latency_ms,id LIMIT 1 OFFSET ?)`
		percentileArgs := append(append(append([]any{}, args...), p50), args...)
		percentileArgs = append(percentileArgs, p95)
		if err := index.db.QueryRowContext(ctx, percentileQuery, percentileArgs...).Scan(
			&metrics.LatencyP50MS, &metrics.LatencyP95MS); err != nil {
			return memory.NativePartitionMetrics{}, err
		}
		metrics.LatencyP50MS = math.Round(metrics.LatencyP50MS*1000) / 1000
		metrics.LatencyP95MS = math.Round(metrics.LatencyP95MS*1000) / 1000
	}
	return metrics, nil
}
