package localservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/mcpserver"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// EngineDispatcher exposes implemented Go service operations. The role and
// profile are bound by the lease and engine, never by tool arguments.
func EngineDispatcher(selected *engine.Engine) Dispatcher {
	return func(ctx context.Context, caller Caller, tool string, args map[string]any) map[string]any {
		var result map[string]any
		var err error
		switch tool {
		case "read_exact_record":
			result, err = dispatchExactRecord(ctx, selected, caller, args)
		case "read_exact_records":
			result, err = dispatchExactRecords(ctx, selected, caller, args)
		case "operation_status":
			result, err = dispatchOperationStatus(ctx, selected, caller, args)
		case "projection_barrier":
			result, err = dispatchProjectionBarrier(ctx, selected, caller, args)
		case "read_working_set":
			result, err = dispatchWorkingSet(ctx, selected, caller, args)
		case "recall":
			result, err = dispatchRecall(ctx, selected, caller, args)
		case "build_context":
			result, err = dispatchBuildContext(ctx, selected, caller, args)
		case "build_granted_context":
			result, err = dispatchGrantedContext(ctx, selected, caller, args)
		case "recent":
			result, err = dispatchRecent(ctx, selected, caller, args)
		case "procedure":
			result, err = dispatchProcedure(ctx, selected, caller, args)
		case "list_records":
			result, err = dispatchListRecords(ctx, selected, caller, args)
		case "block_map":
			result, err = dispatchBlockMap(ctx, selected, caller, args)
		case "memory_map":
			result, err = dispatchMemoryMap(ctx, selected, caller, args)
		case "export_obsidian":
			result, err = dispatchExportObsidian(ctx, selected, caller, args)
		case "expand_block":
			result, err = dispatchExpandBlock(ctx, selected, caller, args)
		case "navigation_envelope":
			result, err = dispatchNavigationEnvelope(ctx, selected, caller, args)
		case "pending_topology_evolutions":
			if !validArguments(args) {
				err = errors.New("unknown pending_topology_evolutions argument")
			} else {
				result, err = mcpserver.PendingTopologyEvolutionsPayload(ctx, selected,
					caller.Identity.WorkspaceID)
			}
		case "abort_topology_evolution":
			if !validArguments(args, "operation_id", "reason") {
				err = errors.New("unknown abort_topology_evolution argument")
			} else {
				var parsed mcpserver.AbortTopologyEvolutionArgs
				if err = decodeActiveMaskArgs(args, &parsed); err == nil {
					result, err = mcpserver.AbortTopologyEvolutionPayload(ctx, selected, parsed)
				}
			}
		case "resume_topology_evolution":
			if !validArguments(args, "operation_id") {
				err = errors.New("unknown resume_topology_evolution argument")
			} else {
				var parsed mcpserver.ResumeTopologyEvolutionArgs
				if err = decodeActiveMaskArgs(args, &parsed); err == nil {
					result, err = mcpserver.ResumeTopologyEvolutionPayload(ctx, selected, parsed)
				}
			}
		case "split_block":
			if !validArguments(args, "block_id", "expected_revision", "targets", "reason", "operation_id") {
				err = errors.New("unknown split_block argument")
			} else {
				var parsed mcpserver.SplitBlockArgs
				if err = decodeActiveMaskArgs(args, &parsed); err == nil {
					result, err = mcpserver.SplitBlockPayload(ctx, selected, parsed)
				}
			}
		case "merge_blocks":
			if !validArguments(args, "sources", "target", "parent_id", "reason", "operation_id") {
				err = errors.New("unknown merge_blocks argument")
			} else {
				var parsed mcpserver.MergeBlocksArgs
				if err = decodeActiveMaskArgs(args, &parsed); err == nil {
					result, err = mcpserver.MergeBlocksPayload(ctx, selected, parsed)
				}
			}
		case "observe_maintenance":
			result, err = dispatchObserveMaintenance(ctx, selected, caller, args)
		case "add_block_edge":
			result, err = dispatchAddBlockEdge(ctx, selected, caller, args)
		case "remove_block_edge":
			result, err = dispatchRemoveBlockEdge(ctx, selected, caller, args)
		case "add_edge":
			result, err = dispatchAddEdge(ctx, selected, caller, args)
		case "export_graph":
			if !validArguments(args) {
				err = errors.New("unknown export_graph argument")
			} else {
				result, err = mcpserver.ExportGraphPayload(ctx, selected,
					caller.Identity.WorkspaceID, caller.Identity.Backend)
			}
		case "rebuild_indexes":
			if !validArguments(args) {
				err = errors.New("unknown rebuild_indexes argument")
			} else {
				result, err = selected.RebuildIndexes(ctx)
			}
		case "create_block":
			result, err = dispatchCreateBlock(ctx, selected, caller, args)
		case "amend_block":
			result, err = dispatchAmendBlock(ctx, selected, caller, args)
		case "convert_block_to_index":
			result, err = dispatchConvertBlockToIndex(ctx, selected, caller, args)
		case "archive_block":
			result, err = dispatchArchiveBlock(ctx, selected, caller, args)
		case "revive_block":
			result, err = dispatchReviveBlock(ctx, selected, caller, args)
		case "move_block":
			result, err = dispatchMoveBlock(ctx, selected, caller, args)
		case "delete_block":
			result, err = dispatchDeleteBlock(ctx, selected, caller, args)
		case "forget":
			result, err = dispatchForget(ctx, selected, caller, args)
		case "amend":
			result, err = dispatchAmend(ctx, selected, caller, args)
		case "verification_subject":
			result, err = dispatchVerificationSubject(ctx, selected, caller, args)
		case "promote":
			result, err = dispatchPromote(ctx, selected, caller, args)
		case "consolidate_observations":
			result, err = dispatchConsolidateObservations(ctx, selected, caller, args)
		case "commit_working_checkpoint":
			result, err = dispatchCommitWorkingCheckpoint(ctx, selected, caller, args)
		case "remember":
			result, err = dispatchRemember(ctx, selected, caller, args)
		case "observe":
			result, err = dispatchObserve(ctx, selected, caller, args)
		case "get_active_mask":
			result, err = dispatchGetActiveMask(ctx, selected, caller, args)
		case "resolve_active_mask":
			result, err = dispatchResolveActiveMask(ctx, selected, caller, args)
		case "set_active_mask":
			result, err = dispatchSetActiveMask(ctx, selected, caller, args)
		case "amend_active_mask":
			result, err = dispatchAmendActiveMask(ctx, selected, caller, args)
		case "transition_active_mask":
			result, err = dispatchTransitionActiveMask(ctx, selected, caller, args)
		default:
			return failure("Go local service tool is not implemented")
		}
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error(),
				"workspace_id": caller.Identity.WorkspaceID, "backend": caller.Identity.Backend}
		}
		return result
	}
}

