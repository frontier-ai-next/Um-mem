package mcpserver

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func recordPackedRender(ctx context.Context, selected *engine.Engine,
	records []memory.MemoryRecord, packed memory.PackedEvidencePayload) error {
	weights := make(map[string]int)
	order := make([]string, 0, len(packed.Items))
	weightTotal := 0
	for _, item := range packed.Items {
		metadata, _ := item["metadata"].(map[string]any)
		id, _ := metadata["block_id"].(string)
		if id == "" {
			id, _ = item["source"].(string)
		}
		if id == "" {
			continue
		}
		encoded, err := jsoncompat.MarshalPython(item, false)
		if err != nil {
			return err
		}
		weight := max(1, memory.EstimateTokens(string(encoded)))
		if _, exists := weights[id]; !exists {
			order = append(order, id)
		}
		weights[id] += weight
		weightTotal += weight
	}
	if weightTotal == 0 {
		return nil
	}
	allocations := make(map[string]int, len(order))
	allocated := 0
	for _, id := range order {
		allocations[id] = packed.EstimatedTokens * weights[id] / weightTotal
		allocated += allocations[id]
	}
	for _, id := range order {
		if allocated >= packed.EstimatedTokens {
			break
		}
		allocations[id]++
		allocated++
	}
	return selected.RecordRenderedTokens(ctx, records, allocations)
}
