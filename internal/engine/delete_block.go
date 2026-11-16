package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type DeleteBlockRequest struct {
	BlockID          string `json:"block_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Reason           string `json:"reason"`
	Cascade          bool   `json:"cascade,omitempty"`
}

func (engine *Engine) DeleteBlock(ctx context.Context, request DeleteBlockRequest) (map[string]any, error) {
	id, reason := strings.TrimSpace(request.BlockID), strings.TrimSpace(request.Reason)
	if id == "" || request.ExpectedRevision < 1 || reason == "" {
		return nil, errors.New("delete_block requires block_id, integer expected_revision, and reason")
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
		return nil, errors.New("memory block was not found in this profile")
	}
	if state.Status != "archived" {
		return nil, errors.New("hard delete requires an archived block")
	}
	if state.Protected {
		return nil, errors.New("protected root cannot be deleted")
	}
	if state.Revision != request.ExpectedRevision {
		return nil, errors.New("expected_revision does not match current block revision")
	}
	if err := opened.CheckDeleteGuards(ctx, id); err != nil {
		return nil, err
	}
	current, pending, err := engine.currentRecordsInBlock(ctx, id)
	if err != nil {
		return nil, err
	}
	if pending && !request.Cascade {
		return nil, errors.New("hard delete cannot prove record coverage while a native write is pending")
	}
	if len(current) > 0 && !request.Cascade {
		return map[string]any{"ok": true, "deleted": false, "archived": true,
			"records_remaining": len(current),
			"hint":              "The block is archived but still has current records. Call delete_block again with cascade=true to retract them and delete the block, or forget each record first."}, nil
	}
	if request.Cascade && (len(current) > 0 || pending) {
		failures := make([]map[string]string, 0)
		for _, record := range current {
			result, err := engine.Forget(ctx, ForgetRequest{MemoryID: record.BlockID,
				ExpectedRevision: record.RevisionID, Reason: "cascade delete block: " + reason})
			if err != nil {
				failures = append(failures, map[string]string{"record_id": record.BlockID,
					"error": err.Error()})
			} else if result["saved"] != true {
				failures = append(failures, map[string]string{"record_id": record.BlockID,
					"error": fmt.Sprint(result["error"])})
			}
		}
		current, pending, err = engine.currentRecordsInBlock(ctx, id)
		if err != nil {
			return nil, err
		}
		if pending || len(current) > 0 {
			return map[string]any{"ok": false, "deleted": false, "archived": true,
				"records_remaining": len(current),
				"error":             "cascade delete could not retract every current record",
				"cascade_failures":  failures, "pending_native_writes": pending}, nil
		}
	}
	tombstone, err := opened.DeleteBlock(ctx, *state, request.ExpectedRevision, "llm", reason)
	if err != nil {
		return nil, err
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "deleted": true, "archived": true,
		"records_remaining": 0, "tombstone": tombstone}, nil
}

func (engine *Engine) currentRecordsInBlock(ctx context.Context, blockID string) ([]memory.MemoryRecord, bool, error) {
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, false, err
	}
	current := make([]memory.MemoryRecord, 0)
	pending := false
	for _, row := range rows {
		record := row.Record
		if record.BlockMembership == nil || record.BlockMembership.PrimaryBlockID != blockID {
			continue
		}
		if row.Status == "pending" {
			pending = true
			continue
		}
		if !row.Current {
			continue
		}
		if row.Status != "committed" || row.Backend != engine.recordNativeStore(row.Record) {
			return nil, false, errors.New("hard delete cannot prove current record coverage for the selected backend")
		}
		switch record.Status {
		case "superseded", "deprecated", "quarantined", "retracted", "expired":
			continue
		}
		current = append(current, record)
	}
	return current, pending, nil
}
