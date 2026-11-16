package engine

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type ReconcileReport struct {
	Backend               string `json:"backend"`
	DryRun                bool   `json:"dry_run"`
	PendingCanonicalHeads int    `json:"pending_canonical_heads"`
	PendingWriteOutbox    int    `json:"pending_write_outbox"`
	Repaired              int    `json:"repaired"`
	Remaining             int    `json:"remaining"`
	Failures              int    `json:"failures"`
	ResultCode            string `json:"result_code"`
	ReasonCode            string `json:"reason_code"`
	Timestamp             string `json:"timestamp"`
}

func (engine *Engine) unresolved(ctx context.Context) (headPending, headFailed, writePending, writeFailed int, revisions []int64, err error) {
	headPending, headFailed, err = engine.index.HeadInventory(ctx)
	if err != nil {
		return
	}
	rows, err := engine.index.UnresolvedOutbox(ctx)
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.Backend != engine.recordNativeStore(row.Record) || row.Revision < 1 {
			err = fmt.Errorf("pending projection %q has invalid backend or revision", row.RecordID)
			return
		}
		if row.Status == "pending" {
			writePending++
			revisions = append(revisions, row.Revision)
		} else {
			writeFailed++
		}
	}
	return
}

// Reconcile reports and optionally retries canonical writes whose native
// projection was interrupted. The same immutable journal payload is replayed;
// native verification remains the commit gate.
func (engine *Engine) Reconcile(ctx context.Context, dryRun bool) (ReconcileReport, error) {
	initialHead, initialHeadFailed, initialWrite, initialWriteFailed, revisions, err := engine.unresolved(ctx)
	if err != nil {
		return ReconcileReport{}, err
	}
	report := ReconcileReport{Backend: engine.config.BackendID, DryRun: dryRun,
		PendingCanonicalHeads: initialHead, PendingWriteOutbox: initialWrite,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
	if dryRun {
		report.Remaining = initialHead + initialHeadFailed + initialWrite + initialWriteFailed
		report.Failures = initialHeadFailed + initialWriteFailed
		switch {
		case report.Failures > 0:
			report.ResultCode, report.ReasonCode = "failed", "reconcile_failures"
		case report.Remaining > 0:
			report.ResultCode, report.ReasonCode = "inventory", "pending_work"
		default:
			report.ResultCode, report.ReasonCode = "clean", "no_pending_work"
		}
		return report, nil
	}
	var projectionErr error
	for _, revision := range revisions {
		if _, err := engine.writer.CompleteProjection(ctx, revision, engine.projector); err != nil {
			projectionErr = errors.Join(projectionErr, err)
		}
	}
	finalHead, finalHeadFailed, finalWrite, finalWriteFailed, _, err := engine.unresolved(ctx)
	if err != nil {
		return ReconcileReport{}, err
	}
	report.Repaired = initialHead + initialWrite - finalHead - finalWrite
	report.Remaining = finalHead + finalHeadFailed + finalWrite + finalWriteFailed
	report.Failures = report.Remaining
	if projectionErr != nil && report.Failures == 0 {
		report.Failures = 1
	}
	switch {
	case report.Failures > 0:
		report.ResultCode, report.ReasonCode = "failed", "reconcile_failures"
	case report.Repaired > 0:
		report.ResultCode, report.ReasonCode = "repaired", "reconcile_complete"
	default:
		report.ResultCode, report.ReasonCode = "clean", "no_pending_work"
	}
	return report, nil
}
