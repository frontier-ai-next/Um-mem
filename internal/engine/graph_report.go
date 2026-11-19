package engine

import (
	"context"
	"sort"
	"strings"

)

// GraphReport answers the question a tree view cannot: are research blocks only
// stacked, or do they actually connect by topic, with enough metadata to be read
// and a mask that hides what is retired?
type GraphReport struct {
	Workspace    string            `json:"workspace"`
	Blocks       []BlockReport     `json:"blocks"`
	CrossEdges   []CrossBlockEdge  `json:"cross_block_edges"`
	Totals       GraphTotals       `json:"totals"`
	Notes        []string          `json:"notes"`
}

type BlockReport struct {
	BlockID     string  `json:"block_id"`
	Records     int     `json:"records"`
	Labeled     int     `json:"labeled"`
	LinkedOut   int     `json:"linked_out"`
	LinkedIn    int     `json:"linked_in"`
	CrossLinks  int     `json:"cross_links"`
	LabelShare  float64 `json:"label_share"`
}

type CrossBlockEdge struct {
	From      string            `json:"from_block"`
	To        string            `json:"to_block"`
	Count     int               `json:"count"`
	EdgeTypes map[string]int    `json:"edge_types"`
}

type GraphTotals struct {
	Records       int     `json:"records"`
	Withdrawn     int     `json:"withdrawn"`
	Edges         int     `json:"edges"`
	CrossEdges    int     `json:"cross_block_edges"`
	Orphans       int     `json:"records_without_edges"`
	LabelShare    float64 `json:"label_share"`
	BlocksInUse   int     `json:"blocks_in_use"`
	CouplingScore float64 `json:"coupling_score"`
}

func (engine *Engine) GraphReport(ctx context.Context) (GraphReport, error) {
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return GraphReport{}, err
	}
	withdrawn := retractedTargets(records, edges)

	report := GraphReport{Workspace: engine.config.WorkspaceID}
	blockOf := make(map[string]string, len(records))
	stats := make(map[string]*BlockReport)
	live := 0
	withdrawnCount := 0
	labeledLive := 0
	for _, record := range records {
		if record.Status == "retracted" || record.Status == "superseded" || withdrawn[record.BlockID] {
			withdrawnCount++
			continue
		}
		live++
		block := ""
		if record.BlockMembership != nil {
			block = record.BlockMembership.PrimaryBlockID
		}
		blockOf[record.BlockID] = block
		if block == "" {
			continue
		}
		item, ok := stats[block]
		if !ok {
			item = &BlockReport{BlockID: block}
			stats[block] = item
		}
		item.Records++
		if hasUserLabel(record.Metadata) {
			item.Labeled++
			labeledLive++
		}
	}

	type pair struct{ from, to string }
	pairs := map[pair]*CrossBlockEdge{}
	linked := map[string]bool{}
	degreeIn := map[string]int{}
	for _, edge := range edges {
		if edge.EdgeType == "supersedes" {
			continue
		}
		from, to := blockOf[edge.SrcID], blockOf[edge.DstID]
		if from == "" || to == "" {
			continue
		}
		linked[edge.SrcID] = true
		linked[edge.DstID] = true
		if from == to {
			if item, ok := stats[from]; ok {
				item.LinkedOut++
			}
			continue
		}
		key := pair{from: from, to: to}
		if _, ok := pairs[key]; !ok {
			pairs[key] = &CrossBlockEdge{From: from, To: to, EdgeTypes: map[string]int{}}
		}
		pairs[key].Count++
		pairs[key].EdgeTypes[edge.EdgeType]++
		if item, ok := stats[from]; ok {
			item.CrossLinks++
		}
		if item, ok := stats[to]; ok {
			item.CrossLinks++
		}
		degreeIn[to]++
	}

	blocks := make([]BlockReport, 0, len(stats))
	for _, item := range stats {
		if item.Records > 0 {
			item.LabelShare = float64(item.Labeled) / float64(item.Records)
		}
		item.LinkedIn = degreeIn[item.BlockID]
		blocks = append(blocks, *item)
	}
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].Records > blocks[j].Records })

	cross := make([]CrossBlockEdge, 0, len(pairs))
	for _, item := range pairs {
		cross = append(cross, *item)
	}
	sort.SliceStable(cross, func(i, j int) bool { return cross[i].Count > cross[j].Count })

	orphans := 0
	for id := range blockOf {
		if !linked[id] {
			orphans++
		}
	}
	totals := GraphTotals{
		Records: live, Withdrawn: withdrawnCount, Edges: len(edges),
		CrossEdges: len(cross), Orphans: orphans, BlocksInUse: len(blocks),
	}
	if live > 0 {
		totals.LabelShare = float64(labeledLive) / float64(live)
	}
	if len(blocks) > 1 && live > 0 {
		// How much of the store lives in blocks that reach beyond themselves.
		reachable := 0
		for _, item := range blocks {
			if item.CrossLinks > 0 {
				reachable += item.Records
			}
		}
		totals.CouplingScore = float64(reachable) / float64(live)
	}
	report.Blocks = blocks
	report.CrossEdges = cross
	report.Totals = totals
	if len(cross) == 0 {
		report.Notes = append(report.Notes, "blocks do not link to each other: the store is a stack, not a web")
	} else if totals.CouplingScore < 0.5 {
		report.Notes = append(report.Notes, "less than half of live records live in blocks that reach other blocks")
	}
	if totals.LabelShare < 0.5 {
		report.Notes = append(report.Notes, "most live records carry no metadata labels, so facets cannot select them")
	}
	if orphans > 0 {
		report.Notes = append(report.Notes, "records without any edge are invisible to graph navigation")
	}
	return report, nil
}

// hasUserLabel ignores the reserved `um.*` keys the engine writes itself: they
// say how the record is governed, not what it is about.
func hasUserLabel(metadata map[string]any) bool {
	for key := range metadata {
		if strings.HasPrefix(key, "um.") {
			continue
		}
		return true
	}
	return false
}
