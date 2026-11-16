package engine

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
)

// ProjectionBarrier observes one durable record until its latest native write
// reaches a terminal state or the deadline expires. It never starts recovery.
func (engine *Engine) ProjectionBarrier(ctx context.Context, recordID string, commitRevision int64, timeoutSeconds float64) (map[string]any, error) {
	recordID = strings.TrimSpace(recordID)
	if recordID == "" {
		return nil, errors.New("projection_barrier requires record_id")
	}
	if commitRevision < 0 {
		return nil, errors.New("projection_barrier requires a non-negative commit_revision")
	}
	if math.IsNaN(timeoutSeconds) || math.IsInf(timeoutSeconds, 0) || timeoutSeconds < 0 {
		return nil, errors.New("projection_barrier requires a finite non-negative timeout")
	}
	started := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		state, err := engine.index.ReadProjectionState(ctx, recordID)
		if err != nil {
			return nil, err
		}
		if state == nil {
			return nil, errors.New("memory block was not found in the selected workspace and profile")
		}
		revision := state.CommitRevision
		if commitRevision != 0 && revision != commitRevision {
			projected := int64(0)
			if state.Status == "committed" {
				projected = revision
			}
			return map[string]any{"ok": false, "record_id": recordID,
				"projection_status": "unknown", "commit_revision": revision,
				"projection_revision": projected, "reason_code": "projection_revision_mismatch",
				"attention_required": true, "retryable": false}, nil
		}
		if state.Status == "committed" {
			return map[string]any{"ok": true, "record_id": recordID,
				"projection_status": "committed", "commit_revision": revision,
				"projection_revision": revision, "retryable": false}, nil
		}
		if state.Status == "failed" || state.RetryExhausted {
			return map[string]any{"ok": false, "record_id": recordID,
				"projection_status": state.Status, "commit_revision": revision,
				"projection_revision": int64(0), "reason_code": "native_projection_attention",
				"attention_required": true, "retryable": false}, nil
		}
		remaining := timeoutSeconds - time.Since(started).Seconds()
		if remaining <= 0 {
			return map[string]any{"ok": false, "record_id": recordID,
				"projection_status": "pending", "commit_revision": revision,
				"projection_revision": int64(0), "reason_code": "projection_deadline_exceeded",
				"attention_required": false, "retryable": true}, nil
		}
		pause := time.Duration(math.Min(0.05, remaining) * float64(time.Second))
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
