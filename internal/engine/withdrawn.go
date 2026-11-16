package engine

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// WithdrawnIDs reports records covered by a retraction. Retraction is stored as a
// successor record plus a `supersedes` edge, so the original keeps its candidate
// status. Every consumer that reads only the status would treat a withdrawn
// record as live; this is the single place that answers the question.
func (engine *Engine) WithdrawnIDs(ctx context.Context) (map[string]bool, error) {
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return retractedTargets(records, edges), nil
}

// markWithdrawn stamps the transient view of records returned by recall. The
// stored record is untouched: the flag says "do not rely on this", not "this was
// edited".
func markWithdrawn(records []memory.MemoryRecord, withdrawn map[string]bool) []memory.MemoryRecord {
	if len(withdrawn) == 0 {
		return records
	}
	for i := range records {
		if withdrawn[records[i].BlockID] {
			records[i].Status = "withdrawn"
		}
	}
	return records
}
