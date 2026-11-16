package mempalace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
	"github.com/gofrs/flock"
)

type recallCollection struct {
	name           string
	primary        string
	partitionID    string
	identity       memory.NativePartitionIdentity
	representative memory.MemoryRecord
	projections    map[string]store.ProjectionRow
}

type rankedRecord struct {
	record  memory.MemoryRecord
	primary string
	score   float64
	rank    int
}

func (p *Projector) Recall(ctx context.Context, index *store.Index, plan memory.RecallPlan) ([]memory.MemoryRecord, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	workspaceID, profileID := index.Scope()
	if plan.WorkspaceID != workspaceID || plan.ProfileID != profileID {
		return nil, errors.New("MemPalace recall scope differs from canonical index")
	}
	if plan.TemporalMode != "current" || plan.AsOf != "" || plan.KnownAsOf != "" ||
		plan.RevisionID != "" || plan.HasExactSelectors() || strings.TrimSpace(plan.Query) == "" {
		return index.RecallCanonical(ctx, plan, "mempalace")
	}
	if !plan.SearchAll && len(plan.LogicalBlockIDs) == 0 {
		return nil, errors.New("MemPalace native recall requires logical_block_ids or search_all=true")
	}
	if p == nil || p.Embedder == nil || p.tokenizer == nil {
		return nil, errors.New("MemPalace native model is unavailable")
	}
	rows, err := index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	collections := make(map[string]*recallCollection)
	for _, row := range rows {
		record := row.Record
		if !row.Current || record.BlockMembership == nil {
			continue
		}
		primary := record.BlockMembership.PrimaryBlockID
		if !plan.SearchAll && !slices.Contains(plan.LogicalBlockIDs, primary) {
			continue
		}
		if row.Backend != StoreForBlock(record) || row.Status != "committed" ||
			row.Revision < 1 || row.NativeID != record.BlockID {
			return nil, fmt.Errorf("MemPalace native projection is not current for block %q", primary)
		}
		partition, err := Partition(record)
		if err != nil {
			return nil, err
		}
		name, err := CollectionName(record)
		if err != nil {
			return nil, err
		}
		group := collections[name]
		if group == nil {
			group = &recallCollection{name: name, primary: primary,
				partitionID: partition.PartitionID, identity: memory.NativePartitionIdentity(partition),
				representative: record,
				projections:    make(map[string]store.ProjectionRow)}
			collections[name] = group
		}
		if group.primary != primary || group.partitionID != partition.PartitionID {
			return nil, errors.New("MemPalace native collection contains mixed logical partitions")
		}
		group.projections[record.BlockID] = row
	}
	if len(collections) == 0 {
		return []memory.MemoryRecord{}, nil
	}
	ordered := make([]*recallCollection, 0, len(collections))
	for _, group := range collections {
		ordered = append(ordered, group)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !plan.SearchAll {
			li := slices.Index(plan.LogicalBlockIDs, ordered[i].primary)
			ri := slices.Index(plan.LogicalBlockIDs, ordered[j].primary)
			if li != ri {
				return li < ri
			}
		}
		return ordered[i].name < ordered[j].name
	})
	vectors, err := p.Embedder.Embed(ctx, []string{plan.Query})
	if err != nil {
		return nil, err
	}
	lockPath, err := operationLockPath(p.RepoRoot, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}
	lock := flock.New(lockPath)
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("MemPalace native operation lock timed out")
	}
	defer lock.Unlock()
	client, err := OpenNativeClient(p.RepoRoot, workspaceID)
	if err != nil {
		return nil, err
	}
	results, searchErr := p.searchCollections(ctx, client, index, plan, ordered, vectors[0])
	closeErr := client.Close()
	if searchErr != nil {
		return nil, searchErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return results, nil
}