func dispatchProcedure(ctx context.Context, selected *engine.Engine, caller Caller,
	args map[string]any) (map[string]any, error) {
	if !validArguments(args, "query", "limit") {
		return nil, errors.New("unknown procedure argument")
	}
	var parsed mcpserver.ProcedureArgs
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("procedure arguments are invalid")
	}
	return mcpserver.ProcedurePayload(ctx, selected, parsed,
		caller.Identity.WorkspaceID, caller.Lineage.SessionID)
}

func dispatchBlockMap(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "depth", "include_meta", "child_limit") {
		return nil, errors.New("unknown block_map argument")
	}
	var parsed mcpserver.BlockMapArgs
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("block_map arguments are invalid")
	}
	return mcpserver.BlockMapPayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchMemoryMap(ctx context.Context, selected *engine.Engine, caller Caller,
	args map[string]any) (map[string]any, error) {
	if caller.Role != "parent" && caller.Role != "supervisor" && caller.Role != "operator" {
		return nil, errors.New("memory_map requires a parent, supervisor or operator role")
	}
	if !validArguments(args, "output_path") {
		return nil, errors.New("unknown memory_map argument")
	}
	outputPath := ""
	if raw, exists := args["output_path"]; exists {
		value, ok := raw.(string)
		if !ok {
			return nil, errors.New("memory_map output_path must be text")
		}
		outputPath = value
	}
	result, err := selected.WriteMemoryMap(ctx, outputPath)
	if err != nil {
		return nil, err
	}
	result["workspace_id"], result["backend"] = caller.Identity.WorkspaceID, caller.Identity.Backend
	return result, nil
}

func dispatchExportObsidian(ctx context.Context, selected *engine.Engine, caller Caller,
	args map[string]any) (map[string]any, error) {
	if caller.Role != "parent" && caller.Role != "supervisor" && caller.Role != "operator" {
		return nil, errors.New("export_obsidian requires a parent, supervisor or operator role")
	}
	if !validArguments(args, "vault_dir", "project_title", "scope_chain") {
		return nil, errors.New("unknown export_obsidian argument")
	}
	var parsed mcpserver.ExportObsidianArgs
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, err
	}
	chain, err := boundScopeChain(caller.Lineage, parsed.ScopeChain)
	if err != nil {
		return nil, err
	}
	vault, err := selected.AuthorizedObsidianVault(parsed.VaultDir)
	if err != nil {
		return nil, err
	}
	return selected.ExportObsidian(ctx, vault, parsed.ProjectTitle, chain)
}

