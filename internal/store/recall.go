package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/frontier-ai-next/Um-mem/memory"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func recallTokens(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_'
	})
}

func distinctTokens(tokens []string) []string {
	seen := make(map[string]bool, len(tokens))
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if !seen[token] {
			seen[token] = true
			result = append(result, token)
		}
	}
	return result
}

func (index *Index) canonicalCandidateIDs(ctx context.Context, query string, limit int) ([]string, map[string]float64, string, error) {
	if query != "" {
		tokens := distinctTokens(recallTokens(query))
		if len(tokens) == 0 {
			return []string{}, nil, "sqlite_fts5_bm25", nil
		}
		terms := make([]string, 0, len(tokens))
		for _, token := range tokens {
			terms = append(terms, `"`+strings.ReplaceAll(token, `"`, `""`)+`"`)
		}
		rows, err := index.db.QueryContext(ctx, `SELECT block_id,bm25(memory_fts) FROM memory_fts
			WHERE memory_fts MATCH ? AND workspace_id=?
			ORDER BY bm25(memory_fts), rowid DESC LIMIT ?`, strings.Join(terms, " OR "), index.workspaceID, limit)
		if err != nil {
			var sqliteError *sqlite.Error
			if !errors.As(err, &sqliteError) || sqliteError.Code() != sqlite3.SQLITE_ERROR {
				return nil, nil, "", fmt.Errorf("canonical FTS search: %w", err)
			}
			ids, _, _, fallbackErr := index.canonicalCandidateIDs(ctx, "", min(limit, 128))
			return ids, nil, "recent_substring_fallback", fallbackErr
		}
		defer rows.Close()
		ids := make([]string, 0)
		scores := make(map[string]float64)
		for rows.Next() {
			var id string
			var score float64
			if err := rows.Scan(&id, &score); err != nil {
				return nil, nil, "", err
			}
			ids = append(ids, id)
			scores[id] = score
		}
		return ids, scores, "sqlite_fts5_bm25", rows.Err()
	}
	rows, err := index.db.QueryContext(ctx, `SELECT temporal.block_id FROM memory_temporal_index AS temporal
		JOIN blocks ON blocks.block_id=temporal.block_id
		WHERE temporal.workspace_id=? ORDER BY temporal.recorded_at DESC,temporal.block_id LIMIT ?`, index.workspaceID, limit)
	if err != nil {
		return nil, nil, "", err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, nil, "", err
		}
		ids = append(ids, id)
	}
	return ids, nil, "", rows.Err()
}

func estimatedTokens(value string) int {
	if value == "" {
		return 0
	}
	return max(1, utf8.RuneCountInString(value)/4)
}

func recordTokenCost(record memory.MemoryRecord) int {
	parts := make([]string, 0, 2)
	if record.Title != "" {
		parts = append(parts, record.Title)
	}
	if record.Content != "" {
		parts = append(parts, record.Content)
	}
	return estimatedTokens(strings.Join(parts, "\n"))
}

func truncateRecord(record memory.MemoryRecord, budget int) (memory.MemoryRecord, bool) {
	if budget <= 0 {
		return record, false
	}
	original := recordTokenCost(record)
	if original <= budget {
		return record, true
	}
	if record.Content == "" {
		record.Content, record.Title = record.Title, ""
	}
	if record.Title != "" && estimatedTokens(record.Title+"\n") >= budget {
		record.Title = ""
	}
	runes := []rune(record.Content)
	low, high := 0, len(runes)
	for low < high {
		middle := (low + high + 1) / 2
		probe := record
		probe.Content = string(runes[:middle])
		if recordTokenCost(probe) <= budget {
			low = middle
		} else {
			high = middle - 1
		}
	}
	if low == 0 {
		return record, false
	}
	record.Content = string(runes[:low])
	if record.Metadata == nil {
		record.Metadata = make(map[string]any)
	}
	record.Metadata["retrieval_truncated"] = true
	record.Metadata["retrieval_original_tokens"] = original
	record.Metadata["retrieval_visible_tokens"] = recordTokenCost(record)
	return record, true
}

// FitRecallTokenBudget applies the same packing rule to native-ranked results.
func FitRecallTokenBudget(records []memory.MemoryRecord, budget int) []memory.MemoryRecord {
	if budget <= 0 {
		return records
	}
	result := make([]memory.MemoryRecord, 0, len(records))
	for _, record := range records {
		cost := recordTokenCost(record)
		if cost > budget {
			if len(result) > 0 {
				continue
			}
			var ok bool
			record, ok = truncateRecord(record, budget)
			if !ok {
				continue
			}
			cost = recordTokenCost(record)
		}
		result = append(result, record)
		budget -= cost
	}
	return result
}

