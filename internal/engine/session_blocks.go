package engine

import (
	"context"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/blockcatalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

type RepairProtectedBlockParentRequest struct {
	BlockID          string         `json:"block_id"`
	ExpectedRevision int            `json:"expected_revision"`
	ExpectedParentID string         `json:"expected_parent_id"`
	ParentID         string         `json:"parent_id"`
	ExpectedMetadata map[string]any `json:"expected_metadata"`
	Reason           string         `json:"reason"`
}

func equalGuardValue(left, right any) bool {
	a, err := jsoncompat.MarshalPython(left, false)
	if err != nil {
		return false
	}
	b, err := jsoncompat.MarshalPython(right, false)
	return err == nil && string(a) == string(b)
}

// RepairProtectedBlockParent changes one host-owned parent only after exact
// revision, parent and metadata guards pass. The catalog CAS remains atomic.
func (engine *Engine) RepairProtectedBlockParent(ctx context.Context,
	request RepairProtectedBlockParentRequest) (map[string]any, error) {
	id := strings.TrimSpace(request.BlockID)
	oldParent := strings.TrimSpace(request.ExpectedParentID)
	newParent := strings.TrimSpace(request.ParentID)
	reason := strings.TrimSpace(request.Reason)
	if id == "" || request.ExpectedRevision < 1 || oldParent == "" || newParent == "" ||
		oldParent == newParent || reason == "" || len(request.ExpectedMetadata) == 0 {
		return nil, errors.New("repair_protected_block_parent requires block, revision, distinct parents, metadata guard, and reason")
	}
	for key := range request.ExpectedMetadata {
		if strings.TrimSpace(key) == "" {
			return nil, errors.New("protected block metadata guard has an empty key")
		}
	}
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	state, err := opened.GetBlock(ctx, id)
	if err != nil {
		return nil, err
	}
	if state == nil || state.Revision != request.ExpectedRevision || state.Status != "active" ||
		!state.Protected || state.Block.ParentID != oldParent {
		return nil, errors.New("protected block does not match the repair guard")
	}
	for key, expected := range request.ExpectedMetadata {
		actual, found := state.Block.Metadata[key]
		if !found || !equalGuardValue(actual, expected) {
			return nil, errors.New("protected block metadata does not match the repair guard")
		}
	}
	updatedBlock := state.Block
	updatedBlock.ParentID = newParent
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	candidate := engine.Blocks()
	found := false
	for i := range candidate {
		if candidate[i].BlockID == id {
			candidate[i] = updatedBlock
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("active protected block is missing from semantic catalog")
	}
	if err := blockcatalog.ValidateTree(candidate); err != nil {
		return nil, err
	}
	if err := blockcatalog.ValidateLeaves(candidate); err != nil {
		return nil, err
	}
	updated, err := opened.ReplaceBlock(ctx, *state, updatedBlock, catalog.ReplaceOptions{
		ExpectedRevision: request.ExpectedRevision, Status: "active", Actor: "system",
		Reason: reason, Operation: "repair_protected_block_parent", AllowProtected: true})
	if err != nil {
		return nil, err
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	revision, err := opened.CatalogRevision(ctx)
	if err != nil {
		return nil, err
	}
	payload, err := blockStatePayload(updated, revision)
	if err != nil {
		return nil, err
	}
	payload["previous_revision"], payload["archived"] = state.Revision, false
	return payload, nil
}

type SessionBlockRequest struct {
	BlockID          string `json:"block_id"`
	ExpectedRevision int    `json:"expected_revision"`
	SessionID        string `json:"session_id"`
	Reason           string `json:"reason"`
	ExpiresAt        string `json:"expires_at,omitempty"`
}

func (engine *Engine) sessionBlockMutation(ctx context.Context, request SessionBlockRequest,
	mode string) (map[string]any, error) {
	id := strings.TrimSpace(request.BlockID)
	sessionID := strings.TrimSpace(request.SessionID)
	reason := strings.TrimSpace(request.Reason)
	if id == "" || request.ExpectedRevision < 1 || sessionID == "" || reason == "" {
		return nil, errors.New("session block mutation requires block, revision, session and reason")
	}
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	state, err := opened.GetBlock(ctx, id)
	if err != nil {
		return nil, err
	}
	if state == nil || state.Revision != request.ExpectedRevision ||
		state.Block.Scope != "session" ||
		strings.TrimSpace(stringValue(state.Block.Metadata["umbrella_memory_session_id"])) != sessionID {
		return nil, errors.New("session block ownership or revision does not match")
	}
	block := state.Block
	status, operation := state.Status, "amend_block"
	switch mode {
	case "archive":
		status = "archived"
		if state.Status != "archived" {
			operation = "archive_block"
		}
		block.Metadata = make(map[string]any, len(state.Block.Metadata)+1)
		for key, value := range state.Block.Metadata {
			block.Metadata[key] = value
		}
		block.Metadata["archive_reason"] = reason
	case "revive":
		status = "active"
		if state.Status == "archived" {
			operation = "revive_block"
		}
	case "expiry":
		block.LifecyclePolicy = make(map[string]any, len(state.Block.LifecyclePolicy)+1)
		for key, value := range state.Block.LifecyclePolicy {
			block.LifecyclePolicy[key] = value
		}
		if expires := strings.TrimSpace(request.ExpiresAt); expires != "" {
			block.LifecyclePolicy["expires_at"] = expires
		} else {
			delete(block.LifecyclePolicy, "expires_at")
		}
	default:
		return nil, errors.New("unsupported session block mutation")
	}
	if status == "active" {
		if err := engine.RefreshCatalog(); err != nil {
			return nil, err
		}
		candidate := engine.Blocks()
		found := false
		for i := range candidate {
			if candidate[i].BlockID == id {
				candidate[i] = block
				found = true
				break
			}
		}
		if !found && state.Status != "archived" {
			return nil, errors.New("active session block is missing from semantic catalog")
		}
		if !found {
			candidate = append(candidate, block)
		}
		if err := blockcatalog.ValidateTree(candidate); err != nil {
			return nil, err
		}
		if err := blockcatalog.ValidateLeaves(candidate); err != nil {
			return nil, err
		}
	}
	updated, err := opened.ReplaceBlock(ctx, *state, block, catalog.ReplaceOptions{
		ExpectedRevision: request.ExpectedRevision, Status: status, Actor: "system",
		Reason: reason, Operation: operation, AllowProtected: state.Protected})
	if err != nil {
		return nil, err
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	revision, err := opened.CatalogRevision(ctx)
	if err != nil {
		return nil, err
	}
	payload, err := blockStatePayload(updated, revision)
	if err != nil {
		return nil, err
	}
	payload["previous_revision"], payload["archived"] = state.Revision, status == "archived"
	if mode == "archive" {
		payload["reason"] = reason
	}
	return payload, nil
}

func (engine *Engine) ArchiveSessionBlock(ctx context.Context, request SessionBlockRequest) (map[string]any, error) {
	return engine.sessionBlockMutation(ctx, request, "archive")
}

func (engine *Engine) ReviveSessionBlock(ctx context.Context, request SessionBlockRequest) (map[string]any, error) {
	return engine.sessionBlockMutation(ctx, request, "revive")
}

func (engine *Engine) SetSessionBlockExpiry(ctx context.Context, request SessionBlockRequest) (map[string]any, error) {
	return engine.sessionBlockMutation(ctx, request, "expiry")
}