func dispatchExpandBlock(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "block_id", "limit", "cursor") {
		return nil, errors.New("unknown expand_block argument")
	}
	var parsed mcpserver.ExpandBlockArgs
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("expand_block arguments are invalid")
	}
	return mcpserver.ExpandBlockPayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchNavigationEnvelope(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "block_id", "limit", "cursor", "related_cursor") {
		return nil, errors.New("unknown navigation_envelope argument")
	}
	var parsed mcpserver.NavigationEnvelopeArgs
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("navigation_envelope arguments are invalid")
	}
	return mcpserver.NavigationEnvelopePayload(ctx, selected, parsed,
		caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchObserveMaintenance(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "block_id", "observation_type", "values", "threshold_revision", "evidence_ids") {
		return nil, errors.New("unknown observe_maintenance argument")
	}
	var parsed catalog.AddObservationRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("observe_maintenance arguments are invalid")
	}
	return mcpserver.ObserveMaintenancePayload(ctx, selected, parsed,
		caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchAddBlockEdge(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "src_id", "dst_id", "edge_type", "reason") {
		return nil, errors.New("unknown add_block_edge argument")
	}
	var parsed mcpserver.AddBlockEdgeArgs
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("add_block_edge arguments are invalid")
	}
	return mcpserver.AddBlockEdgePayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchRemoveBlockEdge(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "edge_id", "expected_revision", "reason") {
		return nil, errors.New("unknown remove_block_edge argument")
	}
	var parsed mcpserver.RemoveBlockEdgeArgs
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("remove_block_edge arguments are invalid")
	}
	return mcpserver.RemoveBlockEdgePayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchAddEdge(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "src_id", "dst_id", "edge_type", "reason", "weight", "evidence_refs", "metadata") {
		return nil, errors.New("unknown add_edge argument")
	}
	var parsed engine.AddEdgeRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("add_edge arguments are invalid")
	}
	return mcpserver.AddEdgePayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchCreateBlock(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "name", "synopsis", "parent_id", "reason", "tags", "metadata",
		"scope", "index_only", "token_budget", "expires_at") {
		return nil, errors.New("unknown create_block argument")
	}
	var parsed engine.CreateBlockRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("create_block arguments are invalid")
	}
	return mcpserver.CreateBlockPayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchAmendBlock(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "block_id", "expected_revision", "reason", "name", "synopsis",
		"tags", "metadata_patch", "token_budget", "expires_at") {
		return nil, errors.New("unknown amend_block argument")
	}
	var parsed engine.AmendBlockRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("amend_block arguments are invalid")
	}
	return mcpserver.AmendBlockPayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchConvertBlockToIndex(ctx context.Context, selected *engine.Engine,
	caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "block_id", "expected_revision", "reason") {
		return nil, errors.New("unknown convert_block_to_index argument")
	}
	var parsed engine.ConvertBlockToIndexRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("convert_block_to_index arguments are invalid")
	}
	return mcpserver.ConvertBlockToIndexPayload(ctx, selected, parsed,
		caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchArchiveBlock(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "block_id", "expected_revision", "reason") {
		return nil, errors.New("unknown archive_block argument")
	}
	var parsed engine.BlockTransitionRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("archive_block arguments are invalid")
	}
	return mcpserver.ArchiveBlockPayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchReviveBlock(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "block_id", "expected_revision", "reason") {
		return nil, errors.New("unknown revive_block argument")
	}
	var parsed engine.BlockTransitionRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("revive_block arguments are invalid")
	}
	return mcpserver.ReviveBlockPayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchMoveBlock(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "block_id", "parent_id", "expected_revision", "reason") {
		return nil, errors.New("unknown move_block argument")
	}
	var parsed engine.MoveBlockRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("move_block arguments are invalid")
	}
	return mcpserver.MoveBlockPayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchDeleteBlock(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "block_id", "expected_revision", "reason", "cascade") {
		return nil, errors.New("unknown delete_block argument")
	}
	var parsed engine.DeleteBlockRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("delete_block arguments are invalid")
	}
	return mcpserver.DeleteBlockPayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchForget(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "memory_id", "reason", "evidence_refs", "expected_revision") {
		return nil, errors.New("unknown forget argument")
	}
	var parsed engine.ForgetRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("forget arguments are invalid")
	}
	return mcpserver.ForgetPayload(ctx, selected, parsed, caller.Identity.WorkspaceID, caller.Identity.Backend)
}

func dispatchAmend(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "memory_id", "expected_revision", "reason", "content", "title",
		"tags", "metadata_patch", "retention", "expires_at", "evidence_refs", "requested_block_id", "operation_id") {
		return nil, errors.New("unknown amend argument")
	}
	var parsed engine.AmendRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("amend arguments are invalid")
	}
	lineage := map[string]string{"workspace_id": caller.Lineage.WorkspaceID,
		"project_id": caller.Lineage.ProjectID, "session_id": caller.Lineage.SessionID,
		"task_id": caller.Lineage.TaskID, "subtask_id": caller.Lineage.SubtaskID,
		"user_id": caller.Lineage.UserID}
	return mcpserver.AmendPayload(ctx, selected, parsed, caller.Identity.WorkspaceID,
		caller.Identity.Backend, caller.Role, ProtocolVersion, lineage)
}

func bindPromotionLineage(request *engine.VerificationSubjectRequest, caller Caller) error {
	bound := engine.VerificationLineage{UserID: caller.Lineage.UserID,
		ProjectID: caller.Lineage.ProjectID, SessionID: caller.Lineage.SessionID,
		TaskID: caller.Lineage.TaskID, SubtaskID: caller.Lineage.SubtaskID}
	for _, pair := range [][2]string{{request.Lineage.UserID, bound.UserID},
		{request.Lineage.ProjectID, bound.ProjectID}, {request.Lineage.SessionID, bound.SessionID},
		{request.Lineage.TaskID, bound.TaskID}, {request.Lineage.SubtaskID, bound.SubtaskID}} {
		if pair[0] != "" && pair[0] != pair[1] {
			return errors.New("promotion scope claim is not bound to the service lease")
		}
	}
	request.Lineage = bound
	return nil
}

