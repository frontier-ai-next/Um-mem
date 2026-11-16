package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/blockcatalog"
	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type CreateBlockRequest struct {
	Name        string         `json:"name"`
	Synopsis    string         `json:"synopsis"`
	ParentID    string         `json:"parent_id"`
	Reason      string         `json:"reason"`
	Tags        []string       `json:"tags,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Scope       string         `json:"scope,omitempty"`
	IndexOnly   bool           `json:"index_only,omitempty"`
	TokenBudget *int           `json:"token_budget,omitempty"`
	ExpiresAt   string         `json:"expires_at,omitempty"`
}

func newCatalogUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}

func (engine *Engine) CreateBlock(ctx context.Context, request CreateBlockRequest) (map[string]any, error) {
	name, synopsis := strings.TrimSpace(request.Name), strings.TrimSpace(request.Synopsis)
	parentID, reason := strings.TrimSpace(request.ParentID), strings.TrimSpace(request.Reason)
	if name == "" || synopsis == "" || parentID == "" || reason == "" {
		return nil, errors.New("dynamic memory block name, synopsis, parent_id, and reason are required")
	}
	scope := strings.ToLower(strings.TrimSpace(request.Scope))
	if scope == "" {
		scope = "workspace"
	}
	if scope != "workspace" && scope != "project" && scope != "session" &&
		scope != "task" && scope != "subtask" && scope != "user_global" {
		return nil, errors.New("dynamic memory block scope is unsupported")
	}
	budget := 1000
	if request.TokenBudget != nil {
		budget = *request.TokenBudget
	}
	if budget < 0 {
		return nil, errors.New("dynamic memory block token_budget must be non-negative")
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	parent, err := opened.GetBlock(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if parent == nil || parent.Status != "active" {
		return nil, errors.New("dynamic memory block parent was not found in this profile")
	}
	if !parent.Block.IndexOnly {
		return nil, errors.New("dynamic memory blocks require an index-only parent")
	}
	id, err := newCatalogUUID()
	if err != nil {
		return nil, err
	}
	partition := ""
	if !request.IndexOnly {
		partition, err = newCatalogUUID()
		if err != nil {
			return nil, err
		}
	}
	lifecycle := map[string]any{"initial_status": "candidate", "mutable": true,
		"terminal_statuses": []string{"superseded", "deprecated", "retracted"}}
	if expires := strings.TrimSpace(request.ExpiresAt); expires != "" {
		lifecycle["expires_at"] = expires
	}
	blockType := "workspace_dynamic"
	if request.IndexOnly {
		blockType = "workspace_dynamic_index"
	}
	block := memory.Block{BlockID: id, BlockType: blockType, Temperature: "warm",
		Scope: scope, Authority: "advisory", DeliveryPolicy: "on_demand",
		Retention: "durable", Name: name, Tags: append([]string{}, request.Tags...),
		Guidance: map[string]any{"synopsis": synopsis, "use_when": []string{}, "avoid_when": []string{}},
		Metadata: request.Metadata, ParentID: parentID, IndexOnly: request.IndexOnly,
		AgentWritable: !request.IndexOnly, Owner: "agent", Producer: "agent",
		Consumers: []string{"memory_agent", "context_packer"}, RecallPath: "hybrid_recall",
		LifecyclePolicy: lifecycle, IsolationPolicy: map[string]any{
			"dimension": scope, "cross_workspace": scope == "user_global"},
		RetrievalPolicy: map[string]any{"mode": "hybrid", "limit": 6, "require_query_seed": true},
		TokenBudget:     budget}
	block.Normalize()
	if err := block.Validate(); err != nil {
		return nil, err
	}
	candidate := append(engine.Blocks(), block)
	if err := blockcatalog.ValidateTree(candidate); err != nil {
		return nil, err
	}
	if err := blockcatalog.ValidateLeaves(candidate); err != nil {
		return nil, err
	}
	state, err := opened.CreateBlock(ctx, block, catalog.CreateOptions{BackendPartitionID: partition,
		Actor: "llm", Reason: reason})
	if err != nil {
		return nil, err
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, fmt.Errorf("catalog block was saved but engine refresh failed: %w", err)
	}
	revision, err := opened.CatalogRevision(ctx)
	if err != nil {
		return nil, err
	}
	return blockStatePayload(state, revision)
}

func blockStatePayload(state catalog.BlockState, revision int) (map[string]any, error) {
	encoded, err := json.Marshal(state.Block)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}
	payload["ok"], payload["saved"] = true, true
	payload["workspace_id"], payload["profile_id"] = state.WorkspaceID, state.ProfileID
	payload["revision"], payload["status"], payload["protected"] = state.Revision, state.Status, state.Protected
	payload["backend_partition_id"] = state.BackendPartitionID
	payload["created_at"], payload["updated_at"] = state.CreatedAt, state.UpdatedAt
	payload["catalog_revision"], payload["block"] = revision, state.Block
	return payload, nil
}
