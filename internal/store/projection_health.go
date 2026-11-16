package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ProjectionHealth describes unresolved native writes without exposing record
// content. Go retries through Reconcile rather than a background worker pool.
type ProjectionHealth struct {
	Pending          int     `json:"pending"`
	Failed           int     `json:"failed"`
	Attention        int     `json:"attention"`
	ActivePartitions int     `json:"active_partitions"`
	MaxWorkers       int     `json:"max_workers"`
	WorkerError      string  `json:"worker_error"`
	OldestPendingMS  float64 `json:"oldest_pending_ms"`
	Recovery         string  `json:"recovery"`
}

func (index *Index) NativeProjectionHealth(ctx context.Context) (ProjectionHealth, error) {
	health := ProjectionHealth{Recovery: "reconcile"}
	rows, err := index.db.QueryContext(ctx, `SELECT status,error,created_at FROM write_outbox
		WHERE workspace_id=? AND status!='committed'`, index.workspaceID)
	if err != nil {
		return ProjectionHealth{}, err
	}
	defer rows.Close()
	var oldest time.Time
	for rows.Next() {
		var status, reason, created string
		if err := rows.Scan(&status, &reason, &created); err != nil {
			return ProjectionHealth{}, err
		}
		if status != "pending" {
			health.Failed++
			continue
		}
		health.Pending++
		var state struct {
			Kind     string `json:"kind"`
			Attempts int    `json:"attempts"`
		}
		if json.Unmarshal([]byte(reason), &state) == nil &&
			state.Kind == "native_projection" && state.Attempts >= 3 {
			health.Attention++
		}
		stamp, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return ProjectionHealth{}, fmt.Errorf("native projection journal has invalid created_at: %w", err)
		}
		if oldest.IsZero() || stamp.Before(oldest) {
			oldest = stamp
		}
	}
	if err := rows.Err(); err != nil {
		return ProjectionHealth{}, err
	}
	if !oldest.IsZero() {
		health.OldestPendingMS = max(0, float64(time.Since(oldest))/float64(time.Millisecond))
	}
	return health, nil
}
