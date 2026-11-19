package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ExportJSONL writes every record and edge as one JSON document per line. The
// point is portability: a store that cannot leave its own database is a store
// that dies with the runtime. Retracted records are included by default so an
// export keeps history rather than only the current view.
func (engine *Engine) ExportJSONL(ctx context.Context, path string, currentOnly bool) (map[string]any, error) {
	target := strings.TrimSpace(path)
	if target == "" {
		return nil, errors.New("export requires an output path")
	}
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	file, err := os.Create(target)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	writer := bufio.NewWriterSize(file, 1<<20)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)

	withdrawn := retractedTargets(records, edges)
	recordLines, edgeLines := 0, 0
	for _, record := range records {
		if currentOnly && (record.Status == "retracted" || record.Status == "superseded" || withdrawn[record.BlockID]) {
			continue
		}
		payload := map[string]any{"kind": "record", "withdrawn": withdrawn[record.BlockID], "record": record}
		if err := encoder.Encode(payload); err != nil {
			return nil, err
		}
		recordLines++
	}
	for _, edge := range edges {
		if err := encoder.Encode(map[string]any{"kind": "edge", "edge": edge}); err != nil {
			return nil, err
		}
		edgeLines++
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	info, _ := file.Stat()
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	return map[string]any{
		"ok": true, "path": target, "records": recordLines, "edges": edgeLines,
		"bytes": size, "current_only": currentOnly,
		"workspace_id": engine.config.WorkspaceID, "backend": engine.config.BackendID,
	}, nil
}
