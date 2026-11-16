package engine

import (
	"context"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type CompensatePromotionRequest struct {
	MemoryID         string `json:"memory_id"`
	ExpectedRevision string `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
	Reason           string `json:"reason"`
}

type CompensationReceipt struct {
	SourceRecordID string `json:"source_record_id"`
	RecordID       string `json:"record_id"`
	RevisionID     string `json:"revision_id"`
	Outcome        string `json:"outcome"`
}

// CompensatePromotion retracts only a pinned candidate or the exact direct
// publication owned by the requested operation. An approved correction first
// restores its pinned target before the proposal is hidden.
func (engine *Engine) CompensatePromotion(ctx context.Context,
	request CompensatePromotionRequest) (CompensationReceipt, error) {
	if !exactSubjectIdentity(request.MemoryID) || !exactSubjectIdentity(request.ExpectedRevision) ||
		request.OperationID == "" || request.OperationID != strings.TrimSpace(request.OperationID) ||
		strings.TrimSpace(request.Reason) == "" {
		return CompensationReceipt{}, errors.New("compensation requires exact source, revision, operation and reason")
	}
	lock, err := engine.acquireEraseWorkspaceLock(ctx)
	if err != nil {
		return CompensationReceipt{}, err
	}
	defer lock.Unlock()
	source, err := engine.ReadStored(ctx, request.MemoryID)
	if err != nil {
		return CompensationReceipt{}, err
	}
	if source == nil || source.ProfileID != engine.config.ProfileID ||
		source.RevisionID != request.ExpectedRevision {
		return CompensationReceipt{}, errors.New("compensation source does not match its pinned revision")
	}
	if err := engine.assertChainNotErased(ctx, source.CanonicalKey); err != nil {
		return CompensationReceipt{}, err
	}
	current, err := engine.compensationCurrent(ctx, source.CanonicalKey)
	if err != nil {
		return CompensationReceipt{}, err
	}
	if targetID, _ := source.Metadata["correction_target_id"].(string); targetID != "" {
		preserved, err := engine.compensateCorrectionLocked(ctx, *source, targetID,
			request.OperationID, request.Reason)
		if err != nil || preserved != nil {
			if preserved != nil {
				return *preserved, err
			}
			return CompensationReceipt{}, err
		}
	}
	result := CompensationReceipt{SourceRecordID: source.BlockID,
		RecordID: current.BlockID, RevisionID: current.RevisionID}
	if current.Status == "retracted" {
		result.Outcome = "already_retracted"
		return result, nil
	}
	ownsCandidate := current.BlockID == source.BlockID && current.Status == "candidate"
	ownsPublication := current.Status == "accepted" && current.RevisionOf == source.BlockID &&
		current.Metadata["promotion_operation_id"] == request.OperationID
	if !ownsCandidate && !ownsPublication {
		result.Outcome = "superseded"
		return result, nil
	}
	retracted, err := engine.transitionInactiveLocked(ctx, ForgetRequest{
		MemoryID: current.BlockID, ExpectedRevision: current.RevisionID,
		Reason: request.Reason}, "retract")
	if err != nil {
		return CompensationReceipt{}, err
	}
	if retracted["ok"] != true || retracted["saved"] != true {
		return CompensationReceipt{}, errors.New("compensation retraction did not commit")
	}
	result.RecordID, _ = retracted["block_id"].(string)
	result.RevisionID, _ = retracted["revision_id"].(string)
	result.Outcome = "retracted"
	return result, nil
}

func (engine *Engine) compensationCurrent(ctx context.Context,
	canonicalKey string) (*memory.MemoryRecord, error) {
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	var current *memory.MemoryRecord
	for _, row := range rows {
		if !row.Current || row.Status != "committed" ||
			row.Backend != engine.recordNativeStore(row.Record) ||
			row.Record.CanonicalKey != canonicalKey {
			continue
		}
		if current != nil {
			return nil, errors.New("compensation canonical chain has more than one current head")
		}
		copy := row.Record
		current = &copy
	}
	if current == nil {
		return nil, errors.New("compensation canonical current head is unavailable")
	}
	return current, nil
}

func (engine *Engine) compensateCorrectionLocked(ctx context.Context,
	source memory.MemoryRecord, targetID, operationID, reason string) (*CompensationReceipt, error) {
	target, err := engine.ReadStored(ctx, targetID)
	if err != nil {
		return nil, err
	}
	if target == nil || target.ProfileID != engine.config.ProfileID ||
		target.RevisionID != source.Metadata["correction_target_revision_id"] {
		return nil, errors.New("compensation correction target differs from its pinned revision")
	}
	current, err := engine.compensationCurrent(ctx, target.CanonicalKey)
	if err != nil {
		return nil, err
	}
	if current.BlockID == target.BlockID && target.Status == "accepted" {
		return nil, nil
	}
	identity, err := engine.index.MutationIdentity(ctx, current.BlockID)
	if err != nil {
		return nil, err
	}
	restored := identity != nil && identity.Status == "committed" &&
		identity.Action == "rollback" && identity.RollbackTargetID == target.BlockID &&
		current.Status == "accepted"
	published := current
	if restored {
		published, err = engine.ReadStored(ctx, identity.PredecessorID)
		if err != nil || published == nil {
			return nil, errors.New("compensation restored predecessor is unavailable")
		}
	}
	publishedIdentity, err := engine.index.MutationIdentity(ctx, published.BlockID)
	if err != nil {
		return nil, err
	}
	ownsPublication := published.Status == "accepted" && published.RevisionOf == target.BlockID &&
		published.Metadata["promotion_operation_id"] == operationID &&
		published.Metadata["promotion_source_id"] == source.BlockID &&
		published.Metadata["promotion_source_revision_id"] == source.RevisionID &&
		published.Metadata["approved_correction_candidate_id"] == source.BlockID &&
		publishedIdentity != nil && publishedIdentity.Status == "committed" &&
		publishedIdentity.Action == "correction" &&
		publishedIdentity.PredecessorID == target.BlockID
	if !ownsPublication {
		return &CompensationReceipt{SourceRecordID: source.BlockID,
			RecordID: current.BlockID, RevisionID: current.RevisionID,
			Outcome: "superseded"}, nil
	}
	if !restored {
		result, err := engine.rollbackLocked(ctx, RollbackRequest{MemoryID: target.BlockID,
			RevisionID: target.RevisionID, Reason: reason})
		if err != nil || result["ok"] != true || result["saved"] != true {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("compensation correction target was not restored")
		}
	}
	return nil, nil
}