func dispatchVerificationSubject(ctx context.Context, selected *engine.Engine, caller Caller,
	args map[string]any) (map[string]any, error) {
	if !validArguments(args, "memory_id", "expected_source_revision_id", "authority",
		"target_block_id", "target_scope", "target_scope_id", "source_evidence_refs", "lineage") {
		return nil, errors.New("unknown verification_subject argument")
	}
	var parsed engine.VerificationSubjectRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("verification_subject arguments are invalid")
	}
	if err := bindPromotionLineage(&parsed, caller); err != nil {
		return nil, err
	}
	subject, err := selected.VerificationSubject(ctx, parsed)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "subject": subject,
		"workspace_id": caller.Identity.WorkspaceID, "backend": caller.Identity.Backend}, nil
}

func dispatchPromote(ctx context.Context, selected *engine.Engine, caller Caller,
	args map[string]any) (map[string]any, error) {
	if !validArguments(args, "memory_id", "expected_source_revision_id", "reason",
		"operation_id", "evidence_refs", "source_evidence_refs", "authority",
		"target_block_id", "target_scope", "target_scope_id", "lineage",
		"workspace_root", "changed_files", "allow_workspace_hash_mismatch_if_diff_matches") {
		return nil, errors.New("unknown promote argument")
	}
	var parsed engine.PromoteRequest
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, errors.New("promote arguments are invalid")
	}
	if err := bindPromotionLineage(&parsed.VerificationSubjectRequest, caller); err != nil {
		return nil, err
	}
	result, err := selected.PromoteFor(ctx, parsed, caller.Role, ProtocolVersion)
	if err != nil {
		return nil, err
	}
	result["workspace_id"] = caller.Identity.WorkspaceID
	return result, nil
}

func dispatchConsolidateObservations(ctx context.Context, selected *engine.Engine,
	caller Caller, args map[string]any) (map[string]any, error) {
	if caller.Role != "supervisor" && caller.Role != "operator" {
		return nil, errors.New("consolidate_observations requires the supervisor or operator role")
	}
	if !validArguments(args, "source_ids", "content", "title", "reason", "verification_reports",
		"semantic_verification_report", "requested_block_id", "session_id", "task_id",
		"subtask_id", "project_id") {
		return nil, errors.New("unknown consolidate_observations argument")
	}
	var request engine.ConsolidateObservationsRequest
	if err := decodeActiveMaskArgs(args, &request); err != nil {
		return nil, errors.New("consolidate_observations arguments are invalid")
	}
	for _, pair := range [][2]string{{request.SessionID, caller.Lineage.SessionID},
		{request.TaskID, caller.Lineage.TaskID}, {request.SubtaskID, caller.Lineage.SubtaskID},
		{request.ProjectID, caller.Lineage.ProjectID}} {
		if pair[0] != "" && pair[0] != pair[1] {
			return nil, errors.New("consolidation lineage claim is outside caller scope")
		}
	}
	request.SessionID, request.TaskID = caller.Lineage.SessionID, caller.Lineage.TaskID
	request.SubtaskID, request.ProjectID = caller.Lineage.SubtaskID, caller.Lineage.ProjectID
	result, err := selected.ConsolidateObservations(ctx, request)
	if err != nil {
		return nil, err
	}
	result["workspace_id"], result["backend"] = caller.Identity.WorkspaceID, caller.Identity.Backend
	return result, nil
}

func dispatchCommitWorkingCheckpoint(ctx context.Context, selected *engine.Engine,
	caller Caller, args map[string]any) (map[string]any, error) {
	if caller.Role != "supervisor" {
		return nil, errors.New("commit_working_checkpoint requires the supervisor role")
	}
	if !validArguments(args, "checkpoint", "verification_report") {
		return nil, errors.New("unknown commit_working_checkpoint argument")
	}
	var request engine.CommitWorkingCheckpointRequest
	if err := decodeActiveMaskArgs(args, &request); err != nil {
		return nil, errors.New("commit_working_checkpoint arguments are invalid")
	}
	checkpoint := request.Checkpoint
	if checkpoint.SessionID != caller.Lineage.SessionID || checkpoint.TaskID == "" ||
		checkpoint.TaskID != caller.Lineage.TaskID || checkpoint.ProjectID != caller.Lineage.ProjectID {
		return nil, errors.New("working checkpoint is outside host-bound scope")
	}
	result, err := selected.CommitWorkingCheckpoint(ctx, request)
	if err != nil {
		return nil, err
	}
	result["workspace_id"], result["backend"] = caller.Identity.WorkspaceID, caller.Identity.Backend
	return result, nil
}

func dispatchObserve(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if caller.Role != "host_hook" {
		return nil, errors.New("observe requires the host_hook role")
	}
	if !validArguments(args, "event_id", "event_kind", "raw_evidence", "observed_at",
		"producer_id", "atomic_claim", "source_path", "source_uri", "artifact_hash",
		"tool_name", "workspace_root", "changed_files", "tags", "speaker", "source_occurred_at",
		"operation_id") {
		return nil, errors.New("unknown observe argument")
	}
	operationID, ok := args["operation_id"].(string)
	if !ok || operationID == "" {
		return nil, errors.New("observe requires a bound operation_id")
	}
	toolArgs := cloneArguments(args)
	delete(toolArgs, "operation_id")
	var observation engine.HostObservation
	if err := decodeActiveMaskArgs(toolArgs, &observation); err != nil {
		return nil, errors.New("observe arguments are invalid")
	}
	lineage := engine.HostLineage{SessionID: caller.Lineage.SessionID,
		TaskID: caller.Lineage.TaskID, SubtaskID: caller.Lineage.SubtaskID,
		ProjectID: caller.Lineage.ProjectID}
	return selected.ObserveHostFor(ctx, observation, lineage, operationID, caller.Role, ProtocolVersion)
}

