package engine

import (
	"context"
	"sort"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// Contradiction is a pair of live records that talk about the same thing with
// different words and no supersession between them. The detector is lexical on
// purpose: it proposes a review, it never rewrites anything.
type Contradiction struct {
	AID       string  `json:"a_id"`
	BID       string  `json:"b_id"`
	ATitle    string  `json:"a_title"`
	BTitle    string  `json:"b_title"`
	ABlock    string  `json:"a_block"`
	BBlock    string  `json:"b_block"`
	AKind     string  `json:"a_kind"`
	BKind     string  `json:"b_kind"`
	Similarity float64 `json:"similarity"`
}

// Contradictions reports same-topic pairs that may disagree.
func (engine *Engine) Contradictions(ctx context.Context, threshold float64, limit int) (map[string]any, error) {
	if threshold <= 0 {
		threshold = 0.55
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	records, _, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	live := make([]memory.MemoryRecord, 0, len(records))
	for _, record := range records {
		if record.Status == "retracted" || record.Kind == "episode" || record.Kind == "skill" {
			continue
		}
		live = append(live, record)
	}
	byID := make(map[string]memory.MemoryRecord, len(records))
	for _, record := range records {
		byID[record.BlockID] = record
	}
	stable := make([]string, len(live))
	for i, record := range live {
		stable[i] = stableRoot(byID, record.BlockID)
	}
	tokens := make([]map[string]bool, len(live))
	titles := make([]map[string]bool, len(live))
	for i, record := range live {
		tokens[i] = tokenSet(record.Title + " " + record.Content)
		titles[i] = tokenSet(record.Title)
	}

	pairs := make([]Contradiction, 0, limit)
	seenPair := map[string]bool{}
	for i := 0; i < len(live); i++ {
		for j := i + 1; j < len(live); j++ {
			left, right := live[i], live[j]
			if left.CanonicalKey != "" && left.CanonicalKey == right.CanonicalKey {
				continue
			}
			// Revisions of one record are the same thought, not a disagreement.
			if left.RevisionOf == right.BlockID || right.RevisionOf == left.BlockID {
				continue
			}
			if stable[i] == stable[j] {
				continue
			}
			key := stable[i] + "|" + stable[j]
			if stable[i] > stable[j] {
				key = stable[j] + "|" + stable[i]
			}
			if seenPair[key] {
				continue
			}
			score := similarity(left.Title, tokens[i], right.Title, tokens[j])
			// Only same-topic pairs matter: shared title terms or a strong body overlap.
			sharedTitle := 0
			for token := range titles[i] {
				if titles[j][token] {
					sharedTitle++
				}
			}
			if sharedTitle == 0 && score < threshold+0.2 {
				continue
			}
			if score < threshold {
				continue
			}
			seenPair[key] = true
			pairs = append(pairs, Contradiction{
				AID: left.BlockID, BID: right.BlockID,
				ATitle: left.Title, BTitle: right.Title,
				ABlock: logicalBlockOf(left), BBlock: logicalBlockOf(right),
				AKind: left.Kind, BKind: right.Kind,
				Similarity: score,
			})
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Similarity > pairs[j].Similarity })
	if len(pairs) > limit {
		pairs = pairs[:limit]
	}
	return map[string]any{
		"ok": true, "count": len(pairs), "threshold": threshold, "pairs": pairs,
		"workspace_id": engine.config.WorkspaceID, "backend": engine.config.BackendID,
		"note": "same-topic pairs for review; nothing is superseded automatically",
	}, nil
}
