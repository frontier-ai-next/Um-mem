package mcpserver

import (
	"context"
	"errors"
	"os"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type GetActiveMaskArgs struct {
	MaskID string `json:"mask_id,omitempty"`
}

type ResolveActiveMaskArgs struct {
	MaskID           string   `json:"mask_id,omitempty"`
	ExpectedRevision *int     `json:"expected_revision,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	ScopeChain       []string `json:"scope_chain,omitempty"`
}

type ActiveMaskSelectorArgs struct {
	BlockID        string   `json:"block_id"`
	SelectorType   string   `json:"selector_type"`
	RecordIDs      []string `json:"record_ids,omitempty"`
	Query          string   `json:"query,omitempty"`
	Limit          *int     `json:"limit,omitempty"`
	WorkingStateID string   `json:"working_state_id,omitempty"`
}

type SetActiveMaskArgs struct {
	SelectedBlockIDs  []string                         `json:"selected_block_ids"`
	TokenBudget       int                              `json:"token_budget"`
	ActivationReason  string                           `json:"activation_reason"`
	ContextRefs       []string                         `json:"context_refs,omitempty"`
	PinnedRecordIDs   []string                         `json:"pinned_record_ids,omitempty"`
	Selectors         []ActiveMaskSelectorArgs         `json:"selectors,omitempty"`
	Priorities        []memory.ActiveMemoryPriority    `json:"priorities,omitempty"`
	BlockTokenBudgets []memory.ActiveMemoryBlockBudget `json:"block_token_budgets,omitempty"`
	ExpiresAt         string                           `json:"expires_at,omitempty"`
}

type TransitionActiveMaskArgs struct {
	MaskID           string `json:"mask_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Transition       string `json:"transition"`
	Reason           string `json:"reason"`
}

type AmendActiveMaskArgs struct {
	MaskID            string                            `json:"mask_id"`
	ExpectedRevision  int                               `json:"expected_revision"`
	ActivationReason  string                            `json:"activation_reason"`
	SelectedBlockIDs  *[]string                         `json:"selected_block_ids,omitempty"`
	ContextRefs       *[]string                         `json:"context_refs,omitempty"`
	PinnedRecordIDs   *[]string                         `json:"pinned_record_ids,omitempty"`
	Selectors         *[]ActiveMaskSelectorArgs         `json:"selectors,omitempty"`
	Priorities        *[]memory.ActiveMemoryPriority    `json:"priorities,omitempty"`
	TokenBudget       *int                              `json:"token_budget,omitempty"`
	BlockTokenBudgets *[]memory.ActiveMemoryBlockBudget `json:"block_token_budgets,omitempty"`
	ExpiresAt         *string                           `json:"expires_at,omitempty"`
}

func convertMaskSelectors(input []ActiveMaskSelectorArgs) []memory.ActiveMemorySelector {
	selectors := make([]memory.ActiveMemorySelector, 0, len(input))
	for _, item := range input {
		limit := 8
		if item.Limit != nil {
			limit = *item.Limit
		}
		selectors = append(selectors, memory.ActiveMemorySelector{BlockID: item.BlockID,
			SelectorType: item.SelectorType, RecordIDs: item.RecordIDs,
			Query: item.Query, Limit: limit, WorkingStateID: item.WorkingStateID})
	}
	return selectors
}

func SetActiveMaskPayload(ctx context.Context, selected *engine.Engine, args SetActiveMaskArgs,
	sessionID, taskID string) (map[string]any, error) {
	mask, err := selected.SetActiveMask(ctx, memory.ActiveMemoryMask{
		SelectedBlockIDs: args.SelectedBlockIDs, TokenBudget: args.TokenBudget,
		ActivationReason: args.ActivationReason, ContextRefs: args.ContextRefs,
		PinnedRecordIDs: args.PinnedRecordIDs, Selectors: convertMaskSelectors(args.Selectors),
		Priorities: args.Priorities, BlockTokenBudgets: args.BlockTokenBudgets,
		ExpiresAt: args.ExpiresAt}, sessionID, taskID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "mask": mask}, nil
}

func AmendActiveMaskPayload(ctx context.Context, selected *engine.Engine, args AmendActiveMaskArgs,
	sessionID, taskID string) (map[string]any, error) {
	change := catalog.MaskAmendment{MaskID: args.MaskID, ExpectedRevision: args.ExpectedRevision,
		SessionID: sessionID, TaskID: taskID, ActivationReason: args.ActivationReason,
		SelectedBlockIDs: args.SelectedBlockIDs, ContextRefs: args.ContextRefs,
		PinnedRecordIDs: args.PinnedRecordIDs, Priorities: args.Priorities,
		TokenBudget: args.TokenBudget, BlockTokenBudgets: args.BlockTokenBudgets,
		ExpiresAt: args.ExpiresAt}
	if args.Selectors != nil {
		converted := convertMaskSelectors(*args.Selectors)
		change.Selectors = &converted
	}
	mask, err := selected.AmendActiveMask(ctx, change)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "mask": mask}, nil
}

