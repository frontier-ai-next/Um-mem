package engine

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// NeighborRequest walks the record graph around one record.
type NeighborRequest struct {
	RecordID  string
	Depth     int
	EdgeTypes []string
	Limit     int
}

// Neighbor is one record reached through a typed edge.
type Neighbor struct {
	RecordID      string `json:"record_id"`
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	LogicalBlock  string `json:"logical_block_id"`
	Hop           int    `json:"hop"`
	EdgeType      string `json:"edge_type"`
	Direction     string  `json:"direction"`
	Weight        float64 `json:"weight"`
	Reason        string `json:"reason,omitempty"`
}

// Neighbors traverses the public record graph breadth-first. Links have always
// been stored but no read path used them, so a record could be connected and
// still invisible to whoever recalled it. This is that read path: bounded hops,
// bounded results, and the edge that produced each hit.
func (engine *Engine) Neighbors(ctx context.Context, request NeighborRequest) (map[string]any, error) {
	id := strings.TrimSpace(request.RecordID)
	if id == "" {
		return nil, errors.New("neighbors requires a record id")
	}
	depth := request.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > 3 {
		depth = 3
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 12
	}
	if limit > 64 {
		limit = 64
	}

	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]memory.MemoryRecord, len(records))
	for _, record := range records {
		byID[record.BlockID] = record
	}
	start, found := byID[id]
	if !found {
		return nil, errors.New("record not found in the selected workspace and profile")
	}

	allowed := make(map[string]bool, len(request.EdgeTypes))
	for _, edgeType := range request.EdgeTypes {
		if value := strings.TrimSpace(edgeType); value != "" {
			allowed[value] = true
		}
	}
	adjacent := make(map[string][]memory.EdgeRecord, len(records))
	for _, edge := range edges {
		if len(allowed) > 0 && !allowed[edge.EdgeType] {
			continue
		}
		adjacent[edge.SrcID] = append(adjacent[edge.SrcID], edge)
		adjacent[edge.DstID] = append(adjacent[edge.DstID], edge)
	}

	neighbors := make([]Neighbor, 0, limit)
	seen := map[string]bool{start.BlockID: true}
	frontier := []string{start.BlockID}
	for hop := 1; hop <= depth && len(neighbors) < limit && len(frontier) > 0; hop++ {
		next := make([]string, 0, len(frontier))
		for _, current := range frontier {
			for _, edge := range adjacent[current] {
				other, direction := edge.SrcID, "in"
				if edge.SrcID == current {
					other, direction = edge.DstID, "out"
				}
				if other == current || seen[other] {
					continue
				}
				record, ok := byID[other]
				if !ok {
					continue
				}
				seen[other] = true
				next = append(next, other)
				neighbors = append(neighbors, Neighbor{
					RecordID: record.BlockID, Title: record.Title, Kind: record.Kind,
					Status: record.Status, LogicalBlock: logicalBlockOf(record), Hop: hop,
					EdgeType: edge.EdgeType, Direction: direction, Reason: edge.Reason,
					Weight: edgeWeight(edge),
				})
				if len(neighbors) >= limit {
					break
				}
			}
			if len(neighbors) >= limit {
				break
			}
		}
		frontier = next
	}
	sort.SliceStable(neighbors, func(i, j int) bool { return neighbors[i].Hop < neighbors[j].Hop })

	return map[string]any{
		"ok":           true,
		"record_id":    start.BlockID,
		"depth":        depth,
		"count":        len(neighbors),
		"neighbors":    neighbors,
		"workspace_id": engine.config.WorkspaceID,
		"backend":      engine.config.BackendID,
	}, nil
}

func logicalBlockOf(record memory.MemoryRecord) string {
	if record.BlockMembership != nil {
		return record.BlockMembership.PrimaryBlockID
	}
	return record.BlockID
}

func edgeWeight(edge memory.EdgeRecord) float64 {
	if edge.Weight == "" {
		return 1
	}
	value, err := edge.Weight.Float64()
	if err != nil || value <= 0 {
		return 1
	}
	return value
}
