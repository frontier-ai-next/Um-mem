package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/receipts"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// CorrectRequest is the remember correction contract. The selected logical
// block must contain the current canonical head; trusted heads yield advisory
// proposals rather than being changed by an agent.
type CorrectRequest struct {
	MemoryID         string               `json:"memory_id"`
	Content          string               `json:"content"`
	Title            string               `json:"title,omitempty"`
	Reason           string               `json:"reason,omitempty"`
	ExpectedRevision string               `json:"expected_revision,omitempty"`
	RequestedBlockID string               `json:"requested_block_id"`
	Metadata         map[string]any       `json:"metadata,omitempty"`
	EvidenceRefs     []memory.EvidenceRef `json:"evidence_refs,omitempty"`
	OperationID      string               `json:"operation_id,omitempty"`
	SessionID        string               `json:"session_id,omitempty"`
	ProjectID        string               `json:"project_id,omitempty"`
	TaskID           string               `json:"task_id,omitempty"`
	SubtaskID        string               `json:"subtask_id,omitempty"`
	UserID           string               `json:"user_id,omitempty"`
	ParentSessionID  string               `json:"parent_session_id,omitempty"`
}

func correctionOwner(value any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	owner, ok := value.(map[string]any)
	if !ok || len(owner) != 2 {
		return nil, errors.New("working_set_owner requires an exact owner and generation")
	}
	for _, key := range []string{"owner_id", "generation_id"} {
		selected, ok := owner[key].(string)
		if !ok || strings.TrimSpace(selected) == "" {
			return nil, errors.New("working_set_owner requires an exact owner and generation")
		}
	}
	return owner, nil
}

func (engine *Engine) CorrectFor(ctx context.Context, request CorrectRequest, callerRole string,
	protocolVersion int) (map[string]any, error) {
	if callerRole == "" || protocolVersion < 1 {
		return nil, errors.New("correction requires authenticated caller identity")
	}
	if request.MemoryID == "" || request.MemoryID != strings.TrimSpace(request.MemoryID) ||
		request.RequestedBlockID == "" || request.RequestedBlockID != strings.TrimSpace(request.RequestedBlockID) ||
		strings.TrimSpace(request.Content) == "" {
		return nil, errors.New("correct requires exact memory_id, requested_block_id, and nonempty content")
	}
	if request.ExpectedRevision != "" && request.ExpectedRevision != strings.TrimSpace(request.ExpectedRevision) {
		return nil, errors.New("expected_revision must be exact")
	}
	lock, err := engine.acquireEraseWorkspaceLock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	target, err := engine.ReadStored(ctx, request.MemoryID)
	if err != nil {
		return nil, err
	}
	if target != nil {
		if err := engine.assertChainNotErased(ctx, target.CanonicalKey); err != nil {
			return nil, err
		}
	}
	if request.OperationID == "" {
		fingerprint, err := memory.PythonHashValue(request)
		if err != nil {
			return nil, err
		}
		request.OperationID = "correct:" + fingerprint[:32]
	}
	if request.OperationID != strings.TrimSpace(request.OperationID) || len(request.OperationID) > 256 {
		return nil, errors.New("correction operation_id must be a bounded opaque string")
	}
	identity := receipts.Identity{WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, Backend: engine.config.BackendID,
		CallerRole: callerRole, ProtocolVersion: protocolVersion}
	fingerprintRequest := request
	fingerprintRequest.OperationID = ""
	arguments := map[string]any{"tool": "remember-correct", "request": fingerprintRequest}
	claimed, err := receipts.Claim(ctx, engine.config.RepoRoot, identity, request.OperationID, arguments)
	if err != nil {
		return nil, err
	}
	if claimed.Status == "committed" {
		inner, ok := claimed.Response["response"].(map[string]any)
		if claimed.Response["receipt_kind"] != "mutation_response" ||
			claimed.Response["mutation_tool"] != "remember" || !ok || inner["ok"] != true {
			return nil, errors.New("correction operation receipt is invalid")
		}
		id, ok := inner["memory_id"].(string)
		if !ok || id == "" {
			return nil, errors.New("correction operation receipt has no canonical record")
		}
		record, err := engine.ReadStored(ctx, id)
		if err != nil || record == nil || record.RevisionID != inner["revision_id"] {
			return nil, errors.New("committed correction revision is unavailable")
		}
		copy := make(map[string]any, len(inner)+1)
		for key, value := range inner {
			copy[key] = value
		}
		copy["replayed"] = true
		return copy, nil
	}
	innerHash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", callerRole,
		protocolVersion, request.OperationID)))
	workRequest := request
	workRequest.OperationID = "correct-write:" + hex.EncodeToString(innerHash[:])
	result, err := engine.correctOnce(ctx, workRequest, callerRole, protocolVersion)
	if err != nil {
		checksum := sha256.Sum256([]byte(err.Error()))
		failure := map[string]any{"ok": false, "error_type": "GoError",
			"reason_code": "correction_write_failed", "error_sha256": hex.EncodeToString(checksum[:])}
		if completeErr := receipts.Complete(ctx, engine.config.RepoRoot, identity,
			request.OperationID, arguments, failure, "failed"); completeErr != nil {
			return nil, errors.Join(err, completeErr)
		}
		return nil, err
	}
	result["operation_id"] = request.OperationID
	wrapper := map[string]any{"receipt_kind": "mutation_response", "mutation_tool": "remember",
		"response": result}
	if err := receipts.Complete(ctx, engine.config.RepoRoot, identity,
		request.OperationID, arguments, wrapper, "committed"); err != nil {
		return nil, err
	}
	return result, nil
}

