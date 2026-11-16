package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/memorymap"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func mapString(value any) string {
	text, _ := value.(string)
	return text
}

func mapBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func mapInt(value any) int {
	switch count := value.(type) {
	case int:
		return count
	case int64:
		return int(count)
	case float64:
		return int(count)
	default:
		return 0
	}
}

func mapStrings(value any) []string {
	result := []string{}
	switch items := value.(type) {
	case []string:
		return append(result, items...)
	case []any:
		for _, item := range items {
			result = append(result, fmt.Sprint(item))
		}
	}
	return result
}

func mapChildren(value any) []map[string]any {
	result := []map[string]any{}
	switch items := value.(type) {
	case []map[string]any:
		return items
	case []any:
		for _, item := range items {
			if node, ok := item.(map[string]any); ok {
				result = append(result, node)
			}
		}
	}
	return result
}

func collectMapTree(nodes []map[string]any, parents []string,
	leaves *[]string, blockCount, metaCount *int) []memorymap.BlockNode {
	result := make([]memorymap.BlockNode, 0, len(nodes))
	for _, node := range nodes {
		name := mapString(node["name"])
		pathNames := append([]string{}, parents...)
		if name != "" {
			pathNames = append(pathNames, name)
		}
		children := collectMapTree(mapChildren(node["children"]), pathNames,
			leaves, blockCount, metaCount)
		id := mapString(node["block_id"])
		if len(children) == 0 && id != "" {
			*leaves = append(*leaves, id)
		}
		*blockCount++
		if mapBool(node["meta"]) {
			*metaCount++
		}
		result = append(result, memorymap.BlockNode{BlockID: id, Name: name,
			Description: mapString(node["description"]), Synopsis: mapString(node["synopsis"]),
			Tags: mapStrings(node["tags"]), Records: mapInt(node["canonical_record_count"]),
			Writable: mapBool(node["writable"]), IndexOnly: mapBool(node["index_only"]),
			Meta: mapBool(node["meta"]), NodeKind: mapString(node["node_kind"]),
			PathNames: pathNames, Children: children})
	}
	return result
}

func metadataText(metadata map[string]any, key string) string {
	if metadata == nil || metadata[key] == nil {
		return ""
	}
	return fmt.Sprint(metadata[key])
}

func mapRecord(record memory.MemoryRecord, blockID string, now time.Time) memorymap.RecordNode {
	steps := mapStrings(record.Metadata["steps"])
	freshness := ""
	if record.Kind == "procedure" {
		freshness = memory.EvaluateProcedureFreshness(record.Metadata, nil, now).Freshness
	}
	return memorymap.RecordNode{RecordID: record.BlockID, Title: record.Title,
		Content: record.Content, Kind: record.Kind, Tags: append([]string{}, record.Tags...),
		Status: record.Status, RecordedAt: record.RecordedAt, Block: blockID,
		Steps: steps, Verify: metadataText(record.Metadata, "verify"), Freshness: freshness}
}

// CollectMemoryMap produces the same bounded block and exact-record snapshot
// used by the on-demand HTML graph. The visual assets live in memorymap.
func (engine *Engine) CollectMemoryMap(ctx context.Context) (memorymap.Data, error) {
	data := memorymap.Data{WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, Backend: engine.config.BackendID,
		Tree: []memorymap.BlockNode{}, Records: []memorymap.RecordNode{},
		Kinds: map[string]int{}, Edges: []memorymap.SemanticEdge{}}
	roots, err := engine.BlockMap(ctx, 5, true, 50)
	if err != nil {
		return data, err
	}
	leaves := []string{}
	data.Tree = collectMapTree(roots, nil, &leaves, &data.BlockCount, &data.MetaBlockCount)
	now := time.Now().UTC()
	for _, blockID := range leaves {
		cursor := ""
		for {
			page, err := engine.ListRecords(ctx, ListRecordsRequest{Scope: "workspace",
				ScopeID: engine.config.WorkspaceID, LogicalBlockID: blockID,
				Limit: 128, Cursor: cursor})
			if err != nil {
				return data, err
			}
			for _, item := range page.Records {
				id := item["record_id"]
				if id == "" {
					continue
				}
				record, err := engine.ReadStored(ctx, id)
				if err != nil {
					return data, err
				}
				if record == nil || record.BlockID != id {
					return data, errors.New("memory map exact record became unavailable")
				}
				data.Records = append(data.Records, mapRecord(*record, blockID, now))
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
	}
	data.RecordCount = len(data.Records)
	for _, record := range data.Records {
		kind := record.Kind
		if kind == "" {
			kind = "untyped"
		}
		data.Kinds[kind]++
	}
	model, indexed, edges, err := engine.memoryMapSemantic(ctx, data.Records)
	if err != nil {
		return data, err
	}
	data.SemanticModel, data.IndexedRecords, data.Edges = model, indexed, edges
	data.GeneratedAt = now.Format("2006-01-02T15:04:05+00:00")
	return data, nil
}

func (engine *Engine) WriteMemoryMap(ctx context.Context, outputPath string) (map[string]any, error) {
	if strings.TrimSpace(outputPath) == "" {
		outputPath = filepath.Join(engine.config.RepoRoot, "memory-map.html")
	}
	data, err := engine.CollectMemoryMap(ctx)
	if err != nil {
		return nil, err
	}
	page, err := memorymap.Render(data, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	written, err := memorymap.Write(outputPath, page)
	if err != nil {
		return nil, err
	}
	summary, err := memorymap.Summary(data)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "workspace_id": engine.config.WorkspaceID,
		"path": written, "summary": summary}, nil
}