func TransitionActiveMaskPayload(ctx context.Context, selected *engine.Engine, args TransitionActiveMaskArgs,
	sessionID, taskID string) (map[string]any, error) {
	mask, err := selected.TransitionActiveMask(ctx, args.MaskID, args.ExpectedRevision,
		args.Transition, args.Reason, sessionID, taskID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "mask": mask}, nil
}

func boundHostScopeChain(workspaceID string, lineage map[string]string) []string {
	bound := []string{}
	for _, entry := range []struct{ kind, id string }{
		{"subtask", lineage["subtask_id"]}, {"task", lineage["task_id"]},
		{"session", lineage["session_id"]}, {"workspace", workspaceID},
		{"project", lineage["project_id"]}, {"user_global", lineage["user_id"]},
	} {
		if entry.id != "" {
			bound = append(bound, entry.kind+":"+entry.id)
		}
	}
	return bound
}

func hostActiveScopeChain(workspaceID string, lineage map[string]string, requested []string) ([]string, error) {
	bound := boundHostScopeChain(workspaceID, lineage)
	if os.Getenv("UM_MEM_ACTIVE_SCOPE_PROJECT_ONLY") == "1" {
		if lineage["project_id"] == "" {
			return nil, errors.New("project-only active memory requires host project identity")
		}
		bound = []string{"project:" + lineage["project_id"]}
	}
	if requested == nil {
		return bound, nil
	}
	allowed := map[string]int{}
	for i, key := range bound {
		allowed[key] = i
	}
	seen := map[string]bool{}
	last := -1
	for _, key := range requested {
		rank, exists := allowed[key]
		kind, _, found := strings.Cut(key, ":")
		if !exists || !found || key != strings.TrimSpace(key) || seen[kind] || rank < last {
			return nil, errors.New("scope chain is not bound to MCP host lineage")
		}
		seen[kind], last = true, rank
	}
	if len(requested) == 0 {
		return nil, errors.New("active mask scope chain is empty")
	}
	return requested, nil
}

