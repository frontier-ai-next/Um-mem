package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/blockcatalog"
	"github.com/frontier-ai-next/Um-mem/internal/catalog"
)

type ConvertBlockToIndexRequest struct {
	BlockID          string `json:"block_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Reason           string `json:"reason"`
}

// ConvertBlockToIndex turns a never-used content leaf into an index parent.
// Historical and pending canonical records both prevent the conversion.
func (engine *Engine) ConvertBlockToIndex(ctx context.Context,
	request ConvertBlockToIndexRequest) (map[string]any, error) {
	id, reason := strings.TrimSpace(request.BlockID), strings.TrimSpace(request.Reason)
	if id == "" || id != request.BlockID || request.ExpectedRevision < 1 || reason == "" {
		return nil, errors.New("convert_block_to_index requires block_id, integer expected_revision, and reason")
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Backend == engine.recordNativeStore(row.Record) && row.Record.BlockMembership != nil &&
			row.Record.BlockMembership.PrimaryBlockID == id {
			return nil, errors.New("convert_block_to_index requires a leaf that has never held records")
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
	if state == nil {
		return nil, errors.New("dynamic memory block was not found in this profile")
	}
	if state.Protected || state.Status != "active" {
		return nil, errors.New("conversion requires an active unprotected block")
	}
	if state.Block.IndexOnly {
		revision, err := opened.CatalogRevision(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "saved": false, "already_index_only": true,
			"block_id": id, "revision": state.Revision, "catalog_revision": revision,
			"block": state.Block}, nil
	}
	if state.Revision != request.ExpectedRevision {
		return nil, errors.New("expected_revision does not match current block revision")
	}
	if len(state.Block.ChildIDs) > 0 {
		return nil, errors.New("a block with children cannot be converted to an index block")
	}
	updated := state.Block
	updated.BlockType = "workspace_dynamic_index"
	updated.IndexOnly, updated.AgentWritable = true, false
	if err := updated.Validate(); err != nil {
		return nil, err
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	candidate := engine.Blocks()
	found := false
	for index := range candidate {
		if candidate[index].BlockID == id {
			candidate[index] = updated
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("active memory block is missing from semantic catalog")
	}
	if err := blockcatalog.ValidateTree(candidate); err != nil {
		return nil, err
	}
	if err := blockcatalog.ValidateLeaves(candidate); err != nil {
		return nil, err
	}
	empty := ""
	converted, err := opened.ReplaceBlock(ctx, *state, updated, catalog.ReplaceOptions{
		ExpectedRevision: request.ExpectedRevision, Status: "active", Actor: "llm",
		Reason: reason, Operation: "convert_block_to_index", BackendPartitionID: &empty})
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
	payload, err := blockStatePayload(converted, revision)
	if err != nil {
		return nil, err
	}
	payload["previous_revision"], payload["archived"] = state.Revision, false
	return payload, nil
}