func decodeActiveMaskArgs(args map[string]any, target any) error {
	toolArgs := make(map[string]any, len(args))
	for key, value := range args {
		if key != "repo_root" && key != "workspace_id" && key != "backend" {
			toolArgs[key] = value
		}
	}
	encoded, err := json.Marshal(toolArgs)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("active mask arguments are invalid")
	}
	return nil
}

func dispatchSetActiveMask(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "selected_block_ids", "token_budget", "activation_reason",
		"context_refs", "pinned_record_ids", "selectors", "priorities", "block_token_budgets", "expires_at") {
		return nil, errors.New("unknown set_active_mask argument")
	}
	var parsed mcpserver.SetActiveMaskArgs
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, err
	}
	return mcpserver.SetActiveMaskPayload(ctx, selected, parsed, caller.Lineage.SessionID, caller.Lineage.TaskID)
}

func dispatchAmendActiveMask(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "mask_id", "expected_revision", "activation_reason", "selected_block_ids",
		"context_refs", "pinned_record_ids", "selectors", "priorities", "token_budget",
		"block_token_budgets", "expires_at") {
		return nil, errors.New("unknown amend_active_mask argument")
	}
	var parsed mcpserver.AmendActiveMaskArgs
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, err
	}
	return mcpserver.AmendActiveMaskPayload(ctx, selected, parsed,
		caller.Lineage.SessionID, caller.Lineage.TaskID)
}

func dispatchTransitionActiveMask(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "mask_id", "expected_revision", "transition", "reason") {
		return nil, errors.New("unknown transition_active_mask argument")
	}
	var parsed mcpserver.TransitionActiveMaskArgs
	if err := decodeActiveMaskArgs(args, &parsed); err != nil {
		return nil, err
	}
	return mcpserver.TransitionActiveMaskPayload(ctx, selected, parsed,
		caller.Lineage.SessionID, caller.Lineage.TaskID)
}

func dispatchGetActiveMask(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "mask_id") {
		return nil, errors.New("unknown get_active_mask argument")
	}
	id, ok := args["mask_id"].(string)
	if args["mask_id"] != nil && !ok {
		return nil, errors.New("mask_id must be a string")
	}
	mask, err := selected.GetActiveMask(ctx, id, caller.Lineage.SessionID, caller.Lineage.TaskID)
	if err != nil {
		return nil, err
	}
	if mask != nil && (mask.SessionID != caller.Lineage.SessionID || mask.TaskID != caller.Lineage.TaskID) {
		mask = nil
	}
	return map[string]any{"ok": true, "mask": mask}, nil
}

func dispatchResolveActiveMask(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "mask_id", "expected_revision", "max_tokens", "scope_chain") {
		return nil, errors.New("unknown resolve_active_mask argument")
	}
	requested := []string{}
	if raw, exists := args["scope_chain"]; exists && raw != nil {
		values, ok := raw.([]any)
		if !ok {
			return nil, errors.New("scope chain is invalid")
		}
		for _, value := range values {
			key, ok := value.(string)
			if !ok {
				return nil, errors.New("scope chain is invalid")
			}
			requested = append(requested, key)
		}
	}
	bound, err := boundScopeChain(caller.Lineage, requested)
	if err != nil {
		return nil, err
	}
	toolArgs := make(map[string]any, len(args))
	for key, value := range args {
		if key != "repo_root" && key != "workspace_id" && key != "backend" && key != "scope_chain" {
			toolArgs[key] = value
		}
	}
	encoded, err := json.Marshal(toolArgs)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var parsed mcpserver.ResolveActiveMaskArgs
	if err := decoder.Decode(&parsed); err != nil {
		return nil, errors.New("resolve_active_mask arguments are invalid")
	}
	return mcpserver.ActiveMaskPayload(ctx, selected, parsed,
		caller.Identity.WorkspaceID, caller.Identity.Backend,
		caller.Lineage.SessionID, caller.Lineage.TaskID, bound)
}

func dispatchRemember(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "content", "requested_block_id", "operation", "memory_id",
		"expected_revision", "reason", "repair_of", "write_mode", "operation_id", "title", "kind", "tags", "metadata", "source_segments",
		"evidence_refs", "evidence_kind", "source_id", "sensitivity", "content_mode",
		"operational_risk", "run_id", "stage_id", "artifact_id", "artifact_revision",
		"valid_from", "valid_to", "repository_id", "checkout", "worktree", "branch",
		"revision", "dirty_state", "project_id", "task_id", "subtask_id",
		"parent_session_id", "user_id") {
		return nil, errors.New("unknown or unsupported remember argument")
	}
	toolArgs := make(map[string]any, len(args))
	for key, value := range args {
		if key != "repo_root" && key != "workspace_id" && key != "backend" {
			toolArgs[key] = value
		}
	}
	encoded, err := json.Marshal(toolArgs)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var parsed mcpserver.RememberArgs
	if err := decoder.Decode(&parsed); err != nil {
		return nil, errors.New("remember arguments are invalid")
	}
	if parsed.ParentSessionID != "" {
		return nil, errors.New("remember parent_session_id requires trusted parent lineage")
	}
	for _, claim := range []struct{ provided, trusted string }{
		{parsed.RunID, caller.Lineage.SessionID}, {parsed.ProjectID, caller.Lineage.ProjectID},
		{parsed.TaskID, caller.Lineage.TaskID}, {parsed.SubtaskID, caller.Lineage.SubtaskID},
		{parsed.UserID, caller.Lineage.UserID},
	} {
		if claim.provided != "" && claim.provided != claim.trusted {
			return nil, errors.New("remember scope claim is not bound to caller lineage")
		}
	}
	parsed.RunID = caller.Lineage.SessionID
	parsed.SessionID = caller.Lineage.SessionID
	parsed.ProjectID = caller.Lineage.ProjectID
	parsed.TaskID = caller.Lineage.TaskID
	parsed.SubtaskID = caller.Lineage.SubtaskID
	parsed.UserID = caller.Lineage.UserID
	return mcpserver.RememberPayload(ctx, selected, parsed, caller.Identity.WorkspaceID,
		caller.Identity.Backend, caller.Role, ProtocolVersion)
}

