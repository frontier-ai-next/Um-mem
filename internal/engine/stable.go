package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// StableResolve answers the question every host eventually asks: "is this still
// the record I referenced?" A revision is stored as its own record whose
// `revision_of` points at the previous one, so an id handed out yesterday may
// already be superseded. The stable id is the root of that chain; it survives
// every amend.
func (engine *Engine) StableResolve(ctx context.Context, recordID string) (map[string]any, error) {
	id := strings.TrimSpace(recordID)
	if id == "" {
		return nil, errors.New("stable id requires a record id")
	}
	records, _, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]memory.MemoryRecord, len(records))
	for _, record := range records {
		byID[record.BlockID] = record
	}
	if _, found := byID[id]; !found {
		return nil, errors.New("record not found in the selected workspace and profile")
	}

	rootOf := func(start string) string {
		current := start
		visited := map[string]bool{}
		for {
			record, ok := byID[current]
			if !ok || strings.TrimSpace(record.RevisionOf) == "" || visited[current] {
				return current
			}
			visited[current] = true
			parent := record.RevisionOf
			if _, ok := byID[parent]; !ok {
				return current
			}
			current = parent
		}
	}

	stable := rootOf(id)
	current := byID[id]
	revisions := 0
	for _, record := range records {
		if rootOf(record.BlockID) != stable {
			continue
		}
		revisions++
		if record.Status == "retracted" {
			continue
		}
		if record.RecordedAt > current.RecordedAt {
			current = record
		}
	}
	return map[string]any{
		"ok":          true,
		"stable_id":   stable,
		"record_id":   current.BlockID,
		"revision_id": current.RevisionID,
		"status":      current.Status,
		"title":       current.Title,
		"revisions":   revisions,
		"requested":   id,
		"workspace_id": engine.config.WorkspaceID,
	}, nil
}
