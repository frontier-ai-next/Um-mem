package engine

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type ActiveMaskRequest struct {
	MaskID           string
	ExpectedRevision *int
	MaxTokens        *int
	SessionID        string
	TaskID           string
	ScopeChain       []string
}

type ActiveMaskResolution struct {
	Mask                    *memory.ActiveMemoryMask
	Records                 []memory.MemoryRecord
	RecordIDs               []string
	Status                  string
	ScopeValidationResult   string
	RefreshReason           string
	CatalogRevision         int
	TokenBudget             int
	SemanticRecallPerformed bool
}

func (engine *Engine) SetActiveMask(ctx context.Context, mask memory.ActiveMemoryMask, sessionID, taskID string) (memory.ActiveMemoryMask, error) {
	if sessionID == "" || mask.SessionID != "" || mask.TaskID != "" || mask.WorkspaceID != "" || mask.ProfileID != "" ||
		mask.MaskID != "" || mask.Revision != 0 || mask.Status != "" {
		return memory.ActiveMemoryMask{}, errors.New("active mask identity must be bound by the host")
	}
	mask.WorkspaceID, mask.ProfileID = engine.config.WorkspaceID, engine.config.ProfileID
	mask.SessionID, mask.TaskID = sessionID, taskID
	mask.Revision, mask.Status = 1, "active"
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return memory.ActiveMemoryMask{}, err
	}
	defer opened.Close()
	return opened.ActivateMask(ctx, mask)
}

func (engine *Engine) AmendActiveMask(ctx context.Context, change catalog.MaskAmendment) (memory.ActiveMemoryMask, error) {
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return memory.ActiveMemoryMask{}, err
	}
	defer opened.Close()
	return opened.AmendMaskForScope(ctx, change)
}

func (engine *Engine) TransitionActiveMask(ctx context.Context, maskID string, expectedRevision int,
	transition, reason, sessionID, taskID string) (memory.ActiveMemoryMask, error) {
	status := map[string]string{"suspend": "suspended", "resume": "active", "deactivate": "expired"}[transition]
	if status == "" {
		return memory.ActiveMemoryMask{}, errors.New("active mask transition is unsupported")
	}
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return memory.ActiveMemoryMask{}, err
	}
	defer opened.Close()
	return opened.TransitionMaskForScope(ctx, maskID, expectedRevision, status, reason, sessionID, taskID)
}

func (engine *Engine) GetActiveMask(ctx context.Context, maskID, sessionID, taskID string) (*memory.ActiveMemoryMask, error) {
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	if maskID != "" {
		return opened.GetMask(ctx, maskID)
	}
	return opened.CurrentMask(ctx, sessionID, taskID)
}

