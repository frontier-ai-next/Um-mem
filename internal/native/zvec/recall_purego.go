//go:build purego

package zvec

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type recallPartition struct {
	id       string
	primary  string
	identity memory.NativePartitionIdentity
	path     string
	records  map[string]store.ProjectionRow
}

type rankedHit struct {
	blockID string
	rank    int
	score   float64
	record  memory.MemoryRecord
}

func (p *Projector) Recall(ctx context.Context, index *store.Index, plan memory.RecallPlan) ([]memory.MemoryRecord, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	workspaceID, profileID := index.Scope()
	if plan.WorkspaceID != workspaceID || plan.ProfileID != profileID {
		return nil, errors.New("Zvec recall scope differs from canonical index")
	}
	if plan.TemporalMode != "current" || plan.AsOf != "" || plan.KnownAsOf != "" ||
		plan.RevisionID != "" || plan.HasExactSelectors() || strings.TrimSpace(plan.Query) == "" {
		return index.RecallCanonical(ctx, plan, "zvec")
	}
	if !plan.SearchAll && len(plan.LogicalBlockIDs) == 0 {
		return nil, errors.New("Zvec native recall requires logical_block_ids or search_all=true")
	}
	if p == nil || p.Embedder == nil {
		return nil, errors.New("Zvec native embedder is unavailable")
	}
	rows, err := index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	partitionMap := make(map[string]*recallPartition)
	for _, row := range rows {
		record := row.Record
		if !row.Current || record.BlockMembership == nil {
			continue
		}
		primary := record.BlockMembership.PrimaryBlockID
		if !plan.SearchAll && !slices.Contains(plan.LogicalBlockIDs, primary) {
			continue
		}
		chunks, _ := Chunks(record)
		if row.Backend != "zvec" || row.Status != "committed" || row.Revision < 1 || row.NativeID != chunks[0].ID {
			return nil, fmt.Errorf("Zvec native projection is not current for block %q", primary)
		}
		identity, err := Partition(record)
		if err != nil {
			return nil, err
		}
		partition := partitionMap[identity.PartitionID]
		if partition == nil {
			path, err := CollectionPath(p.RepoRoot, record)
			if err != nil {
				return nil, err
			}
			partition = &recallPartition{id: identity.PartitionID, primary: primary,
				identity: memory.NativePartitionIdentity(identity), path: path,
				records: make(map[string]store.ProjectionRow)}
			partitionMap[identity.PartitionID] = partition
		}
		partition.records[record.BlockID] = row
	}
	partitions := make([]*recallPartition, 0, len(partitionMap))
	for _, partition := range partitionMap {
		partitions = append(partitions, partition)
	}
	sort.Slice(partitions, func(i, j int) bool {
		left, right := partitions[i], partitions[j]
		if !plan.SearchAll {
			li, ri := slices.Index(plan.LogicalBlockIDs, left.primary), slices.Index(plan.LogicalBlockIDs, right.primary)
			if li != ri {
				return li < ri
			}
		}
		if left.primary != right.primary {
			return left.primary < right.primary
		}
		return left.id < right.id
	})
	vectors, err := p.Embedder.Embed(ctx, []string{plan.Query})
	if err != nil {
		return nil, err
	}
	limit := plan.Limit
	if limit == 0 {
		limit = 10
	}
	candidateLimit := max(64, limit*4)
	densePools := make([][]rankedHit, 0, len(partitions))
	lexicalPools := make([][]rankedHit, 0, len(partitions))
	mode := "scoped"
	if plan.SearchAll {
		mode = "search_all"
	}
	for _, partition := range partitions {
		if _, err := os.Stat(partition.path); err != nil {
			return nil, errors.New("Zvec native partition is unavailable")
		}
		collection, err := OpenNativeCollection(partition.path, false)
		if err != nil {
			return nil, err
		}
		started := time.Now()
		dense, lexical, searchErr := collection.SearchCandidates(vectors[0], plan.Query, candidateLimit, plan.RetrievalMode == "hybrid")
		closeErr := collection.Close()
		var densePool, lexicalPool []rankedHit
		admitErr := error(nil)
		if searchErr == nil && closeErr == nil {
			densePool, admitErr = admitCandidates(ctx, index, plan, *partition, dense)
			if admitErr == nil && plan.RetrievalMode == "hybrid" {
				lexicalPool, admitErr = admitCandidates(ctx, index, plan, *partition, lexical)
			}
		}
		opErr := errors.Join(searchErr, closeErr, admitErr)
		metricErr := index.RecordPartitionOperation(ctx, partition.identity, store.PartitionOperation{
			Operation: "recall", LatencyMS: float64(time.Since(started).Microseconds()) / 1000,
			HitCount: len(densePool), Success: opErr == nil, SearchMode: mode})
		if opErr != nil || metricErr != nil {
			return nil, errors.Join(opErr, metricErr)
		}
		densePools = append(densePools, densePool)
		if plan.RetrievalMode == "hybrid" {
			lexicalPools = append(lexicalPools, lexicalPool)
		}
	}
	if plan.RetrievalMode == "hybrid" {
		return mergeHybrid(densePools, lexicalPools, limit), nil
	}
	return mergeDense(densePools, limit), nil
}