func (engine *Engine) correctOnce(ctx context.Context, request CorrectRequest, callerRole string,
	protocolVersion int) (map[string]any, error) {
	for key := range request.Metadata {
		if memory.ReservedMetadataKey(key) || losslessProofMetadata[key] ||
			key == "operation_id" || key == "operation_caller_role" ||
			key == "operation_protocol_version" || key == "operation_request_fingerprint" ||
			key == "pending_host_approval" || key == "correction_target_id" ||
			key == "correction_target_revision_id" {
			return nil, fmt.Errorf("correction metadata cannot override memory governance: %s", key)
		}
	}
	owner, err := correctionOwner(request.Metadata["working_set_owner"])
	if err != nil {
		return nil, err
	}
	root, err := engine.ReadStored(ctx, request.MemoryID)
	if err != nil {
		return nil, err
	}
	if root == nil || root.CanonicalKey == "" {
		return nil, errors.New("correction target was not found in this profile")
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
	if request.ExpectedRevision != "" && request.ExpectedRevision != current.RevisionID &&
		!(current.RevisionOf == root.BlockID && request.ExpectedRevision == root.RevisionID &&
			current.Status == "candidate") {
		return nil, errors.New("correction source revision is no longer current")
	}
	if current.BlockMembership.PrimaryBlockID != request.RequestedBlockID {
		return nil, errors.New("correction target is outside the selected logical block")
	}
	if err := correctionScope(*current, request); err != nil {
		return nil, err
	}
	if current.Status == "retracted" || current.Status == "deprecated" {
		return nil, errors.New("inactive canonical memory cannot be corrected")
	}
	if current.Status == "accepted" || current.Authority == "directive" ||
		current.TrustLevel == "workspace_verified" || current.TrustLevel == "external_verified" ||
		current.TrustLevel == "supervisor_verified" || current.TrustLevel == "human_approved" ||
		current.TrustLevel == "system_curated" || current.VerificationStatus == "verified" {
		return engine.correctTrusted(ctx, *current, request, callerRole, protocolVersion)
	}
	if current.Metadata["working_set_owner"] != nil &&
		!sameCorrectionOwner(current.Metadata["working_set_owner"], owner) {
		return nil, errors.New("correction candidate belongs to another owner or generation")
	}
	expected := request.ExpectedRevision
	if expected == "" {
		expected = root.RevisionID
	}
	content := request.Content
	var title *string
	if request.Title != "" {
		title = &request.Title
	}
	result, err := engine.amendForLocked(ctx, AmendRequest{MemoryID: request.MemoryID,
		ExpectedRevision: expected, Reason: defaultValue(request.Reason, "correction"),
		Content: &content, Title: title, MetadataPatch: request.Metadata,
		EvidenceRefs: request.EvidenceRefs, RequestedBlockID: request.RequestedBlockID,
		OperationID: request.OperationID}, callerRole, protocolVersion,
		map[string]string{"session_id": request.SessionID, "project_id": request.ProjectID,
			"task_id": request.TaskID, "subtask_id": request.SubtaskID, "user_id": request.UserID})
	if err != nil {
		return nil, err
	}
	id, _ := result["new_id"].(string)
	record, err := engine.ReadStored(ctx, id)
	if err != nil || record == nil {
		return nil, errors.New("committed correction is unavailable")
	}
	result["block_id"], result["memory_id"] = id, id
	result["canonical_key"], result["revision_id"] = record.CanonicalKey, record.RevisionID
	result["recorded_at"], result["pending_host_approval"] = record.RecordedAt, false
	return result, nil
}

func sameCorrectionOwner(left, right any) bool {
	a, okA := left.(map[string]any)
	b, okB := right.(map[string]any)
	return okA && okB && a["owner_id"] == b["owner_id"] &&
		a["generation_id"] == b["generation_id"]
}

func correctionScope(current memory.MemoryRecord, request CorrectRequest) error {
	switch current.Scope {
	case "project":
		if current.ProjectID == "" || current.ProjectID != request.ProjectID {
			return errors.New("correction project is outside caller lineage")
		}
	case "session":
		if current.SessionID != request.SessionID {
			return errors.New("correction session is outside caller lineage")
		}
	case "task":
		if current.TaskID != request.TaskID || current.SessionID != request.SessionID {
			return errors.New("correction task is outside caller lineage")
		}
	case "subtask":
		if current.SubtaskID != request.SubtaskID || current.TaskID != request.TaskID ||
			current.SessionID != request.SessionID {
			return errors.New("correction subtask is outside caller lineage")
		}
	case "user_global":
		if current.UserID != request.UserID {
			return errors.New("correction user is outside caller lineage")
		}
	}
	return nil
}

func (engine *Engine) correctTrusted(ctx context.Context, current memory.MemoryRecord,
	request CorrectRequest, callerRole string, protocolVersion int) (map[string]any, error) {
	refs, err := mergeEvidenceRefs(current.EvidenceRefs, request.EvidenceRefs)
	if err != nil {
		return nil, err
	}
	owner, err := correctionOwner(request.Metadata["working_set_owner"])
	if err != nil {
		return nil, err
	}
	keyHash, err := memory.PythonHashValue(map[string]any{
		"target_id": current.BlockID, "owner": owner, "session_id": request.SessionID})
	if err != nil {
		return nil, err
	}
	proposalKey := current.CanonicalKey + ":correction-proposal:" + keyHash[:16]
	proposalRootID := memory.BlockIDForCanonicalKey(proposalKey)
	proposalRoot, err := engine.ReadStored(ctx, proposalRootID)
	if err != nil {
		return nil, err
	}
	var proposalHead *memory.MemoryRecord
	if proposalRoot != nil {
		if proposalRoot.CanonicalKey != proposalKey ||
			proposalRoot.Metadata["correction_target_id"] != current.BlockID {
			return nil, errors.New("correction proposal key conflicts with existing memory")
		}
		rows, err := engine.index.ProjectionRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Current && row.Status == "committed" && row.Backend == engine.recordNativeStore(row.Record) &&
				row.Record.CanonicalKey == proposalKey {
				if proposalHead != nil {
					return nil, errors.New("correction proposal has more than one current head")
				}
				copy := row.Record
				proposalHead = &copy
			}
		}
		if proposalHead == nil || proposalHead.Status != "candidate" ||
			proposalHead.Authority != "advisory" || proposalHead.BlockMembership == nil ||
			proposalHead.BlockMembership.PrimaryBlockID != request.RequestedBlockID {
			return nil, errors.New("correction proposal is not an active advisory candidate")
		}
	}
	metadata := make(map[string]any, len(current.Metadata)+len(request.Metadata)+4)
	for key, value := range current.Metadata {
		if key != "working_set_owner" && key != "promotion_operation_id" &&
			!losslessProofMetadata[key] && key != "write_mode" && key != "operation_id" &&
			key != "operation_caller_role" && key != "operation_protocol_version" &&
			key != "operation_request_fingerprint" {
			metadata[key] = value
		}
	}
	for key, value := range request.Metadata {
		metadata[key] = value
	}
	metadata["status_reason"] = defaultValue(request.Reason, "correction")
	metadata["correction_target_id"] = current.BlockID
	metadata["correction_target_revision_id"] = current.RevisionID
	metadata["pending_host_approval"] = true
	title := defaultValue(request.Title, current.Title)
	var proposalID string
	var replayed bool
	var commitRevision int64
	if proposalHead == nil {
		proposal, err := engine.RememberCandidateFor(ctx, CandidateRequest{
			OperationID: request.OperationID, Operation: "add", Content: request.Content,
			Title: title, Kind: current.Kind, Tags: current.Tags, Metadata: metadata,
			EvidenceRefs: refs, RequestedBlockID: request.RequestedBlockID,
			UserID: request.UserID, ProjectID: request.ProjectID, SessionID: request.SessionID,
			TaskID: request.TaskID, SubtaskID: request.SubtaskID,
			ParentSessionID: request.ParentSessionID, RunID: request.SessionID,
			Sensitivity: current.Sensitivity, ContentMode: current.ContentMode,
			OperationalRisk:      current.OperationalRisk,
			proposalCanonicalKey: proposalKey,
			proposalEvidenceRefs: request.EvidenceRefs,
		}, callerRole, protocolVersion)
		if err != nil {
			return nil, err
		}
		if !proposal.Saved {
			return nil, errors.New("correction proposal was rejected by the selected block route: " + proposal.Route.Reason)
		}
		proposalID, replayed = proposal.RecordID, proposal.Replayed
		if proposal.Receipt != nil {
			commitRevision = proposal.Receipt.CommitRevision
		}
		proposalRoot, err = engine.ReadStored(ctx, proposalRootID)
		if err != nil || proposalRoot == nil {
			return nil, errors.New("committed correction proposal root is unavailable")
		}
	} else {
		combined, err := mergeEvidenceRefs(proposalHead.EvidenceRefs, request.EvidenceRefs)
		if err != nil {
			return nil, err
		}
		unchanged := proposalHead.Content == request.Content && proposalHead.Title == title &&
			proposalHead.Metadata["status_reason"] == metadata["status_reason"] &&
			proposalHead.Metadata["correction_target_revision_id"] == current.RevisionID &&
			reflect.DeepEqual(proposalHead.EvidenceRefs, combined)
		for key, value := range request.Metadata {
			unchanged = unchanged && reflect.DeepEqual(proposalHead.Metadata[key], value)
		}
		if unchanged {
			proposalID, replayed = proposalHead.BlockID, true
		} else {
			content := request.Content
			amended, err := engine.amendForLocked(ctx, AmendRequest{
				MemoryID: proposalHead.BlockID, ExpectedRevision: proposalHead.RevisionID,
				Reason: defaultValue(request.Reason, "correction"), Content: &content,
				Title: &title, MetadataPatch: metadata, EvidenceRefs: request.EvidenceRefs,
				RequestedBlockID: request.RequestedBlockID, OperationID: request.OperationID,
			}, callerRole, protocolVersion, map[string]string{
				"session_id": request.SessionID, "project_id": request.ProjectID,
				"task_id": request.TaskID, "subtask_id": request.SubtaskID,
				"user_id": request.UserID})
			if err != nil {
				return nil, err
			}
			proposalID, _ = amended["new_id"].(string)
			replayed, _ = amended["replayed"].(bool)
			commitRevision, _ = amended["commit_revision"].(int64)
		}
	}
	edges, err := engine.index.EdgesFor(ctx, proposalRootID)
	if err != nil {
		return nil, err
	}
	linked := false
	for _, edge := range edges {
		if edge.SrcID == proposalRootID && edge.DstID == current.BlockID &&
			edge.EdgeType == "contradicts" && edge.Reason == "pending_host_correction" &&
			edge.Metadata["correction_target_revision_id"] == current.RevisionID {
			linked = true
			break
		}
	}
	if !linked {
		return nil, errors.New("committed correction proposal has no target link")
	}
	record, err := engine.ReadStored(ctx, proposalID)
	if err != nil || record == nil {
		return nil, errors.New("correction proposal is unavailable")
	}
	result := map[string]any{"ok": true, "saved": true, "block_id": proposalID,
		"memory_id": proposalID, "pending_host_approval": true,
		"correction_target_id": current.BlockID, "canonical_key": record.CanonicalKey,
		"revision_id": record.RevisionID, "recorded_at": record.RecordedAt,
		"backend": engine.config.BackendID, "workspace_id": engine.config.WorkspaceID,
		"replayed": replayed}
	if commitRevision != 0 {
		result["commit_revision"] = commitRevision
		result["durable_status"] = "committed"
		result["projection_status"] = "committed"
	}
	return result, nil
}