func ActiveMaskPayload(ctx context.Context, selected *engine.Engine, args ResolveActiveMaskArgs,
	workspaceID, backendID, sessionID, taskID string, scopeChain []string) (map[string]any, error) {
	resolved, err := selected.ResolveActiveMask(ctx, engine.ActiveMaskRequest{
		MaskID: args.MaskID, ExpectedRevision: args.ExpectedRevision, MaxTokens: args.MaxTokens,
		SessionID: sessionID, TaskID: taskID, ScopeChain: scopeChain})
	if err != nil {
		return nil, err
	}
	logicalIDs := []string{}
	maskID, maskRevision := "", 0
	if resolved.Mask != nil {
		logicalIDs = resolved.Mask.SelectedBlockIDs
		maskID, maskRevision = resolved.Mask.MaskID, resolved.Mask.Revision
	}
	raw := make([]map[string]any, 0, len(resolved.Records))
	for _, record := range resolved.Records {
		item := rawRecallItem(record, false)
		item["authority"] = "advisory"
		metadata := item["metadata"].(map[string]any)
		metadata["logical_block_id"] = record.BlockMembership.PrimaryBlockID
		raw = append(raw, item)
	}
	packed, err := memory.PackEvidencePayload(raw, memory.PackOptions{MaxTokens: resolved.TokenBudget})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(packed.Items))
	for _, packedItem := range packed.Items {
		metadata, _ := packedItem["metadata"].(map[string]any)
		if metadata == nil {
			metadata = map[string]any{}
		}
		sourceAuthority := metadata["source_authority"]
		if sourceAuthority == nil {
			sourceAuthority = metadata["authority"]
		}
		metadata["source_authority"] = sourceAuthority
		metadata["authority"] = "non_directive"
		id, _ := metadata["block_id"].(string)
		items = append(items, map[string]any{"block_id": id,
			"text": packedItem["text"], "source": packedItem["source"],
			"authority": "non_directive", "surface": "supplemental_evidence",
			"lifecycle": "active", "score": packedItem["score"],
			"tags": packedItem["tags"], "metadata": metadata})
	}
	leaseID, err := newOperationID()
	if err != nil {
		return nil, err
	}
	if len(leaseID) > 12 {
		leaseID = leaseID[:12]
	}
	trace := map[string]any{
		"mode": "active_mask", "backend_id": backendID,
		"plan_reason": "active_mask_materialization", "query_type": "exact_lookup",
		"requested_tokens": resolved.TokenBudget, "allocated_tokens": packed.TokenBudget,
		"estimated_tokens": packed.EstimatedTokens, "metadata_tokens": packed.MetadataTokens,
		"rendered_item_count": len(packed.Items), "truncated_item_count": packed.TruncatedItemCount,
		"logical_block_ids": logicalIDs, "record_ids": resolved.RecordIDs,
		"visibility": packed.Visibility, "exact_literal_capsule": packed.ExactLiteralCapsule,
		"exact_literal_losses": packed.ExactLiteralLosses, "corrective_recall": map[string]any{},
		"reverse_provenance": map[string]any{}, "event_type": "active_resolve",
		"mask_id": maskID, "mask_revision": maskRevision,
		"catalog_revision": resolved.CatalogRevision, "backend_revision": "",
		"refresh_reason":              resolved.RefreshReason,
		"excluded_lifecycle_statuses": []string{"superseded", "deprecated", "quarantined", "retracted", "expired"},
		"scope_validation_result":     resolved.ScopeValidationResult,
		"resolution_status":           resolved.Status,
		"semantic_recall_performed":   resolved.SemanticRecallPerformed,
		"out_of_prompt":               true,
	}
	lease := map[string]any{"query": "", "workspace_id": workspaceID, "run_id": sessionID,
		"logical_block_ids": logicalIDs, "items": items,
		"rendered_context": packed.RenderedContext, "token_budget": packed.TokenBudget,
		"estimated_tokens": packed.EstimatedTokens, "metadata_tokens": packed.MetadataTokens,
		"trace": trace, "lease_id": "lease_" + leaseID}
	return map[string]any{"ok": true, "lease": lease}, nil
}

func addActiveMaskTools(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string, lineage map[string]string) {
	mcp.AddTool(server, &mcp.Tool{Name: "amend_active_mask", Description: "Amend the current session's active focus with a revision check."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args AmendActiveMaskArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := AmendActiveMaskPayload(ctx, selected, args, lineage["session_id"], lineage["task_id"])
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "set_active_mask", Description: "Activate a durable, session-bound focus over selected catalog blocks."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args SetActiveMaskArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := SetActiveMaskPayload(ctx, selected, args, lineage["session_id"], lineage["task_id"])
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "transition_active_mask", Description: "Suspend, resume or deactivate the current session's active focus."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args TransitionActiveMaskArgs) (*mcp.CallToolResult, map[string]any, error) {
			payload, err := TransitionActiveMaskPayload(ctx, selected, args, lineage["session_id"], lineage["task_id"])
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "get_active_mask", Description: "Read the current session's durable active memory mask."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args GetActiveMaskArgs) (*mcp.CallToolResult, map[string]any, error) {
			mask, err := selected.GetActiveMask(ctx, args.MaskID, lineage["session_id"], lineage["task_id"])
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			if mask != nil && (mask.SessionID != lineage["session_id"] || mask.TaskID != lineage["task_id"]) {
				mask = nil
			}
			return nil, map[string]any{"ok": true, "mask": mask}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "resolve_active_mask", Description: "Resolve the current active focus into bounded, non-directive evidence."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ResolveActiveMaskArgs) (*mcp.CallToolResult, map[string]any, error) {
			chain, err := hostActiveScopeChain(workspaceID, lineage, args.ScopeChain)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			payload, err := ActiveMaskPayload(ctx, selected, args, workspaceID, backendID,
				lineage["session_id"], lineage["task_id"], chain)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
