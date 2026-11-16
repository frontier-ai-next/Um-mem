package store

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// ReadEvidenceRecord resolves one exact evidence reference among visible
// canonical records in this workspace and profile. Ambiguous references fail
// closed instead of selecting whichever row SQLite returns first.
func (index *Index) ReadEvidenceRecord(ctx context.Context, evidenceRef string) (*memory.MemoryRecord, error) {
	evidenceRef = strings.TrimSpace(evidenceRef)
	if evidenceRef == "" {
		return nil, errors.New("evidence reference is required")
	}
	rows, err := index.db.QueryContext(ctx, `
		SELECT block_id FROM blocks
		WHERE workspace_id = ? AND (
			EXISTS (SELECT 1 FROM json_each(payload_json, '$.evidence_refs') AS ref
				WHERE CASE WHEN json_valid(ref.value) THEN
					json_extract(ref.value, '$.ref_id') = ? OR json_extract(ref.value, '$.hash') = ?
				ELSE 0 END)
			OR EXISTS (SELECT 1 FROM json_each(payload_json, '$.metadata.source_segments') AS segment
				WHERE CASE WHEN json_valid(segment.value) THEN
					json_extract(segment.value, '$.source_id') = ? OR json_extract(segment.value, '$.sha256') = ?
				ELSE 0 END)
		)
		ORDER BY block_id`, index.workspaceID, evidenceRef, evidenceRef, evidenceRef, evidenceRef)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var found *memory.MemoryRecord
	for _, id := range ids {
		record, err := index.ReadStoredRecord(ctx, id)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		if found != nil {
			return nil, errors.New("ambiguous_evidence_ref")
		}
		found = record
	}
	if found == nil {
		return nil, errors.New("exact evidence reference was not found")
	}
	return found, nil
}
