package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type ForgetRequest struct {
	MemoryID         string               `json:"memory_id"`
	Reason           string               `json:"reason,omitempty"`
	EvidenceRefs     []memory.EvidenceRef `json:"evidence_refs,omitempty"`
	ExpectedRevision string               `json:"expected_revision,omitempty"`
}

// DeprecateRequest carries the same identity and evidence boundary as a
// retraction, while keeping its distinct lifecycle action public.
type DeprecateRequest = ForgetRequest

func mergeEvidenceRefs(existing, added []memory.EvidenceRef) ([]memory.EvidenceRef, error) {
	merged := make([]memory.EvidenceRef, 0, len(existing)+len(added))
	seen := map[string]bool{}
	for _, ref := range append(append([]memory.EvidenceRef{}, existing...), added...) {
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(ref)
		if err != nil {
			return nil, err
		}
		key := string(encoded)
		if !seen[key] {
			merged = append(merged, ref)
			seen[key] = true
		}
	}
	return merged, nil
}

// Forget writes a retracted canonical successor. The current head remains
// visible until the selected native backend verifies the staged revision.
func (engine *Engine) Forget(ctx context.Context, request ForgetRequest) (map[string]any, error) {
	return engine.transitionInactive(ctx, request, "retract")
}

// Deprecate archives a canonical memory with a reason while retaining its
// history and making the new head unavailable for ordinary delivery.
func (engine *Engine) Deprecate(ctx context.Context, request DeprecateRequest) (map[string]any, error) {
	if strings.TrimSpace(request.Reason) == "" {
		return nil, errors.New("deprecate requires a reason")
	}
	return engine.transitionInactive(ctx, request, "deprecate")
}

func (engine *Engine) transitionInactive(ctx context.Context, request ForgetRequest,
	action string) (map[string]any, error) {
	lock, err := engine.acquireEraseWorkspaceLock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	return engine.transitionInactiveLocked(ctx, request, action)
}