func dispatchListRecords(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "scope", "scope_id", "logical_block_id", "limit", "cursor", "include_retracted") {
		return nil, errors.New("unknown list_records argument")
	}
	toolArgs := make(map[string]any, len(args))
	for key, value := range args {
		if key != "repo_root" && key != "workspace_id" && key != "backend" {
			toolArgs[key] = value
		}
	}
	encoded, err := json.Marshal(toolArgs)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var parsed mcpserver.ListRecordsArgs
	if err := decoder.Decode(&parsed); err != nil {
		return nil, errors.New("list_records arguments are invalid")
	}
	if _, err := boundScopeChain(caller.Lineage, []string{parsed.Scope + ":" + parsed.ScopeID}); err != nil {
		return nil, err
	}
	limit := 32
	if parsed.Limit != nil {
		limit = *parsed.Limit
	}
	page, err := selected.ListRecords(ctx, engine.ListRecordsRequest{Scope: parsed.Scope,
		ScopeID: parsed.ScopeID, LogicalBlockID: parsed.LogicalBlockID, Limit: limit,
		Cursor: parsed.Cursor, IncludeRetracted: parsed.IncludeRetracted})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "records": page.Records, "next_cursor": page.NextCursor,
		"snapshot_count": page.SnapshotCount, "scope": page.Scope, "scope_id": page.ScopeID,
		"logical_block_id": page.LogicalBlockID, "workspace_id": caller.Identity.WorkspaceID,
		"backend": caller.Identity.Backend}, nil
}

func dispatchRecent(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "limit", "since", "scope", "scope_id", "logical_block_id", "include_candidates") {
		return nil, errors.New("unknown recent argument")
	}
	request := engine.RecentRequest{Limit: 8, Scope: "workspace", IncludeCandidates: true}
	if raw, exists := args["limit"]; exists {
		number, ok := raw.(json.Number)
		if !ok {
			return nil, errors.New("recent_records limit must be 1-128")
		}
		value, err := strconv.Atoi(number.String())
		if err != nil {
			return nil, errors.New("recent_records limit must be 1-128")
		}
		request.Limit = value
	}
	for key, target := range map[string]*string{"since": &request.Since, "scope": &request.Scope,
		"scope_id": &request.ScopeID, "logical_block_id": &request.LogicalBlockID} {
		if raw, exists := args[key]; exists && raw != nil {
			value, ok := raw.(string)
			if !ok {
				return nil, errors.New("recent arguments are invalid")
			}
			*target = value
		}
	}
	if raw, exists := args["include_candidates"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return nil, errors.New("recent include_candidates must be a boolean")
		}
		request.IncludeCandidates = value
	}
	if request.ScopeID == "" && request.Scope == "workspace" {
		request.ScopeID = caller.Identity.WorkspaceID
	}
	if _, err := boundScopeChain(caller.Lineage, []string{request.Scope + ":" + request.ScopeID}); err != nil {
		return nil, err
	}
	records, err := selected.Recent(ctx, request)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "records": records, "count": len(records),
		"scope": request.Scope, "scope_id": request.ScopeID, "logical_block_id": request.LogicalBlockID,
		"since": request.Since, "workspace_id": caller.Identity.WorkspaceID,
		"backend": caller.Identity.Backend}, nil
}

func dispatchRecall(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	parsed, bound, _, err := parseBoundRecallArgs(caller, args, false)
	if err != nil {
		return nil, err
	}
	if args["scope_chain"] != nil &&
		!slices.Contains(bound, "user_global:"+caller.Lineage.UserID) {
		parsed.IncludeManager = false
	}
	return mcpserver.RecallPayloadForUser(ctx, selected, parsed, caller.Identity.WorkspaceID,
		caller.Identity.Backend, bound, caller.Lineage.SessionID, caller.Lineage.UserID)
}

func dispatchBuildContext(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	parsed, bound, render, err := parseBoundRecallArgs(caller, args, true)
	if err != nil {
		return nil, err
	}
	if args["scope_chain"] != nil &&
		!slices.Contains(bound, "user_global:"+caller.Lineage.UserID) {
		parsed.IncludeManager = false
	}
	return mcpserver.BuildContextPayloadForUser(ctx, selected, parsed, caller.Identity.WorkspaceID,
		caller.Identity.Backend, bound, caller.Lineage.SessionID, caller.Lineage.UserID, render)
}

