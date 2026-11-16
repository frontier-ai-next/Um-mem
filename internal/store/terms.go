package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"golang.org/x/text/unicode/norm"
)

func exactQueryGroups(plan memory.RecallPlan) map[string][]string {
	groups := map[string][]string{}
	for _, field := range []struct {
		kind   string
		values []string
	}{
		{"path", plan.Paths}, {"symbol", plan.Symbols}, {"entity", plan.Entities},
		{"config_key", plan.ConfigKeys}, {"endpoint", plan.Endpoints},
		{"artifact_hash", plan.ArtifactHashes},
	} {
		if len(field.values) > 0 {
			groups[field.kind] = field.values
		}
	}
	for kind, values := range plan.Facets {
		if len(values) > 0 {
			groups[kind] = values
		}
	}
	return groups
}

func (index *Index) resolveExactIDsTx(ctx context.Context, tx *sql.Tx, plan memory.RecallPlan) ([]string, error) {
	groups := exactQueryGroups(plan)
	if len(groups) == 0 {
		if len(plan.RecordIDs) == 0 {
			return nil, errors.New("exact recall requires record_ids or exact terms")
		}
		return plan.RecordIDs, nil
	}
	selected := make(map[string]bool)
	first := true
	types := make([]string, 0, len(groups))
	for kind := range groups {
		types = append(types, kind)
	}
	sort.Strings(types)
	for _, kind := range types {
		values := groups[kind]
		args := make([]any, 0, len(values)+2)
		args = append(args, index.workspaceID, kind)
		placeholders := make([]string, 0, len(values))
		for _, raw := range values {
			placeholders = append(placeholders, "?")
			args = append(args, norm.NFC.String(strings.TrimSpace(raw)))
		}
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT block_id FROM memory_exact_terms
			WHERE workspace_id=? AND term_type=? AND term_value IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		matched := make(map[string]bool)
		for rows.Next() {
			var recordID string
			if err := rows.Scan(&recordID); err != nil {
				rows.Close()
				return nil, err
			}
			if first || selected[recordID] {
				matched[recordID] = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		selected = matched
		first = false
		if len(selected) == 0 {
			break
		}
	}
	if len(plan.RecordIDs) > 0 {
		ordered := make([]string, 0, len(plan.RecordIDs))
		for _, recordID := range plan.RecordIDs {
			if selected[recordID] {
				ordered = append(ordered, recordID)
			}
		}
		return ordered, nil
	}
	ordered := make([]string, 0, len(selected))
	for recordID := range selected {
		ordered = append(ordered, recordID)
	}
	sort.Strings(ordered)
	return ordered, nil
}
