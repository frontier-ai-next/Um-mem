package mcpserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// GrantedContextArgs is accepted only by the authenticated local service.
// The host binds workspace, profile, session and scope outside this payload.
type GrantedContextArgs struct {
	Limit               *int     `json:"limit,omitempty"`
	SourceHorizon       string   `json:"source_horizon,omitempty"`
	IncludeCandidates   bool     `json:"include_candidates,omitempty"`
	MaxTokens           *int     `json:"max_tokens,omitempty"`
	LogicalBlockIDs     []string `json:"logical_block_ids"`
	RecordIDs           []string `json:"record_ids,omitempty"`
	ActiveMaskID        string   `json:"active_mask_id,omitempty"`
	ActiveMaskRevision  *int     `json:"active_mask_revision,omitempty"`
	SelectedRecordsOnly bool     `json:"selected_records_only,omitempty"`
}

func uniqueNonempty(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result
}

// GrantedContextPayload packs only records assigned to exact host-granted
// blocks. It never calls selected-backend semantic recall.
func GrantedContextPayload(ctx context.Context, selected *engine.Engine,
	args GrantedContextArgs, workspaceID, backendID string, scopeChain []string,
	sessionID, taskID string, renderContext bool) (map[string]any, error) {
	blocks := uniqueNonempty(args.LogicalBlockIDs)
	if len(blocks) == 0 {
		return nil, errors.New("granted context requires exact logical_block_ids")
	}
	grantedIDs := uniqueNonempty(args.RecordIDs)
	assignedIDs, err := selected.RecordIDsForLogicalBlocks(ctx, blocks)
	if err != nil {
		return nil, err
	}
	assigned := make(map[string]bool, len(assignedIDs))
	for _, id := range assignedIDs {
		assigned[id] = true
	}
	selectedIDs := make([]string, 0, len(assignedIDs))
	if len(grantedIDs) > 0 || args.SelectedRecordsOnly {
		for _, id := range grantedIDs {
			if assigned[id] {
				selectedIDs = append(selectedIDs, id)
			}
		}
	} else {
		selectedIDs = append(selectedIDs, assignedIDs...)
	}
	activeStatus := "none"
	semanticRecall := false
	if args.ActiveMaskID != "" {
		if args.ActiveMaskRevision == nil {
			return nil, errors.New("granted active context requires an exact mask revision")
		}
		resolution, err := selected.ResolveActiveMask(ctx, engine.ActiveMaskRequest{
			MaskID: args.ActiveMaskID, ExpectedRevision: args.ActiveMaskRevision,
			MaxTokens: args.MaxTokens, SessionID: sessionID, TaskID: taskID,
			ScopeChain: scopeChain})
		if err != nil {
			return nil, err
		}
		activeStatus, semanticRecall = resolution.Status, resolution.SemanticRecallPerformed
		allowed := make(map[string]bool, len(selectedIDs))
		if args.SelectedRecordsOnly {
			for id := range assigned {
				allowed[id] = true
			}
		} else {
			for _, id := range selectedIDs {
				allowed[id] = true
			}
		}
		ordered := make([]string, 0, len(resolution.RecordIDs)+len(selectedIDs))
		if activeStatus == "resolved" {
			for _, id := range resolution.RecordIDs {
				if allowed[id] && !slices.Contains(ordered, id) {
					ordered = append(ordered, id)
				}
			}
		}
		for _, id := range selectedIDs {
			if !slices.Contains(ordered, id) {
				ordered = append(ordered, id)
			}
		}
		selectedIDs = ordered
	}
	_, profileID := selected.Scope()
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID, plan.RunID = workspaceID, profileID, sessionID
	plan.ScopeChain, plan.LogicalBlockIDs = scopeChain, blocks
	plan.IncludeCandidates = args.IncludeCandidates
	plan.SearchAll = false
	plan.SourceHorizon = "durable"
	if args.SourceHorizon != "" {
		plan.SourceHorizon = args.SourceHorizon
	}
	plan.MaxTokens = 2048
	if args.MaxTokens != nil {
		plan.MaxTokens = *args.MaxTokens
	}
	plan.Limit = 8
	if args.Limit != nil {
		plan.Limit = max(0, *args.Limit)
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	records := make([]memory.MemoryRecord, 0, min(plan.Limit, len(selectedIDs)))
	for _, id := range selectedIDs {
		if len(records) >= plan.Limit {
			break
		}
		record, err := selected.ReadStored(ctx, id)
		if err != nil {
			return nil, err
		}
		if record == nil || !assigned[id] ||
			!memory.EligibleForRecall(*record, plan, time.Now().UTC()) {
			continue
		}
		if record.DeliveryPolicy != "always_on" && record.DeliveryPolicy != "working_set" &&
			record.DeliveryPolicy != "on_demand" {
			continue
		}
		records = append(records, *record)
	}
	raw := make([]map[string]any, 0, len(records))
	for _, record := range records {
		raw = append(raw, rawRecallItem(record, true))
	}
	packed, err := memory.PackEvidencePayload(raw, memory.PackOptions{MaxTokens: plan.MaxTokens})
	if err != nil {
		return nil, err
	}
	renderedIDs := make([]string, 0, len(packed.Items))
	for _, item := range packed.Items {
		metadata, _ := item["metadata"].(map[string]any)
		id, _ := metadata["block_id"].(string)
		if id != "" {
			renderedIDs = append(renderedIDs, id)
		}
	}
	diagnostics := map[string]any{
		"native_path": "exact_grant_index", "semantic_recall_performed": semanticRecall,
		"selection_mode":     map[bool]string{true: "selected_only", false: "granted_blocks"}[args.SelectedRecordsOnly],
		"active_mask_status": activeStatus, "active_mask_id": args.ActiveMaskID,
		"active_mask_revision":        args.ActiveMaskRevision,
		"requested_logical_block_ids": blocks, "requested_record_ids": grantedIDs,
		"assigned_record_count": len(assignedIDs), "selected_record_count": len(records),
		"rendered_record_ids": renderedIDs, "requested_context_token_budget": plan.MaxTokens,
		"allocated_context_token_budget":   plan.MaxTokens,
		"truncated_item_count":             packed.TruncatedItemCount,
		"context_char_budget":              packed.TokenBudget * 4,
		"context_token_budget":             packed.TokenBudget,
		"context_payload_chars":            len([]rune(packed.RenderedContext)),
		"context_payload_estimated_tokens": packed.EstimatedTokens,
		"metadata_overhead_tokens":         packed.MetadataTokens,
		"exact_literal_capsule":            packed.ExactLiteralCapsule,
		"exact_literal_losses":             packed.ExactLiteralLosses,
		"visibility":                       packed.Visibility,
	}
	result := map[string]any{"ok": true, "workspace_id": workspaceID,
		"run_id": sessionID, "backend": backendID,
		"delivery_policy": "start_grant", "authority": "advisory",
		"diagnostics": diagnostics, "items": packed.Items}
	if renderContext {
		result["rendered_context"] = packed.RenderedContext
	}
	return result, nil
}
