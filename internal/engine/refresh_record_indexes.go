package engine

import (
	"context"
	"errors"
)

// RefreshRecordIndexes verifies the atomic write's derived rows. Normal Go
// writes need no second full rebuild; a missing row triggers durable recovery.
func (engine *Engine) RefreshRecordIndexes(ctx context.Context, recordID string) error {
	complete, err := engine.index.RecordDerivedIndexesComplete(ctx, recordID)
	if err != nil || complete {
		return err
	}
	if _, err := engine.RebuildIndexes(ctx); err != nil {
		return err
	}
	complete, err = engine.index.RecordDerivedIndexesComplete(ctx, recordID)
	if err != nil {
		return err
	}
	if !complete {
		return errors.New("memory index refresh left derived rows incomplete")
	}
	return nil
}