func (p *Projector) searchCollections(ctx context.Context, client *NativeClient, index *store.Index,
	plan memory.RecallPlan, groups []*recallCollection, vector []float32) ([]memory.MemoryRecord, error) {
	limit := plan.Limit
	if limit == 0 {
		limit = 10
	}
	candidateLimit := min(64, max(limit+1, limit*4))
	if plan.RetrievalMode == "hybrid" {
		candidateLimit = 64
	}
	semantic := make([]rankedRecord, 0)
	lexical := make([]rankedRecord, 0)
	mode := "scoped"
	if plan.SearchAll {
		mode = "search_all"
	}
	for _, group := range groups {
		started := time.Now()
		hits, err := client.Query(ctx, group.representative, vector, candidateLimit)
		var fullRows []NativeRow
		if err == nil {
			ids := make([]string, len(hits))
			for i, hit := range hits {
				ids[i] = hit.ID
			}
			fullRows, err = client.Get(ctx, group.representative, ids)
		}
		metricErr := index.RecordPartitionOperation(ctx, group.identity, store.PartitionOperation{
			Operation: "recall", LatencyMS: float64(time.Since(started).Microseconds()) / 1000,
			HitCount: len(hits), Success: err == nil, SearchMode: mode})
		if err != nil || metricErr != nil {
			return nil, errors.Join(err, metricErr)
		}
		byID := make(map[string]NativeRow, len(fullRows))
		for _, row := range fullRows {
			byID[row.ID] = row
		}
		seen := make(map[string]bool)
		for i, hit := range hits {
			full, ok := byID[hit.ID]
			if !ok || full.Document != hit.Document || !sameNativeMetadata(full.Metadata, hit.Metadata) {
				return nil, errors.New("MemPalace semantic result differs from native collection")
			}
			record, err := p.admitNative(ctx, index, plan, group, full)
			if err != nil {
				return nil, err
			}
			if record == nil || seen[record.BlockID] {
				continue
			}
			seen[record.BlockID] = true
			semantic = append(semantic, rankedRecord{record: *record, primary: group.primary,
				score: 1 - hit.Distance, rank: i + 1})
		}
		if plan.RetrievalMode != "hybrid" {
			continue
		}
		started = time.Now()
		rows, err := client.Lexical(ctx, group.representative, plan.Query, candidateLimit)
		metricErr = index.RecordPartitionOperation(ctx, group.identity, store.PartitionOperation{
			Operation: "recall", LatencyMS: float64(time.Since(started).Microseconds()) / 1000,
			HitCount: len(rows), Success: err == nil, SearchMode: mode})
		if err != nil || metricErr != nil {
			return nil, errors.Join(err, metricErr)
		}
		clear(seen)
		for i, row := range rows {
			record, err := p.admitNative(ctx, index, plan, group, row)
			if err != nil {
				return nil, err
			}
			if record == nil || seen[record.BlockID] {
				continue
			}
			seen[record.BlockID] = true
			lexical = append(lexical, rankedRecord{record: *record, primary: group.primary, rank: i + 1})
		}
	}
	if plan.RetrievalMode == "hybrid" {
		return rankHybrid(semantic, lexical, limit), nil
	}
	return rankSemantic(semantic, limit), nil
}

func (p *Projector) admitNative(ctx context.Context, index *store.Index, plan memory.RecallPlan,
	group *recallCollection, native NativeRow) (*memory.MemoryRecord, error) {
	canonicalID := native.ID
	if _, window := native.Metadata["derived_window"]; window {
		id, ok := native.Metadata["canonical_record_id"].(string)
		if !ok || !strings.HasPrefix(native.ID, id+":window:") {
			return nil, errors.New("MemPalace window identity is invalid")
		}
		canonicalID = id
	}
	projection, ok := group.projections[canonicalID]
	if !ok {
		return nil, nil
	}
	record := projection.Record
	expected, err := NativeRows(record, projection.Revision, p.tokenizer)
	if err != nil {
		return nil, err
	}
	matched := false
	for _, row := range expected {
		if row.ID == native.ID {
			if err := verifyNativeRow(native, row); err != nil {
				return nil, err
			}
			matched = true
			break
		}
	}
	if !matched {
		return nil, errors.New("MemPalace native candidate does not match canonical window")
	}
	stored, err := index.ReadStoredRecord(ctx, record.BlockID)
	if err != nil {
		return nil, err
	}
	if stored == nil || !reflect.DeepEqual(*stored, record) {
		return nil, errors.New("MemPalace native candidate differs from canonical index")
	}
	current, err := index.IsCurrentRecord(ctx, record)
	if err != nil {
		return nil, err
	}
	if !current || !memory.EligibleForRecall(record, plan, time.Now().UTC()) {
		return nil, nil
	}
	for _, tag := range plan.Tags {
		if !slices.Contains(record.Tags, tag) {
			return nil, nil
		}
	}
	dependencies, err := index.ValidateDependencies(ctx, record.BlockID, store.DependencyOptions{})
	if err != nil {
		return nil, err
	}
	if dependencies.Status != "valid" {
		return nil, nil
	}
	return &record, nil
}