// ResolveActiveMask materializes the current canonical heads from the selected
// catalog blocks. The caller supplies only a host-bound scope chain.
func (engine *Engine) ResolveActiveMask(ctx context.Context, request ActiveMaskRequest) (ActiveMaskResolution, error) {
	result := ActiveMaskResolution{Records: []memory.MemoryRecord{}, RecordIDs: []string{},
		Status: "not_found", ScopeValidationResult: "matched"}
	if request.ExpectedRevision != nil && *request.ExpectedRevision < 1 {
		return result, errors.New("active mask expected revision must be positive")
	}
	if request.MaxTokens != nil && *request.MaxTokens < 0 {
		return result, errors.New("active mask token ceiling must be non-negative")
	}
	if len(request.ScopeChain) == 0 {
		return result, errors.New("active mask requires a host-bound scope chain")
	}
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return result, err
	}
	defer opened.Close()
	result.CatalogRevision, err = opened.CatalogRevision(ctx)
	if err != nil {
		return result, err
	}
	if request.MaskID != "" {
		result.Mask, err = opened.GetMask(ctx, request.MaskID)
	} else {
		result.Mask, err = opened.CurrentMask(ctx, request.SessionID, request.TaskID)
	}
	if err != nil || result.Mask == nil {
		return result, err
	}
	mask := result.Mask
	result.TokenBudget = mask.TokenBudget
	if request.MaxTokens != nil {
		result.TokenBudget = min(result.TokenBudget, *request.MaxTokens)
	}
	if mask.SessionID != request.SessionID || mask.TaskID != request.TaskID ||
		mask.WorkspaceID != engine.config.WorkspaceID || mask.ProfileID != engine.config.ProfileID {
		result.Status, result.ScopeValidationResult = "scope_mismatch", "scope_mismatch"
		return result, nil
	}
	if mask.Status != "active" {
		result.Status = mask.Status
		return result, nil
	}
	if request.ExpectedRevision != nil && mask.Revision != *request.ExpectedRevision {
		result.Status = "revision_mismatch"
		return result, nil
	}
	if mask.IsExpired(time.Now().UTC()) {
		result.Status = "expired"
		return result, nil
	}
	if result.TokenBudget == 0 {
		result.Status = "resolved"
		return result, nil
	}
	for _, blockID := range mask.SelectedBlockIDs {
		state, err := opened.GetBlock(ctx, blockID)
		if err != nil {
			return result, err
		}
		if state == nil || state.Status != "active" {
			result.Status, result.RefreshReason = "catalog_mismatch", "selected_block_missing"
			if state != nil {
				result.RefreshReason = "selected_block_inactive"
			}
			return result, nil
		}
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return result, err
	}
	byID := map[string]store.ProjectionRow{}
	currentByKey := map[string]store.ProjectionRow{}
	byBlock := map[string][]store.ProjectionRow{}
	allowedScopes := make(map[string]bool, len(request.ScopeChain))
	for _, key := range request.ScopeChain {
		allowedScopes[key] = true
	}
	for _, row := range rows {
		byID[row.Record.BlockID] = row
		if row.Current && row.Status == "committed" && row.Backend == engine.recordNativeStore(row.Record) &&
			row.Record.CanonicalKey != "" {
			currentByKey[row.Record.CanonicalKey] = row
		}
		if row.Record.BlockMembership != nil {
			blockID := row.Record.BlockMembership.PrimaryBlockID
			byBlock[blockID] = append(byBlock[blockID], row)
		}
	}
	selectors := mask.Selectors
	if len(selectors) == 0 {
		selectors = make([]memory.ActiveMemorySelector, 0, len(mask.SelectedBlockIDs))
		for _, blockID := range mask.SelectedBlockIDs {
			selectors = append(selectors, memory.ActiveMemorySelector{BlockID: blockID, SelectorType: "canonical_heads"})
		}
	}
	selectedIDs := append([]string{}, mask.PinnedRecordIDs...)
	for _, selector := range selectors {
		switch selector.SelectorType {
		case "pinned_records":
			selectedIDs = append(selectedIDs, selector.RecordIDs...)
		case "backend_query":
			plan := memory.NewRecallPlan(selector.Query)
			plan.WorkspaceID, plan.ProfileID = engine.config.WorkspaceID, engine.config.ProfileID
			plan.RunID, plan.ScopeChain = request.SessionID, request.ScopeChain
			plan.LogicalBlockIDs = []string{selector.BlockID}
			plan.IncludeCandidates, plan.SourceHorizon = true, "archive"
			plan.Limit = selector.Limit
			if plan.Limit == 0 {
				plan.Limit = 10
			}
			plan.MaxTokens = 8192
			if err := plan.Validate(); err != nil {
				return result, err
			}
			hits, err := engine.Recall(ctx, plan)
			if err != nil {
				return result, err
			}
			result.SemanticRecallPerformed = true
			for _, hit := range hits {
				selectedIDs = append(selectedIDs, hit.BlockID)
			}
		case "canonical_heads", "latest_records", "working_state":
			candidates := make([]memory.MemoryRecord, 0)
			for _, row := range byBlock[selector.BlockID] {
				if !row.Current || row.Status != "committed" || row.Backend != engine.recordNativeStore(row.Record) {
					continue
				}
				record := row.Record
				scopeKey, err := record.ScopeKey()
				if err != nil || !allowedScopes[scopeKey] {
					continue
				}
				if selector.SelectorType == "working_state" {
					if record.DeliveryPolicy != "working_set" || selector.WorkingStateID != "" &&
						record.Metadata["working_state_id"] != selector.WorkingStateID {
						continue
					}
				}
				candidates = append(candidates, record)
			}
			if selector.SelectorType == "latest_records" || selector.SelectorType == "working_state" {
				sort.Slice(candidates, func(i, j int) bool {
					left, right := candidates[i], candidates[j]
					leftTime := firstNonempty(left.UpdatedAt, left.RecordedAt, left.CreatedAt)
					rightTime := firstNonempty(right.UpdatedAt, right.RecordedAt, right.CreatedAt)
					if leftTime != rightTime {
						return leftTime > rightTime
					}
					return left.BlockID > right.BlockID
				})
				if selector.Limit < len(candidates) {
					candidates = candidates[:selector.Limit]
				}
			}
			for _, record := range candidates {
				selectedIDs = append(selectedIDs, record.BlockID)
			}
		default:
			return result, errors.New("active mask selector is unsupported")
		}
	}
	selected := map[string]bool{}
	for _, blockID := range mask.SelectedBlockIDs {
		selected[blockID] = true
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID = engine.config.WorkspaceID, engine.config.ProfileID
	plan.ScopeChain, plan.RunID = request.ScopeChain, request.SessionID
	plan.IncludeCandidates, plan.SourceHorizon = true, "archive"
	seen := map[string]bool{}
	priorities := map[string]int{}
	for _, item := range mask.Priorities {
		priorities[item.BlockID] = item.Priority
	}
	for _, id := range selectedIDs {
		row, exists := byID[id]
		if !exists {
			continue
		}
		if head, exists := currentByKey[row.Record.CanonicalKey]; exists && row.Record.CanonicalKey != "" {
			row = head
		}
		record := row.Record
		if seen[record.BlockID] || row.Status != "committed" || row.Backend != engine.recordNativeStore(row.Record) ||
			record.Status == "expired" ||
			record.BlockMembership == nil || !selected[record.BlockMembership.PrimaryBlockID] ||
			!memory.EligibleForRecall(record, plan, time.Now().UTC()) {
			continue
		}
		dependency, err := engine.index.ValidateDependencies(ctx, record.BlockID, store.DependencyOptions{})
		if err != nil {
			return result, err
		}
		if dependency.Status != "valid" {
			continue
		}
		seen[record.BlockID] = true
		result.Records = append(result.Records, record)
	}
	sort.SliceStable(result.Records, func(i, j int) bool {
		left, right := result.Records[i], result.Records[j]
		leftPriority := priorities[left.BlockMembership.PrimaryBlockID]
		rightPriority := priorities[right.BlockMembership.PrimaryBlockID]
		if leftPriority != rightPriority {
			return leftPriority > rightPriority
		}
		leftTime := firstNonempty(left.UpdatedAt, left.RecordedAt, left.CreatedAt)
		rightTime := firstNonempty(right.UpdatedAt, right.RecordedAt, right.CreatedAt)
		if leftTime != rightTime {
			return leftTime > rightTime
		}
		return left.BlockID > right.BlockID
	})
	blockBudgets := map[string]int{}
	for _, item := range mask.BlockTokenBudgets {
		blockBudgets[item.BlockID] = item.TokenBudget
	}
	usage := map[string]int{}
	budgeted := make([]memory.MemoryRecord, 0, len(result.Records))
	for _, record := range result.Records {
		blockID := record.BlockMembership.PrimaryBlockID
		cost := memory.EstimateTokens(record.Content)
		if limit, exists := blockBudgets[blockID]; exists && usage[blockID]+cost > limit {
			continue
		}
		usage[blockID] += cost
		budgeted = append(budgeted, record)
		result.RecordIDs = append(result.RecordIDs, record.BlockID)
	}
	result.Records = budgeted
	result.Status = "resolved"
	result.RefreshReason = "canonical_heads_refreshed"
	if result.CatalogRevision != mask.CatalogRevision {
		result.RefreshReason = "catalog_revision_changed"
	}
	return result, nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
