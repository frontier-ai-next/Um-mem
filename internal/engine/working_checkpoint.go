package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/ledger"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type CommitWorkingCheckpointRequest struct {
	Checkpoint         memory.WorkingStateCheckpoint `json:"checkpoint"`
	VerificationReport *ledger.VerificationReport    `json:"verification_report,omitempty"`
}

func checkpointReferences(checkpoint memory.WorkingStateCheckpoint) []string {
	result := []string{}
	seen := map[string]bool{}
	add := func(refs []string) {
		for _, ref := range refs {
			if !seen[ref] {
				result = append(result, ref)
				seen[ref] = true
			}
		}
	}
	add(checkpoint.SourceEventRefs)
	add(checkpoint.TaskAnchorRefs)
	add(checkpoint.SystemAnchorRefs)
	add(checkpoint.ExactRecentTailRefs)
	for _, page := range checkpoint.EvidencePageRefs {
		add(page)
	}
	add(checkpoint.TransitiveRawRefs)
	return result
}

func (engine *Engine) compileContinuationPacket(ctx context.Context,
	checkpoint memory.WorkingStateCheckpoint) (memory.ContinuationPacket, error) {
	packet, err := memory.BuildContinuationPacket(checkpoint)
	if err != nil {
		return packet, err
	}
	checkpoint = packet.Checkpoint
	records := make(map[string]memory.MemoryRecord)
	for _, rawID := range checkpointReferences(checkpoint) {
		record, err := engine.ReadStored(ctx, rawID)
		if err != nil {
			return packet, err
		}
		if record == nil || record.BlockID != rawID {
			return packet, fmt.Errorf("raw reference %q is missing", rawID)
		}
		if record.WorkspaceID != checkpoint.WorkspaceID || record.ProfileID != checkpoint.ProfileID ||
			record.SessionID != checkpoint.SessionID || record.TaskID != checkpoint.TaskID {
			return packet, fmt.Errorf("raw reference %q is outside the exact task scope", rawID)
		}
		hostEventID, _ := record.Metadata["host_event_id"].(string)
		if hostEventID == "" {
			hostEventID = record.ExternalRefs["host_event_id"]
		}
		recordKind, _ := record.Metadata["record_kind"].(string)
		if record.CanonicalKey == "" || record.RevisionID == "" || record.Producer != "host_observer" ||
			strings.TrimSpace(recordKind) == "" || strings.TrimSpace(hostEventID) == "" ||
			recordKind == "working_state_checkpoint" || record.RevisionOf != "" || len(record.SupersedesIDs) != 0 {
			return packet, fmt.Errorf("raw reference %q is not an immutable host event", rawID)
		}
		records[rawID] = *record
	}
	for _, pin := range checkpoint.SourceRefSHA256 {
		actual, err := memory.ContinuationSourceHash(records[pin[0]])
		if err != nil {
			return packet, err
		}
		if actual != pin[1] {
			return packet, errors.New("exact continuation source hash mismatch")
		}
	}
	window := make([]memory.MemoryRecord, 0, len(checkpoint.SourceEventRefs))
	for _, ref := range checkpoint.SourceEventRefs {
		window = append(window, records[ref])
	}
	actual, err := memory.ContinuationWindowHash(window)
	if err != nil {
		return packet, err
	}
	if actual != checkpoint.SourceEventSHA256 {
		return packet, errors.New("source_event_sha256 mismatch for immutable host events")
	}
	return packet, nil
}

func establishedCheckpointAnchors(current memory.MemoryRecord) ([]string, []string, error) {
	if current.Kind != "task_state" || current.Status != "candidate" ||
		current.Authority != "advisory" || current.Scope != "task" ||
		current.Metadata["record_kind"] != "working_state_checkpoint" ||
		current.Metadata["task_local"] != true {
		return nil, nil, errors.New("stored working-state payload is not trustworthy")
	}
	var packet memory.ContinuationPacket
	if err := json.Unmarshal([]byte(current.Content), &packet); err != nil {
		return nil, nil, errors.New("stored working-state payload is not trustworthy")
	}
	if packet.WorkspaceID != current.WorkspaceID || packet.SessionID != current.SessionID ||
		packet.TaskID != current.TaskID || packet.SchemaVersion != current.SchemaVersion ||
		packet.Checkpoint.WorkspaceID != current.WorkspaceID ||
		packet.Checkpoint.SessionID != current.SessionID || packet.Checkpoint.TaskID != current.TaskID ||
		packet.Checkpoint.SchemaVersion != current.SchemaVersion ||
		len(packet.TaskAnchorRefs) == 0 || len(packet.SystemAnchorRefs) == 0 ||
		!slices.Equal(packet.TaskAnchorRefs, packet.Checkpoint.TaskAnchorRefs) ||
		!slices.Equal(packet.SystemAnchorRefs, packet.Checkpoint.SystemAnchorRefs) {
		return nil, nil, errors.New("stored working-state payload is not trustworthy")
	}
	for _, refs := range [][]string{packet.TaskAnchorRefs, packet.SystemAnchorRefs} {
		seen := map[string]bool{}
		for _, ref := range refs {
			if ref == "" || ref != strings.TrimSpace(ref) || seen[ref] {
				return nil, nil, errors.New("stored working-state payload is not trustworthy")
			}
			seen[ref] = true
		}
	}
	return packet.TaskAnchorRefs, packet.SystemAnchorRefs, nil
}

