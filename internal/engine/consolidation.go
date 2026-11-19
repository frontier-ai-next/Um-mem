package engine

import (
	"context"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// ConsolidationGroup is one duplicate cluster and the record that should survive.
type ConsolidationGroup struct {
	Title    string   `json:"title"`
	KeepID   string   `json:"keep_id"`
	RetireID []string `json:"retire_ids"`
	Reason   string   `json:"reason"`
}

// Consolidation plans (and optionally performs) duplicate retirement. It exists
// because bulk cleanup done by hand already cost this store thirty-four records:
// a plan with ids, counts and a dry run is the only form of deletion allowed.
func (engine *Engine) Consolidation(ctx context.Context, threshold float64, apply bool, limit int) (map[string]any, error) {
	if threshold <= 0 {
		threshold = 0.92
	}
	if limit <= 0 {
		limit = 10
	}
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	semantic := map[string]int{}
	for _, edge := range edges {
		if edge.EdgeType == "supersedes" {
			continue
		}
		semantic[edge.SrcID]++
		semantic[edge.DstID]++
	}

	byID := make(map[string]memory.MemoryRecord, len(records))
	for _, record := range records {
		byID[record.BlockID] = record
	}
	live := make([]memory.MemoryRecord, 0, len(records))
	withdrawn := retractedTargets(records, edges)
	for _, record := range records {
		if record.Status == "retracted" || record.Status == "superseded" || record.Kind == "episode" || withdrawn[record.BlockID] {
			continue
		}
		live = append(live, record)
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].RecordedAt < live[j].RecordedAt })

	used := map[string]bool{}
	groups := make([]ConsolidationGroup, 0, limit)
	for i := 0; i < len(live); i++ {
		if used[live[i].BlockID] {
			continue
		}
		cluster := []memory.MemoryRecord{live[i]}
		for j := i + 1; j < len(live); j++ {
			if used[live[j].BlockID] {
				continue
			}
			nearDuplicate := threshold < 0.99 && similarity(live[i].Title, tokenSet(live[i].Title+" "+live[i].Content),
				live[j].Title, tokenSet(live[j].Title+" "+live[j].Content)) >= threshold
			if sameContent(live[i], live[j]) || nearDuplicate {
				cluster = append(cluster, live[j])
				used[live[j].BlockID] = true
			}
		}
		if len(cluster) < 2 {
			continue
		}
		keep := cluster[0]
		for _, candidate := range cluster {
			if semantic[candidate.BlockID] > semantic[keep.BlockID] {
				keep = candidate
			}
		}
		retire := make([]string, 0, len(cluster)-1)
		for _, candidate := range cluster {
			if candidate.BlockID != keep.BlockID {
				retire = append(retire, candidate.BlockID)
			}
		}
		groups = append(groups, ConsolidationGroup{Title: keep.Title, KeepID: keep.BlockID,
			RetireID: retire, Reason: "duplicate content"})
		if len(groups) >= limit*4 {
			break
		}
	}

	retired := make([]string, 0, limit)
	failures := make([]map[string]string, 0)
	if apply {
		for _, group := range groups {
			for _, id := range group.RetireID {
				if len(retired) >= limit {
					break
				}
				expected := ""
				if record, found := byID[id]; found {
					expected = record.RevisionID
				}
				if _, err := engine.Forget(ctx, ForgetRequest{MemoryID: id, ExpectedRevision: expected,
					Reason: "consolidation: duplicate of " + group.KeepID}); err != nil {
					failures = append(failures, map[string]string{"record_id": id, "error": err.Error()})
					continue
				}
				retired = append(retired, id)
			}
		}
	}
	return map[string]any{
		"ok": true, "applied": apply, "threshold": threshold, "limit": limit,
		"groups": len(groups), "plan": groups, "retired": retired, "failures": failures,
		"workspace_id": engine.config.WorkspaceID, "backend": engine.config.BackendID,
		"note": "dry run unless apply is set; retirement is a retraction, not a delete",
	}, nil
}

func sameContent(left, right memory.MemoryRecord) bool {
	normalize := func(value string) string {
		return strings.Join(strings.Fields(strings.ToLower(value)), " ")
	}
	return normalize(left.Content) == normalize(right.Content) && normalize(left.Title) == normalize(right.Title)
}

