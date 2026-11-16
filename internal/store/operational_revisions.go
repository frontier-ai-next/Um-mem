package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
)

// OperationalRevisions returns the Python-compatible, content-free SQLite
// generation fingerprint. It includes connection generation counters and
// database/WAL file metadata, so an external durable write changes the result.
func (index *Index) OperationalRevisions(ctx context.Context) (map[string]string, error) {
	if index.path == "" {
		return nil, fmt.Errorf("canonical index path is unavailable")
	}
	fileGenerations := make([][]any, 0, 2)
	for _, path := range []string{index.path, index.path + "-wal"} {
		var size, modified int64
		if info, err := os.Stat(path); err == nil {
			size = info.Size()
			modified = info.ModTime().UnixNano()
		}
		fileGenerations = append(fileGenerations, []any{filepath.Ext(path), size, modified})
	}
	var dataVersion, freelistCount, pageCount, totalChanges int64
	for _, item := range []struct {
		query string
		value *int64
	}{
		{"PRAGMA data_version", &dataVersion},
		{"PRAGMA freelist_count", &freelistCount},
		{"PRAGMA page_count", &pageCount},
		{"SELECT total_changes()", &totalChanges},
	} {
		if err := index.db.QueryRowContext(ctx, item.query).Scan(item.value); err != nil {
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("SQLite generation counter %s is missing", item.query)
			}
			return nil, err
		}
	}
	payload, err := jsoncompat.MarshalPython(map[string]any{
		"data_version": dataVersion, "file_generations": fileGenerations,
		"freelist_count": freelistCount, "page_count": pageCount,
		"total_changes": totalChanges,
	}, true)
	if err != nil {
		return nil, err
	}
	revision := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	return map[string]string{
		"schema_revision":           fmt.Sprintf("um-mem-backend-index-schema:%d", IndexSchemaVersion),
		"index_revision":            revision,
		"canonical_snapshot_sha256": revision,
	}, nil
}