func admitCandidates(ctx context.Context, index *store.Index, plan memory.RecallPlan, partition recallPartition, candidates []NativeCandidate) ([]rankedHit, error) {
	pool := make([]rankedHit, 0, len(candidates))
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		if seen[candidate.RecordID] {
			continue
		}
		seen[candidate.RecordID] = true
		projection, ok := partition.records[candidate.RecordID]
		if !ok {
			continue
		}
		canonical := projection.Record
		if err := ValidateCandidate(candidate, canonical, projection.Revision, partition.id); err != nil {
			return nil, err
		}
		stored, err := index.ReadStoredRecord(ctx, canonical.BlockID)
		if err != nil {
			return nil, err
		}
		if stored == nil || !reflect.DeepEqual(*stored, canonical) {
			return nil, errors.New("Zvec native candidate differs from canonical index")
		}
		current, err := index.IsCurrentRecord(ctx, canonical)
		if err != nil {
			return nil, err
		}
		if !current || !memory.EligibleForRecall(canonical, plan, time.Now().UTC()) {
			continue
		}
		if !containsTags(canonical.Tags, plan.Tags) {
			continue
		}
		dependencies, err := index.ValidateDependencies(ctx, canonical.BlockID, store.DependencyOptions{})
		if err != nil {
			return nil, err
		}
		if dependencies.Status != "valid" {
			continue
		}
		similarity := 1 / (1 + math.Max(0, candidate.Score))
		pool = append(pool, rankedHit{blockID: partition.primary, rank: len(pool) + 1,
			score: similarity, record: canonical})
	}
	return pool, nil
}

func containsTags(have, required []string) bool {
	for _, tag := range required {
		if !slices.Contains(have, tag) {
			return false
		}
	}
	return true
}

func mergeDense(pools [][]rankedHit, limit int) []memory.MemoryRecord {
	all := make([]rankedHit, 0)
	for _, pool := range pools {
		all = append(all, pool...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		if all[i].rank != all[j].rank {
			return all[i].rank < all[j].rank
		}
		return all[i].record.BlockID < all[j].record.BlockID
	})
	unique := make([]rankedHit, 0, len(all))
	seen := make(map[string]bool)
	for _, hit := range all {
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
		record.Metadata = copyMetadata(record.Metadata)
		record.Metadata["native_logical_block_id"] = hit.blockID
		record.Metadata["native_rank"] = hit.rank
		record.Metadata["native_score"] = hit.score
		record.Metadata["candidate_pool_ids"] = poolIDs
		record.Metadata["selected_record_ids"] = selectedIDs
		record.Metadata["selection_mode"] = "native_similarity"
		record.Metadata["native_score_mode"] = "cosine_similarity"
		record.Metadata["retrieval_score"] = hit.score
		result = append(result, record)
	}
	return result
}

func mergeHybrid(densePools, lexicalPools [][]rankedHit, limit int) []memory.MemoryRecord {
	dense := make([]rankedHit, 0)
	lexical := make([]rankedHit, 0)
	for _, pool := range densePools {
		dense = append(dense, pool...)
	}
	for _, pool := range lexicalPools {
		lexical = append(lexical, pool...)
	}
	sort.SliceStable(dense, func(i, j int) bool {
		if dense[i].score != dense[j].score {
			return dense[i].score > dense[j].score
		}
		if dense[i].blockID != dense[j].blockID {
			return dense[i].blockID < dense[j].blockID
		}
		return dense[i].record.BlockID < dense[j].record.BlockID
	})
	sort.SliceStable(lexical, func(i, j int) bool {
		if lexical[i].rank != lexical[j].rank {
			return lexical[i].rank < lexical[j].rank
		}
		if lexical[i].blockID != lexical[j].blockID {
			return lexical[i].blockID < lexical[j].blockID
		}
		return lexical[i].record.BlockID < lexical[j].record.BlockID
	})
	type hybridHit struct {
		record memory.MemoryRecord
		ranks  map[string]int
		score  float64
	}
	byID := make(map[string]*hybridHit)
	for _, channel := range []struct {
		name string
		rows []rankedHit
	}{{"semantic", dense}, {"lexical", lexical}} {
		rank := 0
		seen := make(map[string]bool)
		for _, hit := range channel.rows {
			id := hit.record.BlockID
			if seen[id] {
				continue
			}
			seen[id] = true
			rank++
			item := byID[id]
			if item == nil {
				item = &hybridHit{record: hit.record, ranks: make(map[string]int)}
				byID[id] = item
			}
			item.ranks[channel.name] = rank
		}
	}
	ordered := make([]*hybridHit, 0, len(byID))
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
		record.Metadata = copyMetadata(record.Metadata)
		record.Metadata["retrieval_mode"] = "hybrid"
		record.Metadata["native_logical_block_id"] = record.BlockMembership.PrimaryBlockID
		record.Metadata["selection_mode"] = "rrf"
		record.Metadata["native_score_mode"] = "rrf"
		record.Metadata["rrf_constant"] = 60
		record.Metadata["channel_ranks"] = item.ranks
		record.Metadata["retrieval_score"] = item.score
		record.Metadata["candidate_pool_ids"] = poolIDs
		record.Metadata["selected_record_ids"] = selectedIDs
		result = append(result, record)
	}
	return result
}

func copyMetadata(metadata map[string]any) map[string]any {
	copy := make(map[string]any, len(metadata)+10)
	for key, value := range metadata {
		copy[key] = value
	}
	return copy
}
