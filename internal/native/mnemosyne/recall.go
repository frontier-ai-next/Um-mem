package mnemosyne

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type recallPartition struct {
	id             string
	primaryBlockID string
	identity       memory.NativePartitionIdentity
	records        map[string]store.ProjectionRow
	path           string
}

type scoredID struct {
	id    string
	score float64
}

func ftsQuery(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return ""
	}
	if len(words) > 32 {
		words = words[:32]
	}
	for i, word := range words {
		words[i] = `"` + strings.ReplaceAll(word, `"`, `""`) + `"`
	}
	return strings.Join(words, " OR ")
}

func searchNative(ctx context.Context, db *sql.DB, partition recallPartition, query string, limit int) ([]nativeRow, error) {
	all, err := readNativeRows(ctx, db, "um_mem:block:"+partition.primaryBlockID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]nativeRow, len(all))
	for _, row := range all {
		if _, duplicate := byID[row.ID]; duplicate {
			return nil, errors.New("Mnemosyne native ID is ambiguous across tiers")
		}
		byID[row.ID] = row
	}
	currentIDs := make(map[string]bool, len(partition.records))
	matchedRecords := make(map[string]bool, len(partition.records))
	for _, row := range all {
		var identity struct {
			BlockID string `json:"block_id"`
		}
		if json.Unmarshal([]byte(row.Metadata), &identity) != nil {
			continue
		}
		projection, current := partition.records[identity.BlockID]
		if !current || verifyNativePayload(row, projection.Record, projection.Revision, partition.id, projection.CanonicalJSON) != nil {
			continue
		}
		if matchedRecords[identity.BlockID] {
			return nil, errors.New("Mnemosyne current native projection is ambiguous")
		}
		matchedRecords[identity.BlockID] = true
		currentIDs[row.ID] = true
	}
	if len(matchedRecords) != len(partition.records) {
		return nil, errors.New("Mnemosyne current native projection is unavailable")
	}
	match := ftsQuery(query)
	if match == "" {
		return recentNative(ctx, db, partition, byID, currentIDs, limit)
	}
	now := time.Now().Format("2006-01-02T15:04:05.999999")
	queries := []string{
		`SELECT f.id,bm25(fts_working) FROM fts_working f JOIN working_memory w ON w.id=f.id
		WHERE fts_working MATCH ? AND w.session_id=? AND w.source=?
		AND (w.valid_until IS NULL OR w.valid_until>?) AND w.superseded_by IS NULL
		ORDER BY bm25(fts_working)`,
		`SELECT e.id,bm25(fts_episodes) FROM fts_episodes f JOIN episodic_memory e ON e.rowid=f.rowid
		WHERE fts_episodes MATCH ? AND e.session_id=? AND e.source=?
		AND (e.valid_until IS NULL OR e.valid_until>?) AND e.superseded_by IS NULL
		ORDER BY bm25(fts_episodes)`,
	}
	results := make([]scoredID, 0)
	seen := make(map[string]bool)
	for _, statement := range queries {
		collected := 0
		rows, err := db.QueryContext(ctx, statement, match, partition.id,
			"um_mem:block:"+partition.primaryBlockID, now)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var candidate scoredID
			if err := rows.Scan(&candidate.id, &candidate.score); err != nil {
				rows.Close()
				return nil, err
			}
			if currentIDs[candidate.id] && !seen[candidate.id] {
				seen[candidate.id] = true
				results = append(results, candidate)
				collected++
				if collected >= limit {
					break
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	if len(results) == 0 {
		return recentNative(ctx, db, partition, byID, currentIDs, limit)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score < results[j].score
		}
		return results[i].id < results[j].id
	})
	selected := make([]nativeRow, 0, min(limit, len(results)))
	for _, candidate := range results {
		if row, ok := byID[candidate.id]; ok {
			selected = append(selected, row)
		}
		if len(selected) >= limit {
			break
		}
	}
	return selected, nil
}

func recentNative(ctx context.Context, db *sql.DB, partition recallPartition, byID map[string]nativeRow, currentIDs map[string]bool, limit int) ([]nativeRow, error) {
	now := time.Now().Format("2006-01-02T15:04:05.999999")
	rows, err := db.QueryContext(ctx, `SELECT id FROM (
		SELECT id,timestamp FROM working_memory WHERE session_id=? AND source=?
		AND (valid_until IS NULL OR valid_until>?) AND superseded_by IS NULL
		UNION ALL SELECT id,timestamp FROM episodic_memory WHERE session_id=? AND source=?
		AND (valid_until IS NULL OR valid_until>?) AND superseded_by IS NULL
	) ORDER BY timestamp DESC,id`,
		partition.id, "um_mem:block:"+partition.primaryBlockID, now,
		partition.id, "um_mem:block:"+partition.primaryBlockID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]nativeRow, 0, limit)
	seen := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if row, ok := byID[id]; ok && currentIDs[id] && !seen[id] {
			seen[id] = true
			result = append(result, row)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, rows.Err()
}

