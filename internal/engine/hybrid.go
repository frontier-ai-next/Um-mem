package engine

import (
	"context"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// hybridRank reorders the records admitted by the selected backend. This set
// already enforces scope, facets, lifecycle and the requested retrieval budget.
// Ranking may change order, but must not broaden that set.
func (engine *Engine) hybridRank(ctx context.Context, query string, backend []memory.MemoryRecord, limit int, dense bool) []memory.MemoryRecord {
	if limit <= 0 {
		limit = len(backend)
	}
	if strings.TrimSpace(query) == "" || len(backend) == 0 || (!dense && len(backend) == 1) {
		return backend[:min(len(backend), limit)]
	}
	index := buildBM25(backend)
	lexical := index.search(query, limit*3)
	semantic := engine.embedScores(ctx, query, backend, dense)

	queryTokens := tokenSet(query)
	type scored struct {
		record memory.MemoryRecord
		score  float64
	}
	ranked := make([]scored, 0, len(backend))
	now := time.Now().UTC()
	for position, record := range backend {
		coverage := 0.7*containment(queryTokens, tokenSet(record.Title)) +
			0.3*containment(queryTokens, tokenSet(record.Content))
		backendRank := 1 - float64(position)/float64(len(backend))
		value := 0.30*coverage + 0.25*lexical[record.BlockID] + 0.15*backendRank +
			0.15*semantic[record.BlockID] + 0.15*recordBoost(record, now)
		if dense && len(semantic) > 0 {
			value = 0.08*coverage + 0.08*lexical[record.BlockID] + 0.05*backendRank +
				0.75*semantic[record.BlockID] + 0.04*recordBoost(record, now)
		}
		ranked = append(ranked, scored{record: record, score: value})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]memory.MemoryRecord, len(ranked))
	for i, item := range ranked {
		out[i] = item.record
		if similarity, ok := semantic[item.record.BlockID]; dense && ok {
			out[i].Metadata = maps.Clone(item.record.Metadata)
			if out[i].Metadata == nil {
				out[i].Metadata = map[string]any{}
			}
			out[i].Metadata["retrieval_dense_similarity"] = similarity
		}
	}
	return out
}

// recordBoost rewards records that are trustworthy rather than merely similar:
// promoted or verified, freshly written, or carrying several witnesses.
func recordBoost(record memory.MemoryRecord, now time.Time) float64 {
	boost := 0.0
	if record.Status == "verified" || record.Status == "promoted" {
		boost += 0.5
	}
	if len(record.EvidenceRefs) >= 2 {
		boost += 0.2
	}
	if stamp, err := time.Parse(time.RFC3339Nano, record.RecordedAt); err == nil {
		age := now.Sub(stamp.UTC())
		switch {
		case age < 24*time.Hour:
			boost += 0.3
		case age < 7*24*time.Hour:
			boost += 0.15
		}
	}
	if boost > 1 {
		boost = 1
	}
	return boost
}