func rankSemantic(candidates []rankedRecord, limit int) []memory.MemoryRecord {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].record.BlockID < candidates[j].record.BlockID
	})
	unique := make([]rankedRecord, 0, len(candidates))
	seen := make(map[string]bool)
	for _, hit := range candidates {
		if !seen[hit.record.BlockID] {
			seen[hit.record.BlockID] = true
			unique = append(unique, hit)
		}
	}
	poolIDs := make([]string, len(unique))
	for i, hit := range unique {
		poolIDs[i] = hit.record.BlockID
	}
	selected := unique[:min(limit, len(unique))]
	selectedIDs := poolIDs[:len(selected)]
	result := make([]memory.MemoryRecord, 0, len(selected))
	for _, hit := range selected {
		record := hit.record
		record.Metadata = withRankMetadata(record.Metadata, poolIDs, selectedIDs)
		record.Metadata["native_logical_block_id"] = hit.primary
		record.Metadata["native_rank"] = hit.rank
		record.Metadata["native_score"] = hit.score
		record.Metadata["selection_mode"] = "native_similarity"
		record.Metadata["native_score_mode"] = "cosine_similarity"
		record.Metadata["retrieval_score"] = hit.score
		result = append(result, record)
	}
	return result
}

func rankHybrid(semantic, lexical []rankedRecord, limit int) []memory.MemoryRecord {
	sort.SliceStable(semantic, func(i, j int) bool { return semantic[i].score > semantic[j].score })
	type fused struct {
		record memory.MemoryRecord
		ranks  map[string]int
		score  float64
	}
	byID := make(map[string]*fused)
	for _, channel := range []struct {
		name string
		hits []rankedRecord
	}{{"semantic", semantic}, {"lexical", lexical}} {
		seen := make(map[string]bool)
		rank := 0
		for _, hit := range channel.hits {
			id := hit.record.BlockID
			if seen[id] {
				continue
			}
			seen[id] = true
			rank++
			item := byID[id]
			if item == nil {
				item = &fused{record: hit.record, ranks: make(map[string]int)}
				byID[id] = item
			}
			item.ranks[channel.name] = rank
		}
	}
	ordered := make([]*fused, 0, len(byID))
	for _, item := range byID {
		for _, rank := range item.ranks {
			item.score += 1 / float64(60+rank)
		}
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		return ordered[i].record.BlockID < ordered[j].record.BlockID
	})
	poolIDs := make([]string, len(ordered))
	for i, item := range ordered {
		poolIDs[i] = item.record.BlockID
	}
	selected := ordered[:min(limit, len(ordered))]
	selectedIDs := poolIDs[:len(selected)]
	result := make([]memory.MemoryRecord, 0, len(selected))
	for _, item := range selected {
		record := item.record
		record.Metadata = withRankMetadata(record.Metadata, poolIDs, selectedIDs)
		record.Metadata["retrieval_mode"] = "hybrid"
		record.Metadata["native_logical_block_id"] = record.BlockMembership.PrimaryBlockID
		record.Metadata["selection_mode"] = "rrf"
		record.Metadata["native_score_mode"] = "rrf"
		record.Metadata["rrf_constant"] = 60
		record.Metadata["channel_ranks"] = item.ranks
		record.Metadata["retrieval_score"] = item.score
		result = append(result, record)
	}
	return result
}

func withRankMetadata(metadata map[string]any, poolIDs, selectedIDs []string) map[string]any {
	copy := make(map[string]any, len(metadata)+10)
	for key, value := range metadata {
		copy[key] = value
	}
	copy["candidate_pool_ids"] = poolIDs
	copy["selected_record_ids"] = selectedIDs
	return copy
}