func (p Projector) Recall(ctx context.Context, index *store.Index, plan memory.RecallPlan) ([]memory.MemoryRecord, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	workspace, profile := index.Scope()
	if plan.WorkspaceID != workspace || plan.ProfileID != profile {
		return nil, errors.New("Mnemosyne recall scope differs from canonical index")
	}
	if plan.RetrievalMode == "hybrid" {
		return nil, errors.New("hybrid retrieval is unsupported by Mnemosyne")
	}
	if plan.TemporalMode != "current" || plan.AsOf != "" || plan.KnownAsOf != "" ||
		plan.RevisionID != "" || plan.HasExactSelectors() {
		return index.RecallCanonical(ctx, plan, "mnemosyne")
	}
	if !plan.SearchAll && len(plan.LogicalBlockIDs) == 0 {
		return nil, errors.New("Mnemosyne native recall requires logical_block_ids or search_all=true")
	}
	projected, err := index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	partitions := make(map[string]*recallPartition)
	historical := make(map[string]store.ProjectionRow)
	for _, row := range projected {
		record := row.Record
		if record.BlockMembership == nil {
			continue
		}
		if !row.Current {
			if row.Backend == "mnemosyne" && row.Status == "committed" &&
				row.NativeID != "" && row.Revision >= 1 {
				historical[record.BlockID] = row
			}
			continue
		}
		primary := record.BlockMembership.PrimaryBlockID
		if !plan.SearchAll && !slices.Contains(plan.LogicalBlockIDs, primary) {
			continue
		}
		if row.Backend != "mnemosyne" || row.Status != "committed" || row.NativeID == "" || row.Revision < 1 {
			return nil, fmt.Errorf("Mnemosyne native projection is not current for block %q", primary)
		}
		id, err := PartitionID(record)
		if err != nil {
			return nil, err
		}
		partition := partitions[id]
		if partition == nil {
			path, err := p.partitionPath(record)
			if err != nil {
				return nil, err
			}
			identity, err := p.PartitionIdentity(record)
			if err != nil {
				return nil, err
			}
			partition = &recallPartition{id: id, primaryBlockID: primary,
				identity: identity, records: make(map[string]store.ProjectionRow), path: path}
			partitions[id] = partition
		}
		partition.records[record.BlockID] = row
	}
	ordered := make([]*recallPartition, 0, len(partitions))
	for _, partition := range partitions {
		ordered = append(ordered, partition)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if !plan.SearchAll {
			a, b := slices.Index(plan.LogicalBlockIDs, left.primaryBlockID), slices.Index(plan.LogicalBlockIDs, right.primaryBlockID)
			if a != b {
				return a < b
			}
		}
		return left.id < right.id
	})
	limit := plan.Limit
	if limit == 0 {
		limit = 10
	}
	pools := make([][]memory.MemoryRecord, 0, len(ordered))
	for _, partition := range ordered {
		if _, err := os.Stat(partition.path); err != nil {
			return nil, errors.New("Mnemosyne native partition is unavailable")
		}
		db, err := sql.Open("sqlite", partition.path)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		started := time.Now()
		rows, err := searchNative(ctx, db, *partition, plan.Query, max(64, limit*4))
		mode := "scoped"
		if plan.SearchAll {
			mode = "search_all"
		}
		metricErr := index.RecordPartitionOperation(ctx, partition.identity, store.PartitionOperation{
			Operation: "recall", LatencyMS: float64(time.Since(started).Microseconds()) / 1000,
			HitCount: len(rows), Success: err == nil, SearchMode: mode})
		db.Close()
		if err != nil || metricErr != nil {
			return nil, errors.Join(err, metricErr)
		}
		pool := make([]memory.MemoryRecord, 0, min(len(rows), limit))
		seenRecordIDs := make(map[string]bool)
		for _, native := range rows {
			var identity struct {
				BlockID string `json:"block_id"`
			}
			if err := json.Unmarshal([]byte(native.Metadata), &identity); err != nil || identity.BlockID == "" {
				return nil, errors.New("Mnemosyne native row has invalid canonical identity")
			}
			if seenRecordIDs[identity.BlockID] {
				return nil, errors.New("Mnemosyne native record identity is ambiguous")
			}
			seenRecordIDs[identity.BlockID] = true
			projection, ok := partition.records[identity.BlockID]
			if !ok {
				if old, known := historical[identity.BlockID]; known {
					if old.NativeID != native.ID ||
						old.Record.BlockMembership.PrimaryBlockID != partition.primaryBlockID {
						return nil, errors.New("Mnemosyne historical native row differs from canonical projection")
					}
					if err := verifyNative(native, old.Record, old.Revision, partition.id); err != nil {
						return nil, err
					}
					continue
				}
				return nil, errors.New("Mnemosyne native row has no committed canonical projection")
			}
			canonical := projection.Record
			if err := verifyNative(native, canonical, projection.Revision, partition.id); err != nil {
				return nil, err
			}
			stored, err := index.ReadStoredRecord(ctx, canonical.BlockID)
			if err != nil {
				return nil, err
			}
			if stored == nil || !reflect.DeepEqual(*stored, canonical) {
				return nil, errors.New("Mnemosyne native row differs from canonical index")
			}
			current, err := index.IsCurrentRecord(ctx, canonical)
			if err != nil {
				return nil, err
			}
			if !current || !memory.EligibleForRecall(canonical, plan, time.Now().UTC()) {
				continue
			}
			if !slices.ContainsFunc(plan.Tags, func(tag string) bool { return !slices.Contains(canonical.Tags, tag) }) {
				dependencies, err := index.ValidateDependencies(ctx, canonical.BlockID, store.DependencyOptions{})
				if err != nil {
					return nil, err
				}
				if dependencies.Status == "valid" {
					pool = append(pool, canonical)
				}
			}
			if len(pool) >= limit {
				break
			}
		}
		pools = append(pools, pool)
	}
	result := make([]memory.MemoryRecord, 0, limit)
	seen := make(map[string]bool)
	for rank := 0; len(result) < limit; rank++ {
		found := false
		for _, pool := range pools {
			if rank >= len(pool) {
				continue
			}
			found = true
			record := pool[rank]
			if !seen[record.BlockID] {
				seen[record.BlockID] = true
				result = append(result, record)
			}
			if len(result) >= limit {
				break
			}
		}
		if !found {
			break
		}
	}
	return result, nil
}