// transitionInactiveLocked also serves the erase operation while it holds the
// workspace lock for the whole native purge and certificate transaction.
func (engine *Engine) transitionInactiveLocked(ctx context.Context, request ForgetRequest,
	action string) (map[string]any, error) {
	status, name, alreadyKey := "retracted", "forget", "already_retracted"
	if action == "deprecate" {
		status, name, alreadyKey = "deprecated", "deprecate", "already_deprecated"
	} else if action != "retract" {
		return nil, errors.New("unsupported inactive lifecycle action")
	}
	id := strings.TrimSpace(request.MemoryID)
	if id == "" || id != request.MemoryID {
		return nil, errors.New(name + " requires an exact memory_id")
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		reason = "user_requested"
	}
	if request.ExpectedRevision != "" && request.ExpectedRevision != strings.TrimSpace(request.ExpectedRevision) {
		return nil, errors.New("expected_revision must be a nonempty revision identity")
	}
	root, err := engine.ReadStored(ctx, id)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return map[string]any{"ok": false, "saved": false, "error": "not_found", "block_id": id}, nil
	}
	if root.CanonicalKey == "" || root.BlockMembership == nil {
		return nil, errors.New(name + " requires a canonical memory with block membership")
	}
	if err := engine.assertChainNotErased(ctx, root.CanonicalKey); err != nil {
		return nil, err
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	var current *memory.MemoryRecord
	for _, row := range rows {
		if row.Current && row.Status == "committed" && row.Backend == engine.recordNativeStore(row.Record) &&
			row.Record.CanonicalKey == root.CanonicalKey {
			if current != nil {
				return nil, errors.New("canonical key has more than one current head")
			}
			copy := row.Record
			current = &copy
		}
	}
	if current == nil || current.BlockMembership == nil {
		return nil, errors.New("canonical current head is unavailable")
	}
	if request.ExpectedRevision != "" && current.Status != status &&
		(current.BlockID != id || current.RevisionID != request.ExpectedRevision) {
		return map[string]any{"ok": false, "saved": false,
			"error": "expected_revision_conflict", "block_id": id}, nil
	}
	for _, row := range rows {
		if row.Status != "pending" || row.Record.CanonicalKey != current.CanonicalKey ||
			row.Record.RevisionOf != current.BlockID {
			continue
		}
		if row.Record.Status != status || row.Record.Metadata["status_reason"] != reason {
			return nil, errors.New("canonical mutation is pending with a different identity")
		}
		refs, err := mergeEvidenceRefs(current.EvidenceRefs, request.EvidenceRefs)
		if err != nil {
			return nil, err
		}
		encodedPending, _ := json.Marshal(row.Record.EvidenceRefs)
		encodedRequested, _ := json.Marshal(refs)
		if string(encodedPending) != string(encodedRequested) {
			return nil, errors.New("canonical lifecycle mutation is pending with different evidence")
		}
		return engine.completeInactive(ctx, row.Record, current.BlockID, row.Revision)
	}
	if current.Status == status && request.ExpectedRevision != "" &&
		root.RevisionID == request.ExpectedRevision && current.RevisionOf == id {
		return map[string]any{"ok": true, "saved": true, alreadyKey: true,
			"block_id": current.BlockID, "backend": engine.config.BackendID}, nil
	}
	if request.ExpectedRevision != "" && (current.BlockID != id || current.RevisionID != request.ExpectedRevision) {
		return map[string]any{"ok": false, "saved": false,
			"error": "expected_revision_conflict", "block_id": id}, nil
	}
	if current.Status == status {
		refs, err := mergeEvidenceRefs(current.EvidenceRefs, request.EvidenceRefs)
		if err != nil {
			return nil, err
		}
		stored, _ := json.Marshal(current.EvidenceRefs)
		requested, _ := json.Marshal(refs)
		if current.BlockID == id && request.ExpectedRevision == "" &&
			current.Metadata["status_reason"] == reason && string(stored) == string(requested) {
			return map[string]any{"ok": true, "saved": true, alreadyKey: true,
				"block_id": current.BlockID, "backend": engine.config.BackendID}, nil
		}
		return nil, errors.New("current canonical memory is already " + status)
	}
	if action == "deprecate" && current.Status == "retracted" {
		return nil, errors.New("retracted canonical memory cannot be deprecated")
	}
	refs, err := mergeEvidenceRefs(current.EvidenceRefs, request.EvidenceRefs)
	if err != nil {
		return nil, err
	}
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	successor := *current
	successor.Status, successor.Retention, successor.DeliveryPolicy = status, "archive", "hidden"
	successor.EvidenceRefs = refs
	successor.Metadata = make(map[string]any, len(current.Metadata)+1)
	for key, value := range current.Metadata {
		successor.Metadata[key] = value
	}
	successor.Metadata["status_reason"] = reason
	successor.RecordedAt, successor.ValidFrom, successor.ValidTo = stamp, stamp, ""
	successor.RevisionOf = current.BlockID
	successor.SupersedesIDs = []string{current.BlockID}
	successor.RevisionID, err = memory.RevisionIDForRecord(current.CanonicalKey, successor, action, current.RevisionID)
	if err != nil {
		return nil, err
	}
	successor.BlockID = memory.BlockIDForRevisionID(successor.RevisionID)
	membership := *current.BlockMembership
	membership.RecordID, membership.RecordRevisionID = successor.BlockID, successor.RevisionID
	membership.ValidFrom, membership.ValidTo = stamp, ""
	membership.MembershipRevisionID = membership.RevisionHash()
	successor.BlockMembership = &membership
	if err := successor.ValidateForStorage(); err != nil {
		return nil, err
	}
	staged, err := engine.prepareSuccessorWrite(ctx, successor, action, "")
	if err != nil {
		return nil, err
	}
	return engine.completeInactive(ctx, successor, current.BlockID, staged.CommitRevision)
}

func (engine *Engine) completeInactive(ctx context.Context, successor memory.MemoryRecord,
	predecessorID string, commitRevision int64) (map[string]any, error) {
	receipt, err := engine.writer.CompleteProjection(ctx, commitRevision, engine.projector)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "saved": true, "block_id": successor.BlockID,
		"revision_id": successor.RevisionID, "revision_of": predecessorID,
		"backend": engine.config.BackendID, "commit_revision": receipt.CommitRevision,
		"projection_status":  "committed",
		"logical_index_sync": map[string]any{"status": "not_applicable"}}, nil
}
