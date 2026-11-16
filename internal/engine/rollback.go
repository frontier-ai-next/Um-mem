package engine

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/erasure"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type RollbackRequest struct {
	MemoryID     string               `json:"memory_id"`
	RevisionID   string               `json:"revision_id"`
	Reason       string               `json:"reason"`
	EvidenceRefs []memory.EvidenceRef `json:"evidence_refs,omitempty"`
}

// Rollback appends a new revision copied from a committed historical revision.
// The historical record and the current predecessor remain immutable.
func (engine *Engine) Rollback(ctx context.Context, request RollbackRequest) (map[string]any, error) {
	if request.MemoryID == "" || request.MemoryID != strings.TrimSpace(request.MemoryID) ||
		request.RevisionID == "" || request.RevisionID != strings.TrimSpace(request.RevisionID) ||
		request.Reason != strings.TrimSpace(request.Reason) {
		return nil, errors.New("rollback requires exact memory_id, revision_id, and reason")
	}
	lock, err := engine.acquireEraseWorkspaceLock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	return engine.rollbackLocked(ctx, request)
}

func (engine *Engine) rollbackLocked(ctx context.Context, request RollbackRequest) (map[string]any, error) {
	root, err := engine.ReadStored(ctx, request.MemoryID)
	if err != nil {
		return nil, err
	}
	if root == nil || root.CanonicalKey == "" {
		return nil, errors.New("rollback source was not found in this profile")
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	journal, err := erasure.OpenJournal(engine.config.RepoRoot)
	if err != nil {
		return nil, err
	}
	defer journal.Close()
	targetSHAs := make([]string, 0)
	for _, row := range rows {
		if row.Record.CanonicalKey == root.CanonicalKey && row.Status == "committed" &&
			row.Record.WorkspaceID == engine.config.WorkspaceID {
			targetSHAs = append(targetSHAs, memory.EraseSHA256(row.Record.BlockID))
		}
	}
	erased, err := journal.HasCompletedTarget(ctx,
		memory.EraseSHA256(engine.config.WorkspaceID), targetSHAs)
	if err != nil {
		return nil, err
	}
	if erased {
		return nil, errors.New("rollback is blocked for an erased canonical chain")
	}
	var current, target *memory.MemoryRecord
	for _, row := range rows {
		if row.Record.CanonicalKey != root.CanonicalKey || row.Backend != engine.recordNativeStore(row.Record) ||
			row.Status != "committed" {
			continue
		}
		if row.Current {
			if current != nil {
				return nil, errors.New("canonical key has more than one current head")
			}
			copy := row.Record
			current = &copy
		}
		if row.Record.RevisionID == request.RevisionID {
			if target != nil {
				return nil, errors.New("rollback revision identity is ambiguous")
			}
			copy := row.Record
			target = &copy
		}
	}
	if current == nil || current.BlockMembership == nil {
		return nil, errors.New("canonical current head is unavailable")
	}
	if target == nil || target.BlockMembership == nil {
		return nil, errors.New("rollback revision was not found in the canonical history")
	}
	if target.Scope != current.Scope ||
		target.BlockMembership.PrimaryBlockID != current.BlockMembership.PrimaryBlockID {
		return nil, errors.New("rollback target has a different logical scope or primary block")
	}
	refs, err := mergeEvidenceRefs(target.EvidenceRefs, request.EvidenceRefs)
	if err != nil {
		return nil, err
	}
	wantReason := target.Metadata["status_reason"]
	if request.Reason != "" {
		wantReason = request.Reason
	}
	for _, row := range rows {
		if row.Status != "pending" || row.Record.CanonicalKey != current.CanonicalKey ||
			row.Record.RevisionOf != current.BlockID {
			continue
		}
		identity, err := engine.index.MutationIdentity(ctx, row.Record.BlockID)
		if err != nil {
			return nil, err
		}
		if identity == nil || identity.Action != "rollback" ||
			identity.RollbackTargetID != target.BlockID ||
			!rollbackMatches(row.Record, *target, refs, wantReason) {
			return nil, errors.New("canonical mutation is pending with a different identity")
		}
		return engine.completeRollback(ctx, row.Record, current.BlockID,
			target.BlockID, row.Revision, true)
	}
	identity, err := engine.index.MutationIdentity(ctx, current.BlockID)
	if err != nil {
		return nil, err
	}
	if identity != nil && identity.Status == "committed" && identity.Action == "rollback" &&
		identity.RollbackTargetID == target.BlockID &&
		rollbackMatches(*current, *target, refs, wantReason) {
		return map[string]any{"ok": true, "saved": true, "block_id": current.BlockID,
			"revision_id": current.RevisionID, "revision_of": current.RevisionOf,
			"rollback_target_id": target.BlockID, "backend": engine.config.BackendID,
			"replayed": true}, nil
	}
	successor := *target
	successor.Metadata = make(map[string]any, len(target.Metadata)+1)
	for key, value := range target.Metadata {
		successor.Metadata[key] = value
	}
	if request.Reason != "" {
		successor.Metadata["status_reason"] = request.Reason
	}
	successor.EvidenceRefs = refs
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	successor.RecordedAt, successor.ValidFrom, successor.ValidTo = stamp, stamp, ""
	successor.RevisionOf = current.BlockID
	successor.SupersedesIDs = []string{current.BlockID}
	successor.BlockMembership = &memory.RecordBlockMembership{
		PrimaryBlockID:    target.BlockMembership.PrimaryBlockID,
		SecondaryBlockIDs: append([]string{}, target.BlockMembership.SecondaryBlockIDs...)}
	successor.RevisionID, err = memory.RevisionIDForRecord(current.CanonicalKey, successor,
		"rollback", current.RevisionID)
	if err != nil {
		return nil, err
	}
	successor.BlockID = memory.BlockIDForRevisionID(successor.RevisionID)
	selectedID := successor.BlockMembership.PrimaryBlockID
	successor.BlockMembership = nil
	if err := engine.BindSuccessorRecord(ctx, &successor, selectedID); err != nil {
		return nil, err
	}
	successor.BlockMembership.SecondaryBlockIDs = append([]string{},
		target.BlockMembership.SecondaryBlockIDs...)
	successor.BlockMembership.MembershipRevisionID = successor.BlockMembership.RevisionHash()
	if err := successor.ValidateForStorage(); err != nil {
		return nil, err
	}
	staged, err := engine.prepareSuccessorWrite(ctx, successor, "rollback", target.BlockID)
	if err != nil {
		return nil, err
	}
	return engine.completeRollback(ctx, successor, current.BlockID,
		target.BlockID, staged.CommitRevision, false)
}

func rollbackMatches(record, target memory.MemoryRecord, refs []memory.EvidenceRef,
	reason any) bool {
	left, _ := json.Marshal(record.EvidenceRefs)
	right, _ := json.Marshal(refs)
	return record.Content == target.Content && record.Title == target.Title &&
		record.Status == target.Status && record.Authority == target.Authority &&
		record.Retention == target.Retention && record.DeliveryPolicy == target.DeliveryPolicy &&
		reflect.DeepEqual(record.Metadata["status_reason"], reason) && string(left) == string(right)
}

func (engine *Engine) completeRollback(ctx context.Context, successor memory.MemoryRecord,
	predecessorID, targetID string, commitRevision int64, replayed bool) (map[string]any, error) {
	receipt, err := engine.writer.CompleteProjection(ctx, commitRevision, engine.projector)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "saved": true, "block_id": successor.BlockID,
		"revision_id": successor.RevisionID, "revision_of": predecessorID,
		"rollback_target_id": targetID, "backend": engine.config.BackendID,
		"commit_revision": receipt.CommitRevision, "projection_status": "committed",
		"replayed": replayed}, nil
}
