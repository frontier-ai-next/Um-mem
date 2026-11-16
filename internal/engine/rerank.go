package engine

import (
	"sort"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// rerankByQuery orders recalled records by how much of the query they actually
// cover. The backend score answers "is this related"; a reader asks "does this
// answer me", and a long record that merely shares vocabulary used to outrank
// the short record that is the answer.
func rerankByQuery(query string, records []memory.MemoryRecord) []memory.MemoryRecord {
	queryTokens := tokenSet(query)
	if len(queryTokens) == 0 || len(records) < 2 {
		return records
	}
	type scored struct {
		record memory.MemoryRecord
		score  float64
	}
	ranked := make([]scored, len(records))
	for i, record := range records {
		titleTokens := tokenSet(record.Title)
		contentTokens := tokenSet(record.Content)
		score := 0.7*containment(queryTokens, titleTokens) + 0.3*containment(queryTokens, contentTokens)
		if score > 1 {
			score = 1
		}
		ranked[i] = scored{record: record, score: score}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	out := make([]memory.MemoryRecord, len(records))
	for i, item := range ranked {
		out[i] = item.record
	}
	return out
}
