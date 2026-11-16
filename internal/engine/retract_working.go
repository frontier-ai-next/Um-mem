package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

type RetractWorkingRequest struct {
	MemoryID     string `json:"memory_id"`
	OwnerID      string `json:"owner_id"`
	GenerationID string `json:"generation_id"`
	Reason       string `json:"reason"`
}

// RetractWorkingRecord removes only the current candidate still owned by the
// selected owner generation. An accepted or foreign successor is preserved.
func (engine *Engine) RetractWorkingRecord(ctx context.Context,
	request RetractWorkingRequest) (map[string]any, error) {
	if request.MemoryID == "" || request.MemoryID != strings.TrimSpace(request.MemoryID) ||
		strings.TrimSpace(request.OwnerID) == "" || strings.TrimSpace(request.GenerationID) == "" ||
		strings.TrimSpace(request.Reason) == "" {
		return nil, errors.New("working retraction requires exact memory_id, owner, generation, and reason")
	}
	owner := map[string]any{"owner_id": request.OwnerID, "generation_id": request.GenerationID}
	source, err := engine.ReadStored(ctx, request.MemoryID)
	if err != nil {
		return nil, err
	}
	if source == nil || source.CanonicalKey == "" {
		return nil, errors.New("working record was not found in this profile")
	}
	if !sameCorrectionOwner(source.Metadata["working_set_owner"], owner) {
		return nil, errors.New("working record belongs to another owner or generation")
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	var current *memory.MemoryRecord
	for _, row := range rows {
		if row.Current && row.Status == "committed" && row.Backend == engine.recordNativeStore(row.Record) &&
			row.Record.CanonicalKey == source.CanonicalKey {
			if current != nil {
				return nil, errors.New("canonical key has more than one current head")
			}
			copy := row.Record
			current = &copy
		}
	}
	if current == nil {
		return nil, errors.New("working canonical current head is unavailable")
	}
	if current.Status == "retracted" {
		return map[string]any{"source_record_id": request.MemoryID,
			"record_id": current.BlockID, "revision_id": current.RevisionID,
			"outcome": "already_retracted"}, nil
	}
	if current.Status != "candidate" ||
		!sameCorrectionOwner(current.Metadata["working_set_owner"], owner) {
		return map[string]any{"source_record_id": request.MemoryID,
			"record_id": current.BlockID, "revision_id": current.RevisionID,
			"outcome": "superseded"}, nil
	}
	result, err := engine.Forget(ctx, ForgetRequest{MemoryID: current.BlockID,
		ExpectedRevision: current.RevisionID, Reason: request.Reason})
	if err != nil {
		return nil, err
	}
	if result["saved"] != true {
		return nil, errors.New("working record retraction failed")
	}
	id, ok := result["block_id"].(string)
	if !ok || id == "" {
		return nil, errors.New("working record retraction returned no successor")
	}
	retracted, err := engine.ReadStored(ctx, id)
	if err != nil || retracted == nil || retracted.Status != "retracted" ||
		retracted.RevisionOf != current.BlockID {
		return nil, errors.New("working record retraction returned an invalid successor")
	}
	return map[string]any{"source_record_id": request.MemoryID,
		"record_id": retracted.BlockID, "revision_id": retracted.RevisionID,
		"outcome": "retracted"}, nil
}