func (engine *Engine) workingCheckpointTarget(ctx context.Context, checkpoint memory.WorkingStateCheckpoint) error {
	if err := engine.RefreshCatalog(); err != nil {
		return err
	}
	block, ok := engine.current.Load().byID[checkpoint.RequestedBlockID]
	if !ok || len(block.ChildIDs) != 0 || block.IndexOnly || !block.AgentWritable ||
		block.Scope != "task" || block.Authority != "advisory" {
		return errors.New("working checkpoint requires a model-selected writable task leaf")
	}
	state, err := engine.topology.GetBlock(ctx, checkpoint.RequestedBlockID)
	if err != nil {
		return err
	}
	if state == nil || state.Status != "active" || state.BackendPartitionID == "" {
		return errors.New("working checkpoint requires a materialized native leaf partition")
	}
	return nil
}

func (engine *Engine) CommitWorkingCheckpoint(ctx context.Context,
	request CommitWorkingCheckpointRequest) (map[string]any, error) {
	checkpoint, err := memory.NormalizeWorkingStateCheckpoint(request.Checkpoint)
	if err != nil {
		return nil, err
	}
	if checkpoint.WorkspaceID != engine.config.WorkspaceID || checkpoint.ProfileID != engine.config.ProfileID {
		return nil, errors.New("working checkpoint is outside selected workspace or profile")
	}
	var packet memory.ContinuationPacket
	if request.VerificationReport == nil {
		packet, err = engine.compileContinuationPacket(ctx, checkpoint)
		if err != nil {
			return nil, err
		}
	} else {
		subject, err := memory.CheckpointVerificationSubject(checkpoint)
		if err != nil {
			return nil, err
		}
		resolver, err := ledger.OpenResolver(engine.config.RepoRoot, engine.config.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if _, err := resolver.ValidateCandidateSemanticReport(*request.VerificationReport,
			subject, engine.config.RepoRoot, nil, false); err != nil {
			return nil, err
		}
		packet, err = memory.BuildContinuationPacket(checkpoint)
		if err != nil {
			return nil, err
		}
	}
	if err := engine.workingCheckpointTarget(ctx, checkpoint); err != nil {
		return nil, err
	}
	canonicalKey, err := memory.WorkingStateCanonicalKey(checkpoint.WorkspaceID,
		checkpoint.ProfileID, checkpoint.OwnerGeneration, checkpoint.SessionID, checkpoint.TaskID)
	if err != nil {
		return nil, err
	}
	content, err := jsoncompat.MarshalPython(packet, false)
	if err != nil {
		return nil, err
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Record.CanonicalKey != canonicalKey || row.Status != "pending" {
			continue
		}
		if row.Backend != engine.recordNativeStore(row.Record) ||
			row.Record.Content != string(content) || row.Record.BlockMembership == nil ||
			row.Record.BlockMembership.PrimaryBlockID != checkpoint.RequestedBlockID {
			return nil, errors.New("working checkpoint has a different pending canonical revision")
		}
		if _, err := engine.writer.CompleteProjection(ctx, row.Revision, engine.projector); err != nil {
			return nil, err
		}
	}
	history, err := engine.index.CanonicalHistory(ctx, canonicalKey)
	if err != nil {
		return nil, err
	}
	var current *memory.MemoryRecord
	if len(history) > 0 {
		current = &history[len(history)-1]
		if current.WorkspaceID != checkpoint.WorkspaceID || current.CanonicalKey != canonicalKey {
			return nil, errors.New("canonical working-state root is outside the requested scope")
		}
		taskAnchors, systemAnchors, err := establishedCheckpointAnchors(*current)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(taskAnchors, checkpoint.TaskAnchorRefs) ||
			!slices.Equal(systemAnchors, checkpoint.SystemAnchorRefs) {
			return nil, errors.New("working-state anchor drift is not allowed")
		}
	}
	record, err := memory.NewMemoryRecord(string(content), checkpoint.WorkspaceID)
	if err != nil {
		return nil, err
	}
	record.ProfileID, record.CanonicalKey = checkpoint.ProfileID, canonicalKey
	record.Kind, record.Status, record.Retention = "task_state", "candidate", "durable"
	record.DeliveryPolicy, record.Temperature = "working_set", "hot"
	record.Authority, record.TrustLevel, record.Scope = "advisory", "agent_reported", "task"
	record.VerificationStatus, record.PromotionDecision = "unverified", "not_requested"
	record.Title = "task-local working state: " + checkpoint.TaskID
	record.RunID = strings.SplitN(checkpoint.TaskID, ":", 2)[0]
	record.StageID, record.Producer, record.SourceBackend = "working_state", "agent", "um_mem"
	record.Tags = []string{"working-state-checkpoint", "task-local", "structured-projection"}
	record.EvidenceRefs = checkpoint.EvidenceRefs
	record.ExternalRefs = map[string]string{"host_event_id": checkpoint.HostEventID,
		"session_id": checkpoint.SessionID, "task_id": checkpoint.TaskID,
		"artifact_id": checkpoint.ArtifactID, "artifact_revision": checkpoint.ArtifactRevision}
	sourceManifest := make([]map[string]any, 0)
	for _, role := range []struct {
		name string
		refs []string
	}{
		{"source_event", checkpoint.SourceEventRefs}, {"task_anchor", checkpoint.TaskAnchorRefs},
		{"system_anchor", checkpoint.SystemAnchorRefs}, {"recent_tail", checkpoint.ExactRecentTailRefs},
	} {
		for _, ref := range role.refs {
			sourceManifest = append(sourceManifest, map[string]any{"block_id": ref, "role": role.name})
		}
	}
	for i, page := range checkpoint.EvidencePageRefs {
		for _, ref := range page {
			sourceManifest = append(sourceManifest, map[string]any{"block_id": ref,
				"role": "evidence_card", "page": i + 1})
		}
	}
	record.Metadata = map[string]any{"record_kind": "working_state_checkpoint", "task_local": true,
		"working_set_owner": map[string]any{"profile_id": checkpoint.ProfileID,
			"session_id": checkpoint.SessionID, "task_id": checkpoint.TaskID,
			"generation": checkpoint.OwnerGeneration},
		"checkpoint_payload": checkpoint, "source_event_range": packet.SourceEventRange,
		"source_event_sha256": packet.SourceEventSHA256, "raw_reference_manifest": sourceManifest}
	record.SessionID, record.TaskID, record.ParentSessionID = checkpoint.SessionID, checkpoint.TaskID, checkpoint.ParentSessionID
	record.ArtifactID, record.ArtifactRevision = checkpoint.ArtifactID, checkpoint.ArtifactRevision
	record.ValidFrom, record.ValidTo = checkpoint.ValidFrom, checkpoint.ValidTo
	record.ProjectID, record.RepositoryID = checkpoint.ProjectID, checkpoint.RepositoryID
	record.Checkout, record.Worktree, record.Branch = checkpoint.Checkout, checkpoint.Worktree, checkpoint.Branch
	record.Revision, record.DirtyState = checkpoint.Revision, checkpoint.DirtyState
	record.SchemaVersion, record.HardPolicyVersion = checkpoint.SchemaVersion, checkpoint.HardPolicyVersion
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	record.RecordedAt, record.CreatedAt, record.UpdatedAt = stamp, stamp, stamp
	if current != nil {
		if current.Content == record.Content && current.ArtifactRevision == record.ArtifactRevision &&
			current.BlockMembership != nil && current.BlockMembership.PrimaryBlockID == checkpoint.RequestedBlockID {
			return map[string]any{"ok": true, "saved": true, "replayed": true,
				"memory_id": current.BlockID, "requested_block_id": checkpoint.RequestedBlockID,
				"actual_block_id": checkpoint.RequestedBlockID, "route_outcome": "idempotent_replay",
				"status": current.Status, "authority": current.Authority}, nil
		}
		record.ValidFrom, record.ValidTo = stamp, ""
		record.RevisionOf = current.BlockID
		record.SupersedesIDs = []string{current.BlockID}
	}
	record.DuplicateClusterID, err = memory.DuplicateClusterID(record)
	if err != nil {
		return nil, err
	}
	action, predecessorRevision := "insert", ""
	if current != nil {
		action, predecessorRevision = "supersede", current.RevisionID
	}
	record.RevisionID, err = memory.RevisionIDForRecord(canonicalKey, record, action, predecessorRevision)
	if err != nil {
		return nil, err
	}
	if current == nil {
		record.BlockID = memory.BlockIDForCanonicalKey(canonicalKey)
		if err := engine.BindRootRecord(ctx, &record, checkpoint.RequestedBlockID); err != nil {
			return nil, err
		}
	} else {
		record.BlockID = memory.BlockIDForRevisionID(record.RevisionID)
		if err := engine.BindSuccessorRecord(ctx, &record, checkpoint.RequestedBlockID); err != nil {
			return nil, err
		}
	}
	var receipt any
	if current == nil {
		receipt, err = engine.PutRoot(ctx, record)
	} else {
		receipt, err = engine.PutSuccessor(ctx, record, "supersede", "")
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "saved": true, "replayed": false,
		"memory_id": record.BlockID, "requested_block_id": checkpoint.RequestedBlockID,
		"actual_block_id": checkpoint.RequestedBlockID, "route_outcome": "working_checkpoint",
		"status": record.Status, "authority": record.Authority, "receipt": receipt}, nil
}
