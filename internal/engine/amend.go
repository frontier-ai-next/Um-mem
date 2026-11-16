package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/receipts"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type AmendRequest struct {
	MemoryID         string               `json:"memory_id"`
	ExpectedRevision string               `json:"expected_revision"`
	Reason           string               `json:"reason"`
	Content          *string              `json:"content,omitempty"`
	Title            *string              `json:"title,omitempty"`
	Tags             []string             `json:"tags,omitempty"`
	MetadataPatch    map[string]any       `json:"metadata_patch,omitempty"`
	Retention        *string              `json:"retention,omitempty"`
	ExpiresAt        *string              `json:"expires_at,omitempty"`
	EvidenceRefs     []memory.EvidenceRef `json:"evidence_refs,omitempty"`
	RequestedBlockID string               `json:"requested_block_id,omitempty"`
	OperationID      string               `json:"operation_id,omitempty"`
}

var amendForbiddenMetadata = []string{"authority", "delivery_policy", "promotion_decision", "scope",
	"status", "trust_level", "verification_methods", "verification_status", "workspace_id"}

func amendRetentionAllowed(current, selected string) bool {
	switch current {
	case "ephemeral":
		return selected == "ephemeral"
	case "session":
		return selected == "ephemeral" || selected == "session"
	case "durable":
		return selected == "ephemeral" || selected == "session" || selected == "durable"
	case "archive":
		return selected == "archive"
	}
	return false
}

func amendImportance(value any) bool {
	var number float64
	switch actual := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(actual.String(), 64)
		if err != nil {
			return false
		}
		number = parsed
	case float64:
		number = actual
	case float32:
		number = float64(actual)
	case int:
		number = float64(actual)
	case int64:
		number = float64(actual)
	default:
		return false
	}
	return number >= 0 && number <= 1
}

func (engine *Engine) Amend(ctx context.Context, request AmendRequest) (map[string]any, error) {
	return engine.AmendFor(ctx, request, "go_cli", 2, nil)
}

func (engine *Engine) AmendFor(ctx context.Context, request AmendRequest, callerRole string,
	protocolVersion int, scopeLineage map[string]string) (map[string]any, error) {
	lock, err := engine.acquireEraseWorkspaceLock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	return engine.amendForLocked(ctx, request, callerRole, protocolVersion, scopeLineage)
}

// amendForLocked is used by correction, which holds the workspace erase lock
// across its target check and the resulting mutation.
func (engine *Engine) amendForLocked(ctx context.Context, request AmendRequest, callerRole string,
	protocolVersion int, scopeLineage map[string]string) (map[string]any, error) {
	if callerRole == "" || protocolVersion < 1 {
		return nil, errors.New("amend requires authenticated caller identity")
	}
	if request.OperationID == "" {
		return engine.amendOnce(ctx, request, "", false)
	}
	if request.OperationID != strings.TrimSpace(request.OperationID) || len(request.OperationID) > 256 {
		return nil, errors.New("amend operation_id must be a bounded opaque string")
	}
	identity := receipts.Identity{WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, Backend: engine.config.BackendID,
		CallerRole: callerRole, ProtocolVersion: protocolVersion}
	fingerprintRequest := request
	fingerprintRequest.OperationID = ""
	arguments := map[string]any{"tool": "amend", "request": fingerprintRequest,
		"scope_lineage": scopeLineage}
	claimed, err := receipts.Claim(ctx, engine.config.RepoRoot, identity, request.OperationID, arguments)
	if err != nil {
		return nil, err
	}
	if claimed.Status == "committed" {
		inner, ok := claimed.Response["response"].(map[string]any)
		if claimed.Response["receipt_kind"] != "mutation_response" ||
			claimed.Response["mutation_tool"] != "amend" || !ok || inner["ok"] != true {
			return nil, errors.New("amend operation receipt is invalid")
		}
		id, ok := inner["new_id"].(string)
		if !ok || id == "" {
			return nil, errors.New("amend operation receipt has no canonical record")
		}
		record, err := engine.ReadStored(ctx, id)
		if err != nil || record == nil || record.RevisionID != inner["new_revision"] {
			return nil, errors.New("committed amendment revision is unavailable")
		}
		if err := engine.assertChainNotErased(ctx, record.CanonicalKey); err != nil {
			return nil, err
		}
		copy := make(map[string]any, len(inner)+1)
		for key, value := range inner {
			copy[key] = value
		}
		copy["replayed"] = true
		return copy, nil
	}
	result, err := engine.amendOnce(ctx, request, claimed.CreatedAt, claimed.Status == "pending")
	if err != nil {
		checksum := sha256.Sum256([]byte(err.Error()))
		failure := map[string]any{"ok": false, "error_type": "GoError",
			"reason_code": "amend_write_failed", "error_sha256": hex.EncodeToString(checksum[:])}
		if completeErr := receipts.Complete(ctx, engine.config.RepoRoot, identity,
			request.OperationID, arguments, failure, "failed"); completeErr != nil {
			return nil, errors.Join(err, completeErr)
		}
		return nil, err
	}
	wrapper := map[string]any{"receipt_kind": "mutation_response", "mutation_tool": "amend",
		"response": result}
	if err := receipts.Complete(ctx, engine.config.RepoRoot, identity,
		request.OperationID, arguments, wrapper, "committed"); err != nil {
		return nil, err
	}
	return result, nil
}

