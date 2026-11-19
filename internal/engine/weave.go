package engine

import (
	"context"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// WeaveLink is one proposed topic bridge between two blocks.
type WeaveLink struct {
	FromID    string  `json:"from_id"`
	FromBlock string  `json:"from_block"`
	ToID      string  `json:"to_id"`
	ToBlock   string  `json:"to_block"`
	Score     float64 `json:"score"`
	Title     string  `json:"title"`
}

// WeaveBlocks connects records that sit in different blocks but talk about the
// same thing. Without this a store is a stack of unrelated shelves: recall works
// inside a block and the graph view shows nothing but one lonely bridge.
func (engine *Engine) WeaveBlocks(ctx context.Context, threshold float64, apply bool, limit int) (map[string]any, error) {
	if threshold <= 0 {
		threshold = 0.16
	}
	if limit <= 0 {
		limit = 20
	}
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	withdrawn := retractedTargets(records, edges)

	type entry struct {
		record  memory.MemoryRecord
		block   string
		title   string
		content string
		tokens  map[string]bool
	}
	live := make([]entry, 0, len(records))
	for _, record := range records {
		if record.Status == "retracted" || record.Status == "superseded" || withdrawn[record.BlockID] {
			continue
		}
		if record.Kind == "episode" {
			continue
		}
		block := ""
		if record.BlockMembership != nil {
			block = record.BlockMembership.PrimaryBlockID
		}
		if block == "" {
			continue
		}
		live = append(live, entry{record: record, block: block, title: record.Title,
			content: record.Content, tokens: tokenSet(record.Title + " " + record.Content)})
	}

	existing := map[string]bool{}
	crossPairs := map[string]bool{}
	for _, edge := range edges {
		if edge.EdgeType == "supersedes" {
			continue
		}
		existing[edge.SrcID+"|"+edge.DstID] = true
		existing[edge.DstID+"|"+edge.SrcID] = true
	}
	blockOf := map[string]string{}
	for _, item := range live {
		blockOf[item.record.BlockID] = item.block
	}
	for _, edge := range edges {
		from, to := blockOf[edge.SrcID], blockOf[edge.DstID]
		if from != "" && to != "" && from != to {
			crossPairs[from+"->"+to] = true
			crossPairs[to+"->"+from] = true
		}
	}

	links := make([]WeaveLink, 0, limit)
	for _, item := range live {
		if len(links) >= limit {
			break
		}
		best := WeaveLink{Score: 0}
		for _, other := range live {
			if other.block == item.block {
				continue
			}
			if existing[item.record.BlockID+"|"+other.record.BlockID] {
				continue
			}
			score := similarity(item.title, item.tokens, other.title, other.tokens)
			if score > best.Score {
				best = WeaveLink{FromID: item.record.BlockID, FromBlock: item.block,
					ToID: other.record.BlockID, ToBlock: other.block, Score: score,
					Title: other.title}
			}
		}
		if best.Score < threshold {
			continue
		}
		links = append(links, best)
	}
	sort.SliceStable(links, func(i, j int) bool { return links[i].Score > links[j].Score })

	created := make([]string, 0, len(links))
	failures := make([]map[string]string, 0)
	if apply {
		for _, link := range links {
			edge := memory.EdgeRecord{
				SrcID: link.FromID, DstID: link.ToID, EdgeType: "related_to",
				WorkspaceID: engine.config.WorkspaceID,
				Reason:      "topic bridge across blocks",
				EvidenceRefs: []memory.EvidenceRef{
					{RefType: "tool_result", RefID: "weave:" + link.FromID + ":" + link.ToID,
						ProducedBy: "agent", ProducerID: "um-mem-weave"},
				},
			}
			if _, err := engine.writer.AddEdge(ctx, edge); err != nil {
				failures = append(failures, map[string]string{"from": link.FromID, "to": link.ToID,
					"error": strings.TrimSpace(err.Error())})
				continue
			}
			created = append(created, link.FromID+"|"+link.ToID)
		}
	}
	return map[string]any{
		"ok": true, "applied": apply, "threshold": threshold, "limit": limit,
		"proposed": len(links), "created": len(created), "created_pairs": created,
		"failures": failures, "pairs": links,
		"workspace_id": engine.config.WorkspaceID,
		"note":         "dry run unless apply is set; edges are related_to with a tool_result witness",
	}, nil
}
