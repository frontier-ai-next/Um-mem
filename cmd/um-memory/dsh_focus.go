package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func dshHostEvidence(record memory.MemoryRecord) bool {
	return slices.ContainsFunc(record.EvidenceRefs, func(ref memory.EvidenceRef) bool {
		return (ref.RefType == "message" && ref.ProducedBy == "human" && ref.ProducerID == "harness-user") ||
			ref.IsHarnessToolReceipt()
	})
}

func prioritizeDSHFocus(records []memory.MemoryRecord) []memory.MemoryRecord {
	notes := make([]memory.MemoryRecord, 0, len(records))
	var source *memory.MemoryRecord
	for _, record := range records {
		if record.BlockMembership == nil || record.Kind == "episode" || record.Temperature == "cold" ||
			record.Retention == "archive" || record.DeliveryPolicy == "hidden" || record.Status == "withdrawn" {
			continue
		}
		if record.Kind == "source_excerpt" {
			if record.Metadata["source"] == "dsh-user" && dshHostEvidence(record) &&
				(source == nil || !strings.ContainsFunc(source.Content, unicode.IsDigit) && strings.ContainsFunc(record.Content, unicode.IsDigit)) {
				source = &record
			}
			continue
		}
		notes = append(notes, record)
	}
	if len(notes) == 0 && source != nil {
		return []memory.MemoryRecord{*source}
	}
	if len(notes) < 2 || dshHostEvidence(notes[0]) {
		return notes
	}
	// The search already scoped and ranked these candidates. Prefer verified
	// evidence of another note in the same topic to an agent's interim account.
	block := notes[0].BlockMembership.PrimaryBlockID
	for i := 1; i < len(notes); i++ {
		if notes[i].BlockMembership.PrimaryBlockID == block && dshHostEvidence(notes[i]) {
			confirmed := notes[i]
			copy(notes[1:i+1], notes[:i])
			notes[0] = confirmed
			break
		}
	}
	return notes
}

// The caller is the local host. The model never supplies a session or a scope.
type dshFocusRequest struct {
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id,omitempty"`
	Query     string `json:"query"`
	Limit     int    `json:"limit"`
	MaxTokens int    `json:"max_tokens"`
	Hybrid    bool   `json:"hybrid"`
}

func dshFocus(ctx context.Context, selected *engine.Engine, input dshFocusRequest) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" || input.Limit < 1 || input.Limit > 16 ||
		input.MaxTokens < 1 || input.MaxTokens > 2048 || strings.TrimSpace(input.Query) == "" {
		return nil, errors.New("invalid DSH focus request")
	}
	workspace, profile := selected.Scope()
	const task = "dsh-auto"
	scopes := []string{"session:" + input.SessionID, "workspace:" + workspace}
	if input.UserID != "" {
		scopes = append(scopes, "user_global:"+input.UserID)
	}
	plan := memory.NewRecallPlan(input.Query)
	plan.WorkspaceID, plan.ProfileID, plan.ScopeChain, plan.RunID = workspace, profile, scopes, input.SessionID
	plan.SourceHorizon, plan.IncludeCandidates = "durable", true
	for _, block := range selected.Blocks() {
		if !block.IndexOnly && block.Temperature != "cold" && block.Retention != "archive" &&
			block.Authority != "directive" && block.DeliveryPolicy != "hidden" &&
			block.DeliveryPolicy != "reflection_only" && block.BlockID != "workspace_episodes" {
			plan.LogicalBlockIDs = append(plan.LogicalBlockIDs, block.BlockID)
		}
	}
	plan.Limit, plan.MaxTokens = min(max(input.Limit*3, 16), 32), 2048
	if input.Hybrid {
		plan.RetrievalMode = "hybrid"
	}
	var records []memory.MemoryRecord
	if len(plan.LogicalBlockIDs) > 0 {
		var err error
		records, err = selected.Recall(ctx, plan)
		if err != nil {
			return nil, err
		}
	}
	blocks, ids := []string{}, []string{}
	byBlock := map[string][]string{}
	for _, record := range prioritizeDSHFocus(records) {
		block := record.BlockMembership.PrimaryBlockID
		if !slices.Contains(blocks, block) {
			blocks = append(blocks, block)
		}
		ids = append(ids, record.BlockID)
		byBlock[block] = append(byBlock[block], record.BlockID)
		if len(ids) == input.Limit {
			break
		}
	}
	current, err := selected.GetActiveMask(ctx, "", input.SessionID, task)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		if current != nil && current.Status == "active" && !current.IsExpired(time.Now().UTC()) {
			if _, err := selected.TransitionActiveMask(ctx, current.MaskID, current.Revision, "deactivate", "no eligible turn evidence", input.SessionID, task); err != nil {
				return nil, err
			}
		}
		return map[string]any{"status": "empty", "records": []memory.MemoryRecord{}}, nil
	}
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	selectors := make([]memory.ActiveMemorySelector, 0, len(blocks))
	for _, block := range blocks {
		selectors = append(selectors, memory.ActiveMemorySelector{BlockID: block, SelectorType: "pinned_records", RecordIDs: byBlock[block]})
	}
	const reason = "DSH automatic turn focus"
	switch {
	case current == nil || current.Status != "active" || current.IsExpired(time.Now().UTC()):
		mask, err := selected.SetActiveMask(ctx, memory.ActiveMemoryMask{SelectedBlockIDs: blocks,
			TokenBudget: input.MaxTokens, ActivationReason: reason, PinnedRecordIDs: ids, Selectors: selectors, ExpiresAt: expiry}, input.SessionID, task)
		if err != nil {
			return nil, err
		}
		current = &mask
	case !slices.Equal(current.PinnedRecordIDs, ids) || !slices.Equal(current.SelectedBlockIDs, blocks) || current.TokenBudget != input.MaxTokens:
		mask, err := selected.AmendActiveMask(ctx, catalog.MaskAmendment{MaskID: current.MaskID,
			ExpectedRevision: current.Revision, SessionID: input.SessionID, TaskID: task, ActivationReason: reason,
			SelectedBlockIDs: &blocks, PinnedRecordIDs: &ids, Selectors: &selectors, TokenBudget: &input.MaxTokens, ExpiresAt: &expiry})
		if err != nil {
			return nil, err
		}
		current = &mask
	}
	resolved, err := selected.ResolveActiveMask(ctx, engine.ActiveMaskRequest{MaskID: current.MaskID,
		ExpectedRevision: &current.Revision, MaxTokens: &input.MaxTokens, SessionID: input.SessionID, TaskID: task, ScopeChain: scopes})
	if err != nil {
		return nil, err
	}
	// The canonical resolver may add all heads of selected blocks unless the
	// mask supplies selectors. Keep delivery bounded to the turn's pinned rank.
	turnRecords := make([]memory.MemoryRecord, 0, len(ids))
	for _, id := range ids {
		for _, record := range resolved.Records {
			if record.BlockID == id {
				turnRecords = append(turnRecords, record)
				break
			}
		}
	}
	return map[string]any{"status": resolved.Status, "mask_id": current.MaskID, "revision": current.Revision,
		"records": store.FitRecallTokenBudget(turnRecords, resolved.TokenBudget)}, nil
}