func (engine *Engine) amendOnce(ctx context.Context, request AmendRequest,
	claimedAt string, receiptPending bool) (map[string]any, error) {
	id, expected, reason := strings.TrimSpace(request.MemoryID),
		strings.TrimSpace(request.ExpectedRevision), strings.TrimSpace(request.Reason)
	if id == "" || id != request.MemoryID || expected == "" || expected != request.ExpectedRevision || reason == "" {
		return nil, errors.New("amend requires exact memory_id, expected_revision, and reason")
	}
	root, err := engine.ReadStored(ctx, id)
	if err != nil {
		return nil, err
	}
	if root == nil || root.CanonicalKey == "" {
		return nil, errors.New("canonical memory was not found in this profile")
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
	if err := engine.assertChainNotErased(ctx, current.CanonicalKey); err != nil {
		return nil, err
	}
	var committedSuccessor *memory.MemoryRecord
	var committedRevision int64
	if current.RevisionID != expected {
		if receiptPending {
			for _, row := range rows {
				if row.Status == "committed" && row.Record.RevisionID == expected &&
					row.Record.CanonicalKey == current.CanonicalKey &&
					current.RevisionOf == row.Record.BlockID {
					previous, head := row.Record, *current
					current, committedSuccessor, committedRevision = &previous, &head, row.Revision
					for _, possibleHead := range rows {
						if possibleHead.Record.BlockID == head.BlockID {
							committedRevision = possibleHead.Revision
						}
					}
					break
				}
			}
		}
		if current.RevisionID != expected {
			return nil, fmt.Errorf("amend expected_revision does not match the current revision %s", current.RevisionID)
		}
	}
	if current.Status == "accepted" || current.Authority == "directive" ||
		slices.Contains([]string{"workspace_verified", "external_verified", "supervisor_verified",
			"human_approved", "system_curated"}, current.TrustLevel) ||
		current.VerificationStatus == "verified" {
		return nil, errors.New("trusted memory amend requires the existing correction proposal and tribunal path")
	}
	if current.Metadata["memory_mode"] == "lossless_atomic" && request.Content != nil {
		return nil, errors.New("lossless source content requires a validated correction path")
	}
	metadata := make(map[string]any, len(current.Metadata)+len(request.MetadataPatch)+1)
	for key, value := range current.Metadata {
		metadata[key] = value
	}
	for key, value := range request.MetadataPatch {
		if losslessProofMetadata[key] {
			return nil, errors.New("amend cannot forge lossless source proof: " + key)
		}
		if slices.Contains(amendForbiddenMetadata, key) {
			return nil, errors.New("amend metadata patch cannot change governance fields: " + key)
		}
		if key == "importance" && !amendImportance(value) {
			return nil, errors.New("amend importance must be between 0 and 1")
		}
		if value == nil {
			delete(metadata, key)
		} else {
			metadata[key] = value
		}
	}
	metadata["status_reason"] = reason
	if _, err := json.Marshal(metadata); err != nil {
		return nil, err
	}
	selectedID := strings.TrimSpace(request.RequestedBlockID)
	if selectedID == "" {
		selectedID = current.BlockMembership.PrimaryBlockID
	}
	if committedSuccessor == nil {
		if err := engine.RefreshCatalog(); err != nil {
			return nil, err
		}
		snapshot := engine.current.Load()
		block, exists := snapshot.byID[selectedID]
		if !exists || block.IndexOnly || !block.AgentWritable || block.Authority != "advisory" {
			return nil, errors.New("amend requires an active agent-writable advisory leaf block")
		}
		for _, possibleChild := range snapshot.blocks {
			if possibleChild.ParentID == selectedID {
				return nil, errors.New("amend requires an active agent-writable advisory leaf block")
			}
		}
		if block.Scope != current.Scope {
			return nil, errors.New("amend cannot move a record across scope dimensions")
		}
	} else if committedSuccessor.BlockMembership == nil ||
		committedSuccessor.BlockMembership.PrimaryBlockID != selectedID ||
		committedSuccessor.Scope != current.Scope {
		return nil, errors.New("amend receipt recovery differs from the committed correction")
	}
	retention := current.Retention
	if request.Retention != nil {
		retention = strings.TrimSpace(*request.Retention)
	}
	if !amendRetentionAllowed(current.Retention, retention) {
		return nil, errors.New("amend cannot increase or bypass the current retention policy")
	}
	successor := *current
	if request.Content != nil {
		successor.Content = strings.TrimSpace(*request.Content)
	}
	if request.Title != nil {
		successor.Title = strings.TrimSpace(*request.Title)
	}
	if successor.Content == "" || successor.Title == "" {
		return nil, errors.New("amend content and title cannot be empty")
	}
	if request.Tags != nil {
		successor.Tags = append([]string{}, request.Tags...)
	}
	if request.ExpiresAt != nil {
		successor.ExpiresAt = strings.TrimSpace(*request.ExpiresAt)
	}
	successor.Retention, successor.Metadata = retention, metadata
	successor.EvidenceRefs, err = mergeEvidenceRefs(current.EvidenceRefs, request.EvidenceRefs)
	if err != nil {
		return nil, err
	}
	successor.DuplicateClusterID = ""
	successor.DuplicateClusterID, err = memory.DuplicateClusterID(successor)
	if err != nil {
		return nil, err
	}
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	if claimedAt != "" {
		claimedTime, err := time.Parse(time.RFC3339Nano, claimedAt)
		if err != nil {
			return nil, errors.New("amend operation claim time is invalid")
		}
		stamp = claimedTime.UTC().Format("2006-01-02T15:04:05.000000Z")
	}
	successor.RecordedAt, successor.ValidFrom, successor.ValidTo = stamp, stamp, ""
	successor.RevisionOf = current.BlockID
	successor.SupersedesIDs = []string{current.BlockID}
	successor.BlockMembership = &memory.RecordBlockMembership{PrimaryBlockID: selectedID,
		SecondaryBlockIDs: []string{}}
	successor.RevisionID, err = memory.RevisionIDForRecord(current.CanonicalKey, successor, "correction", current.RevisionID)
	if err != nil {
		return nil, err
	}
	successor.BlockID = memory.BlockIDForRevisionID(successor.RevisionID)
	if committedSuccessor != nil {
		if successor.BlockID != committedSuccessor.BlockID ||
			successor.RevisionID != committedSuccessor.RevisionID || committedRevision < 1 {
			return nil, errors.New("amend receipt recovery differs from the committed correction")
		}
		return engine.completeAmend(ctx, *committedSuccessor, *current, selectedID, committedRevision, true)
	}
	successor.BlockMembership = nil
	if err := engine.BindSuccessorRecord(ctx, &successor, selectedID); err != nil {
		return nil, err
	}
	if err := successor.ValidateForStorage(); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Record.BlockID == successor.BlockID && row.Status == "pending" &&
			row.Record.RevisionOf == current.BlockID {
			return engine.completeAmend(ctx, row.Record, *current, selectedID, row.Revision, true)
		}
	}
	staged, err := engine.prepareSuccessorWrite(ctx, successor, "correction", "")
	if err != nil {
		return nil, err
	}
	return engine.completeAmend(ctx, successor, *current, selectedID, staged.CommitRevision, false)
}

func (engine *Engine) completeAmend(ctx context.Context, successor, predecessor memory.MemoryRecord,
	selectedID string, commitRevision int64, replayed bool) (map[string]any, error) {
	receipt, err := engine.writer.CompleteProjection(ctx, commitRevision, engine.projector)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "saved": true, "backend": engine.config.BackendID,
		"workspace_id": engine.config.WorkspaceID, "old_id": predecessor.BlockID,
		"new_id": successor.BlockID, "memory_id": successor.BlockID, "record_id": successor.BlockID,
		"old_revision": predecessor.RevisionID, "new_revision": successor.RevisionID,
		"requested_block_id": selectedID, "commit_revision": receipt.CommitRevision,
		"projection_status": "committed", "durable_status": "committed", "replayed": replayed,
		"lifecycle": map[string]any{"old_status": "superseded", "new_status": successor.Status},
		"active_frame_invalidation": map[string]any{"mode": "canonical_revision",
			"superseded_record_ids": []string{predecessor.BlockID}}}, nil
}
