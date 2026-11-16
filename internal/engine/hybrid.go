package engine

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// hybridRank fuses three signals: how much of the query the record covers, the
// BM25 lexical score over the whole live corpus, and whether the semantic backend
// surfaced it at all. Pure lexical matching misses synonyms ("ingest" for
// "добавить источник"); pure backend ranking drifts on short questions and lets
// long records win. Fusing them is what makes ranking defensible.
func (engine *Engine) hybridRank(ctx context.Context, query string, backend []memory.MemoryRecord, limit int) []memory.MemoryRecord {
	if limit <= 0 {
		limit = len(backend)
	}
	if strings.TrimSpace(query) == "" {
		return backend
	}
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return rerankByQuery(query, backend)
	}
	withdrawn := retractedTargets(records, edges)
	corpus := make([]memory.MemoryRecord, 0, len(records))
	byID := make(map[string]memory.MemoryRecord, len(records))
	for _, record := range records {
		if record.Status == "retracted" || record.Status == "superseded" || withdrawn[record.BlockID] {
			continue
		}
		corpus = append(corpus, record)
		byID[record.BlockID] = record
	}
	index := buildBM25(corpus)
	lexical := index.search(query, limit*3)
	semantic := engine.embedScores(ctx, query, corpus)

	queryTokens := tokenSet(query)
	backendRank := make(map[string]float64, len(backend))
	for position, record := range backend {
		if len(backend) == 1 {
			backendRank[record.BlockID] = 1
			continue
		}
		backendRank[record.BlockID] = 1 - float64(position)/float64(len(backend))
	}

	// Candidate set: everything the backend returned plus everything BM25 liked.
	order := make([]string, 0, len(backend)+len(lexical))
	seen := map[string]bool{}
	for _, record := range backend {
		if !seen[record.BlockID] {
			seen[record.BlockID] = true
			order = append(order, record.BlockID)
		}
	}
	lexIDs := make([]string, 0, len(lexical))
	for id := range lexical {
		lexIDs = append(lexIDs, id)
	}
	sort.SliceStable(lexIDs, func(i, j int) bool { return lexical[lexIDs[i]] > lexical[lexIDs[j]] })
	for _, id := range lexIDs {
		if !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	embedIDs := make([]string, 0, len(semantic))
	for id, score := range semantic {
		if score > 0.2 {
			embedIDs = append(embedIDs, id)
		}
	}
	sort.SliceStable(embedIDs, func(i, j int) bool { return semantic[embedIDs[i]] > semantic[embedIDs[j]] })
	for position, id := range embedIDs {
		if position >= limit*3 {
			break
		}
		if !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}

	type scored struct {
		record memory.MemoryRecord
		score  float64
	}
	ranked := make([]scored, 0, len(order))
	now := time.Now().UTC()
	for _, id := range order {
		record, ok := byID[id]
		if !ok {
			for _, candidate := range backend {
				if candidate.BlockID == id {
					record, ok = candidate, true
					break
				}
			}
			if !ok {
				continue
			}
		}
		coverage := 0.7*containment(queryTokens, tokenSet(record.Title)) +
			0.3*containment(queryTokens, tokenSet(record.Content))
		embedding := 0.0
		if score, ok := semantic[id]; ok {
			embedding = score
		}
		value := 0.30*coverage + 0.25*lexical[id] + 0.15*backendRank[id] + 0.15*embedding + 0.15*recordBoost(record, now)
		ranked = append(ranked, scored{record: record, score: value})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]memory.MemoryRecord, len(ranked))
	for i, item := range ranked {
		out[i] = item.record
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