// RecallCanonical searches the immutable SQLite index for historical or exact
// queries. Current broad semantic recall belongs to the selected native backend.
func (index *Index) RecallCanonical(ctx context.Context, plan memory.RecallPlan, backendID string) ([]memory.MemoryRecord, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if plan.WorkspaceID != index.workspaceID || plan.ProfileID != index.profileID {
		return nil, errors.New("canonical recall scope does not match selected index")
	}
	if plan.RetrievalMode == "hybrid" {
		return nil, errors.New("hybrid retrieval is unsupported by canonical SQLite recall")
	}
	limit := plan.Limit
	if limit == 0 {
		limit = 10
	}
	scanLimit := max(limit, 2000)
	query := strings.TrimSpace(strings.ToLower(plan.Query))
	var ids []string
	var bm25 map[string]float64
	channel := ""
	var err error
	if plan.HasExactSelectors() {
		// The exact index owns this candidate set; text only affects its rank.
		selectedPlan := plan
		selectedPlan.Limit = scanLimit
		selected, err := index.ReadExactRecords(ctx, selectedPlan)
		if err != nil {
			return nil, err
		}
		return rankCanonicalRecords(selected, plan, backendID, "exact_inverted_index", nil), nil
	}
	ids, bm25, channel, err = index.canonicalCandidateIDs(ctx, query, scanLimit)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []memory.MemoryRecord{}, nil
	}
	selectedPlan := plan
	selectedPlan.RecordIDs = ids
	selectedPlan.Limit = scanLimit
	selected, err := index.ReadExactRecords(ctx, selectedPlan)
	if err != nil {
		return nil, err
	}
	return rankCanonicalRecords(selected, plan, backendID, channel, bm25), nil
}

func rankCanonicalRecords(records []memory.MemoryRecord, plan memory.RecallPlan, backendID, channel string, bm25 map[string]float64) []memory.MemoryRecord {
	tokens := recallTokens(plan.Query)
	type scored struct {
		record  memory.MemoryRecord
		score   int
		matched []string
	}
	ordered := make([]scored, 0, len(records))
	for _, record := range records {
		haystack := strings.ToLower(record.Content + " " + record.Title + " " + strings.Join(record.Tags, " "))
		matched := make([]string, 0)
		for _, token := range tokens {
			if strings.Contains(haystack, token) {
				matched = append(matched, token)
			}
		}
		if len(tokens) > 0 && len(matched) == 0 && !plan.HasExactSelectors() {
			continue
		}
		score := len(matched)
		if plan.HasExactSelectors() && len(tokens) > 0 {
			score = max(score, len(tokens), 1)
		}
		ordered = append(ordered, scored{record, score, matched})
	}
	slices.SortStableFunc(ordered, func(a, b scored) int { return b.score - a.score })
	limit := plan.Limit
	if limit == 0 {
		limit = 10
	}
	result := make([]memory.MemoryRecord, 0, min(limit, len(ordered)))
	usedTokens := 0
	for _, item := range ordered {
		record := item.record
		if record.Metadata == nil {
			record.Metadata = make(map[string]any)
		}
		if plan.AsOf != "" && record.ValidFrom == "" {
			record.Metadata["event_time_status"] = "unknown"
		}
		rank := len(result) + 1
		score := 1.0 / float64(rank)
		if len(tokens) > 0 {
			score = float64(item.score) / float64(len(tokens))
		}
		record.Metadata["retrieval_backend"] = backendID
		record.Metadata["retrieval_rank"] = rank
		record.Metadata["retrieval_score"] = score
		record.Metadata["retrieval_score_raw"] = item.score
		record.Metadata["retrieval_matched_terms"] = item.matched[:min(20, len(item.matched))]
		if channel != "" {
			record.Metadata["retrieval_channel"] = channel
		}
		if channel == "recent_substring_fallback" {
			record.Metadata["retrieval_scan_limit"] = min(max(plan.Limit, 2000), 128)
		}
		if value, ok := bm25[record.BlockID]; ok {
			record.Metadata["retrieval_bm25"] = value
		}
		cost := recordTokenCost(record)
		if plan.MaxTokens > 0 && usedTokens+cost > plan.MaxTokens {
			if len(result) != 0 {
				continue
			}
			var okay bool
			record, okay = truncateRecord(record, plan.MaxTokens-usedTokens)
			if !okay {
				continue
			}
			cost = recordTokenCost(record)
		}
		result = append(result, record)
		usedTokens += cost
		if len(result) == limit {
			break
		}
	}
	return result
}
