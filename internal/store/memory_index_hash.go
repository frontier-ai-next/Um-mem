package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

type indexRowsQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func indexRows(ctx context.Context, queryer indexRowsQueryer, query, workspaceID string) ([][]any, error) {
	rows, err := queryer.QueryContext(ctx, query, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([][]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		for i, value := range values {
			if raw, ok := value.([]byte); ok {
				values[i] = string(raw)
			}
		}
		result = append(result, values)
	}
	return result, rows.Err()
}

// memoryIndexSHA256 matches the Python index digest over one SQLite snapshot.
// The result identifies derived index contents without returning any rows.
func memoryIndexSHA256(ctx context.Context, queryer indexRowsQueryer, workspaceID string) (string, error) {
	queries := []struct{ name, sql string }{
		{"exact", `SELECT term_type,term_value,block_id FROM memory_exact_terms
			WHERE workspace_id=? ORDER BY term_type,term_value,block_id`},
		{"temporal", `SELECT block_id,canonical_key_sha256,revision_id,valid_from,valid_to,
			recorded_at,is_current FROM memory_temporal_index WHERE workspace_id=? ORDER BY block_id`},
		{"provenance", `SELECT derived_id,raw_source_ref,block_id FROM memory_provenance_links
			WHERE workspace_id=? ORDER BY derived_id,raw_source_ref,block_id`},
		{"memberships", `SELECT record_id,record_revision_id,membership_revision_id,
			logical_block_id,role,ordinal,catalog_revision_id,backend_partition_id,
			valid_from,valid_to,is_current FROM record_block_memberships
			WHERE workspace_id=? ORDER BY record_id,ordinal,logical_block_id`},
		{"text", `SELECT block_id,title,content,tags FROM memory_fts
			WHERE workspace_id=? ORDER BY block_id`},
		{"nodes", `SELECT node_id,payload_json FROM index_nodes
			WHERE workspace_id=? AND node_type IN ('project_structure','block_descriptor','exact_facet')
			ORDER BY node_id`},
	}
	payload := make(map[string]any, len(queries))
	for _, query := range queries {
		rows, err := indexRows(ctx, queryer, query.sql, workspaceID)
		if err != nil {
			return "", fmt.Errorf("read %s memory index: %w", query.name, err)
		}
		payload[query.name] = rows
	}
	encoded, err := jsoncompat.MarshalPython(payload, false)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// MemoryIndexSHA256 returns the selected workspace's derived-index checksum.
func (index *Index) MemoryIndexSHA256(ctx context.Context) (string, error) {
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	sum, err := memoryIndexSHA256(ctx, tx, index.workspaceID)
	if err != nil {
		return "", err
	}
	return sum, tx.Commit()
}