func dispatchGrantedContext(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "limit", "source_horizon", "include_candidates", "max_tokens",
		"logical_block_ids", "record_ids", "scope_chain", "run_id", "render_context",
		"active_mask_id", "active_mask_revision", "selected_records_only") {
		return nil, errors.New("unknown build_granted_context argument")
	}
	scopeArgs := make(map[string]any, len(args))
	grantArgs := make(map[string]any, len(args))
	for key, value := range args {
		if key != "active_mask_id" && key != "active_mask_revision" && key != "selected_records_only" {
			scopeArgs[key] = value
		}
		if key != "scope_chain" && key != "run_id" && key != "render_context" {
			grantArgs[key] = value
		}
	}
	_, bound, render, err := parseBoundRecallArgs(caller, scopeArgs, true)
	if err != nil {
		return nil, err
	}
	var parsed mcpserver.GrantedContextArgs
	if err := decodeActiveMaskArgs(grantArgs, &parsed); err != nil {
		return nil, errors.New("granted context arguments are invalid")
	}
	return mcpserver.GrantedContextPayload(ctx, selected, parsed,
		caller.Identity.WorkspaceID, caller.Identity.Backend, bound,
		caller.Lineage.SessionID, caller.Lineage.TaskID, render)
}

func parseBoundRecallArgs(caller Caller, args map[string]any, allowRender bool) (mcpserver.RecallArgs, []string, bool, error) {
	invalid := "unknown recall argument"
	if allowRender {
		invalid = "unknown build_context argument"
	}
	if !validArguments(args, "query", "limit", "tags", "source_horizon", "include_candidates",
		"logical_block_ids", "search_all", "retrieval_mode", "record_ids", "max_tokens",
		"source_speakers", "temporal_mode", "as_of", "known_as_of", "revision_id",
		"include_manager", "scope_chain", "run_id", "render_context") || !allowRender && args["render_context"] != nil {
		return mcpserver.RecallArgs{}, nil, false, errors.New(invalid)
	}
	requested := []string{}
	if raw, exists := args["scope_chain"]; exists && raw != nil {
		values, ok := raw.([]any)
		if !ok {
			return mcpserver.RecallArgs{}, nil, false, errors.New("scope chain is invalid")
		}
		for _, rawValue := range values {
			value, ok := rawValue.(string)
			if !ok {
				return mcpserver.RecallArgs{}, nil, false, errors.New("scope chain is invalid")
			}
			requested = append(requested, value)
		}
	}
	bound, err := boundScopeChain(caller.Lineage, requested)
	if err != nil {
		return mcpserver.RecallArgs{}, nil, false, err
	}
	if raw, exists := args["run_id"]; exists && raw != nil && raw != "" && raw != caller.Lineage.SessionID {
		return mcpserver.RecallArgs{}, nil, false, errors.New("recall run_id is not bound to caller session")
	}
	render := false
	if raw, exists := args["render_context"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return mcpserver.RecallArgs{}, nil, false, errors.New("render_context must be a boolean")
		}
		render = value
	}
	toolArgs := make(map[string]any, len(args))
	for key, value := range args {
		if key != "repo_root" && key != "workspace_id" && key != "backend" && key != "scope_chain" && key != "run_id" && key != "render_context" {
			toolArgs[key] = value
		}
	}
	encoded, err := json.Marshal(toolArgs)
	if err != nil {
		return mcpserver.RecallArgs{}, nil, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var parsed mcpserver.RecallArgs
	if err := decoder.Decode(&parsed); err != nil {
		return mcpserver.RecallArgs{}, nil, false, errors.New("recall arguments are invalid")
	}
	return parsed, bound, render, nil
}

func validArguments(args map[string]any, fields ...string) bool {
	allowed := map[string]bool{"repo_root": true, "workspace_id": true, "backend": true}
	for _, field := range fields {
		allowed[field] = true
	}
	for field := range args {
		if !allowed[field] {
			return false
		}
	}
	return true
}

func dispatchExactRecord(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "record_id", "evidence_ref") {
		return nil, errors.New("unknown read_exact_record argument")
	}
	id, idOK := args["record_id"].(string)
	ref, refOK := args["evidence_ref"].(string)
	if (args["record_id"] != nil && !idOK) || (args["evidence_ref"] != nil && !refOK) ||
		(strings.TrimSpace(id) == "") == (strings.TrimSpace(ref) == "") {
		return nil, errors.New("read_exact_record requires exactly one record_id or evidence_ref")
	}
	var record *memory.MemoryRecord
	var err error
	if strings.TrimSpace(id) != "" {
		record, err = selected.ReadStored(ctx, strings.TrimSpace(id))
	} else {
		record, err = selected.ReadEvidence(ctx, strings.TrimSpace(ref))
	}
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, errors.New("exact record was not found")
	}
	return map[string]any{"ok": true, "record": record,
		"workspace_id": caller.Identity.WorkspaceID, "backend": caller.Identity.Backend}, nil
}

