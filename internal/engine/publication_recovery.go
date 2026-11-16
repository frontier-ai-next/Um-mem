package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// RecoverPublication resolves a request-bound committed publication before
// rechecking the source head. An old Python copy may need its independently
// committed provenance edge repaired after a crash.
func (engine *Engine) RecoverPublication(ctx context.Context, receiptID, requestHash,
	sourceID, sourceRevisionID string) (*memory.PublicationCommitReceipt, error) {
	for _, identity := range []string{receiptID, requestHash, sourceID, sourceRevisionID} {
		if identity == "" || identity != strings.TrimSpace(identity) {
			return nil, errors.New("publication recovery requires exact request and source identity")
		}
	}
	receipt, err := engine.index.PublicationReceipt(ctx, receiptID)
	if err != nil && !errors.Is(err, store.ErrPublicationProvenancePending) {
		return nil, err
	}
	if receipt == nil {
		return nil, nil
	}
	if receipt.RequestHash != requestHash || receipt.SourceID != sourceID ||
		receipt.SourceRevisionID != sourceRevisionID ||
		receipt.WorkspaceID != engine.config.WorkspaceID ||
		receipt.ProfileID != engine.config.ProfileID {
		return nil, errors.New("canonical publication receipt conflicts with request")
	}
	if errors.Is(err, store.ErrPublicationProvenancePending) {
		if err := engine.writer.RecoverDetachedPublicationCopy(ctx, *receipt); err != nil {
			return nil, err
		}
		receipt, err = engine.index.PublicationReceipt(ctx, receiptID)
		if err != nil || receipt == nil {
			return nil, errors.New("publication provenance repair did not produce a committed receipt")
		}
	}
	return receipt, nil
}