func dispatchExactRecords(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "record_ids") {
		return nil, errors.New("unknown read_exact_records argument")
	}
	ids, ok := args["record_ids"].([]any)
	if !ok || len(ids) < 1 || len(ids) > 128 {
		return nil, errors.New("read_exact_records requires 1-128 distinct record IDs")
	}
	seen := make(map[string]bool, len(ids))
	records := make([]memory.MemoryRecord, 0, len(ids))
	for _, raw := range ids {
		id, ok := raw.(string)
		if !ok || strings.TrimSpace(id) == "" || seen[id] {
			return nil, errors.New("read_exact_records requires 1-128 distinct record IDs")
		}
		seen[id] = true
		record, err := selected.ReadStored(ctx, id)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, errors.New("exact record was not found")
		}
		records = append(records, *record)
	}
	return map[string]any{"ok": true, "records": records,
		"workspace_id": caller.Identity.WorkspaceID, "backend": caller.Identity.Backend}, nil
}

func dispatchOperationStatus(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "operation_id") {
		return nil, errors.New("unknown operation_status argument")
	}
	id, ok := args["operation_id"].(string)
	if !ok {
		return nil, errors.New("operation_status requires operation_id")
	}
	result, err := selected.OperationStatusFor(ctx, id, caller.Role, ProtocolVersion)
	if err != nil {
		return nil, err
	}
	result["workspace_id"] = caller.Identity.WorkspaceID
	result["backend"] = caller.Identity.Backend
	return result, nil
}

func dispatchProjectionBarrier(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "record_id", "commit_revision", "timeout_seconds") {
		return nil, errors.New("unknown projection_barrier argument")
	}
	id, ok := args["record_id"].(string)
	if !ok || strings.TrimSpace(id) == "" {
		return nil, errors.New("projection_barrier requires record_id")
	}
	var revision int64
	if raw, exists := args["commit_revision"]; exists {
		number, ok := raw.(json.Number)
		if !ok {
			return nil, errors.New("projection_barrier requires a non-negative commit_revision")
		}
		var err error
		revision, err = number.Int64()
		if err != nil || revision < 0 {
			return nil, errors.New("projection_barrier requires a non-negative commit_revision")
		}
	}
	timeout := 30.0
	if raw, exists := args["timeout_seconds"]; exists {
		number, ok := raw.(json.Number)
		if !ok {
			return nil, errors.New("projection_barrier requires a finite non-negative timeout")
		}
		var err error
		timeout, err = number.Float64()
		if err != nil {
			return nil, errors.New("projection_barrier requires a finite non-negative timeout")
		}
	}
	result, err := selected.ProjectionBarrier(ctx, id, revision, timeout)
	if err != nil {
		return nil, err
	}
	result["workspace_id"] = caller.Identity.WorkspaceID
	result["backend"] = caller.Identity.Backend
	return result, nil
}

func dispatchWorkingSet(ctx context.Context, selected *engine.Engine, caller Caller, args map[string]any) (map[string]any, error) {
	if !validArguments(args, "owner_id", "generation_id", "record_ids", "max_tokens", "max_bytes", "scope_chain") {
		return nil, errors.New("unknown read_working_set argument")
	}
	owner, ownerOK := args["owner_id"].(string)
	generation, generationOK := args["generation_id"].(string)
	rawIDs, idsOK := args["record_ids"].([]any)
	if !ownerOK || !generationOK || !idsOK {
		return nil, errors.New("read_working_set requires owner, generation and record IDs")
	}
	ids := make([]string, 0, len(rawIDs))
	for _, raw := range rawIDs {
		id, ok := raw.(string)
		if !ok {
			return nil, errors.New("working set record IDs must be nonempty strings")
		}
		ids = append(ids, id)
	}
	parseBudget := func(field string, fallback int) (int, error) {
		raw, exists := args[field]
		if !exists {
			return fallback, nil
		}
		number, ok := raw.(json.Number)
		if !ok {
			return 0, errors.New("working set budgets must be positive integers")
		}
		value, err := number.Int64()
		if err != nil || value < 1 || value > int64(^uint(0)>>1) {
			return 0, errors.New("working set budgets must be positive integers")
		}
		return int(value), nil
	}
	maxTokens, err := parseBudget("max_tokens", 2048)
	if err != nil {
		return nil, err
	}
	maxBytes, err := parseBudget("max_bytes", 32768)
	if err != nil {
		return nil, err
	}
	requested := []string{}
	if raw, exists := args["scope_chain"]; exists {
		values, ok := raw.([]any)
		if !ok {
			return nil, errors.New("scope chain is invalid")
		}
		for _, entry := range values {
			value, ok := entry.(string)
			if !ok {
				return nil, errors.New("scope chain is invalid")
			}
			requested = append(requested, value)
		}
	}
	chain, err := boundScopeChain(caller.Lineage, requested)
	if err != nil {
		return nil, err
	}
	working, err := selected.ReadWorkingSet(ctx, owner, generation, ids, maxTokens, maxBytes, chain)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "records": working.Records,
		"rendered_context": working.RenderedContext, "token_budget": working.TokenBudget,
		"estimated_tokens": working.EstimatedTokens, "byte_budget": working.ByteBudget,
		"rendered_bytes": working.RenderedBytes, "truncated": working.Truncated,
		"omitted_record_ids":     working.OmittedRecordIDs,
		"unavailable_record_ids": working.UnavailableRecordIDs,
		"token_accounting":       working.TokenAccounting, "visibility": working.Visibility}, nil
}
