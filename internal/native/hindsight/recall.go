package hindsight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type recallPartition struct {
	bankID         string
	primaryBlockID string
	identity       memory.NativePartitionIdentity
}

// Recall uses native Hindsight ranking for current semantic queries. Exact
// selectors are resolved from the committed canonical index.
func (p Projector) Recall(ctx context.Context, index *store.Index, plan memory.RecallPlan) ([]memory.MemoryRecord, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	workspaceID, profileID := index.Scope()
	if plan.WorkspaceID != workspaceID || plan.ProfileID != profileID {
		return nil, errors.New("Hindsight recall scope does not match selected canonical index")
	}
	if plan.TemporalMode != "current" || plan.AsOf != "" || plan.KnownAsOf != "" || plan.RevisionID != "" {
		return index.RecallCanonical(ctx, plan, "hindsight")
	}
	if plan.HasExactSelectors() {
		return index.RecallCanonical(ctx, plan, "hindsight")
	}
	if !plan.SearchAll && len(plan.LogicalBlockIDs) == 0 {
		return nil, errors.New("Hindsight native recall requires logical_block_ids or search_all=true")
	}
	rows, err := index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	partitions := make([]recallPartition, 0)
	seenPartitions := make(map[string]bool)
	revisions := make(map[string]int64)
	for _, row := range rows {
		record := row.Record
		if !row.Current || !nativeAllowed(record) || record.BlockMembership == nil {
			continue
		}
		primary := record.BlockMembership.PrimaryBlockID
		if !plan.SearchAll && !slices.Contains(plan.LogicalBlockIDs, primary) {
			continue
		}
		if row.Backend != "hindsight" || row.Status != "committed" ||
			row.NativeID != "UM-MEM:block:"+record.BlockID || row.Revision < 1 {
			return nil, fmt.Errorf("Hindsight native projection is not current for logical block %q", primary)
		}
		revisions[record.BlockID] = row.Revision
		bankID, err := partitionID(record)
		if err != nil {
			return nil, err
		}
		key := primary + "\x00" + bankID
		if !seenPartitions[key] {
			partition := recallPartition{bankID: bankID, primaryBlockID: primary}
			partition.identity, err = p.PartitionIdentity(record)
			if err != nil {
				return nil, err
			}
			seenPartitions[key] = true
			partitions = append(partitions, partition)
		}
	}
	// Preserve caller block order; search-all uses stable partition identity.
	if plan.SearchAll {
		slices.SortFunc(partitions, func(a, b recallPartition) int {
			return strings.Compare(a.primaryBlockID+"\x00"+a.bankID, b.primaryBlockID+"\x00"+b.bankID)
		})
	} else {
		slices.SortFunc(partitions, func(a, b recallPartition) int {
			left := slices.Index(plan.LogicalBlockIDs, a.primaryBlockID)
			right := slices.Index(plan.LogicalBlockIDs, b.primaryBlockID)
			if left != right {
				return left - right
			}
			return strings.Compare(a.bankID, b.bankID)
		})
	}
	limit := plan.Limit
	if limit == 0 {
		limit = 10
	}
	pools := make([][]memory.MemoryRecord, 0, len(partitions))
	for _, partition := range partitions {
		started := time.Now()
		nativeRows, err := p.recallPartition(ctx, partition, plan, revisions)
		mode := "scoped"
		if plan.SearchAll {
			mode = "search_all"
		}
		metricErr := index.RecordPartitionOperation(ctx, partition.identity, store.PartitionOperation{
			Operation: "recall", LatencyMS: float64(time.Since(started).Microseconds()) / 1000,
			HitCount: len(nativeRows), Success: err == nil, SearchMode: mode})
		if err != nil || metricErr != nil {
			return nil, errors.Join(err, metricErr)
		}
		pool := make([]memory.MemoryRecord, 0, min(limit, len(nativeRows)))
		for _, native := range nativeRows {
			canonical, err := index.ReadStoredRecord(ctx, native.BlockID)
			if err != nil {
				return nil, err
			}
			if canonical == nil {
				return nil, errors.New("Hindsight native record is not visible in its canonical index")
			}
			if !recordsEqual(*canonical, native) {
				return nil, errors.New("Hindsight native record differs from canonical index")
			}
			current, err := index.IsCurrentRecord(ctx, *canonical)
			if err != nil {
				return nil, err
			}
			if !current {
				continue
			}
			if !memory.EligibleForRecall(*canonical, plan, time.Now().UTC()) {
				continue
			}
			if !containsAllTags(canonical.Tags, plan.Tags) {
				continue
			}
			dependencies, err := index.ValidateDependencies(ctx, canonical.BlockID, store.DependencyOptions{})
			if err != nil {
				return nil, err
			}
			if dependencies.Status != "valid" {
				continue
			}
			pool = append(pool, *canonical)
			if len(pool) >= limit {
				break
			}
		}
		pools = append(pools, pool)
	}
	result := make([]memory.MemoryRecord, 0, limit)
	seenRecords := make(map[string]bool)
	for rank := 0; len(result) < limit; rank++ {
		found := false
		for _, pool := range pools {
			if rank >= len(pool) {
				continue
			}
			found = true
			record := pool[rank]
			if !seenRecords[record.BlockID] {
				seenRecords[record.BlockID] = true
				result = append(result, record)
				if len(result) == limit {
					break
				}
			}
		}
		if !found {
			break
		}
	}
	return result, nil
}

func (p Projector) recallPartition(ctx context.Context, partition recallPartition, plan memory.RecallPlan, revisions map[string]int64) ([]memory.MemoryRecord, error) {
	budget := plan.MaxTokens
	if budget == 0 {
		budget = 2048
	}
	path := "/v1/default/banks/" + url.PathEscape(partition.bankID) + "/memories/recall"
	response, err := p.request(ctx, http.MethodPost, path, map[string]any{
		"query": plan.Query, "max_tokens": budget, "budget": "low",
		"tags":       []string{"um_mem:block:" + partition.primaryBlockID},
		"tags_match": "all_strict",
	})
	if err != nil {
		return nil, err
	}
	raw, exists := response["results"]
	if !exists || raw == nil {
		return []memory.MemoryRecord{}, nil
	}
	hits, ok := raw.([]any)
	if !ok {
		return nil, errors.New("Hindsight recall returned an invalid results list")
	}
	result := make([]memory.MemoryRecord, 0, len(hits))
	for _, hit := range hits {
		row, ok := hit.(map[string]any)
		if !ok {
			return nil, errors.New("Hindsight recall returned a non-object hit")
		}
		metadata, ok := row["metadata"].(map[string]any)
		if !ok || metadata["projection_marker"] != projectionMarker || metadata["canonical_record_json"] == nil {
			// Hindsight can return derived facts alongside source documents.
			continue
		}
		serialized, ok := metadata["canonical_record_json"].(string)
		if !ok {
			return nil, errors.New("Hindsight native canonical record is invalid")
		}
		record, err := memory.DecodeCanonicalRecord([]byte(serialized))
		if err != nil {
			return nil, fmt.Errorf("Hindsight native canonical record is invalid: %w", err)
		}
		if record.BlockMembership == nil || record.BlockMembership.PrimaryBlockID != partition.primaryBlockID ||
			record.WorkspaceID != plan.WorkspaceID || record.ProfileID != plan.ProfileID ||
			metadata["block_id"] != record.BlockID || metadata["record_id"] != record.BlockID ||
			metadata["workspace_id"] != record.WorkspaceID || metadata["primary_block_id"] != partition.primaryBlockID ||
			metadata["native_partition_id"] != partition.bankID ||
			metadata["catalog_revision_id"] != record.BlockMembership.CatalogRevisionID ||
			metadata["bank_id"] != partition.bankID || revisions[record.BlockID] == 0 ||
			metadata["commit_revision"] != strconv.FormatInt(revisions[record.BlockID], 10) {
			return nil, errors.New("Hindsight native recall row escaped its canonical partition")
		}
		actualBank, err := partitionID(record)
		if err != nil || actualBank != partition.bankID {
			return nil, errors.New("Hindsight native recall row has invalid partition identity")
		}
		if documentID, ok := row["document_id"].(string); ok && documentID != "" {
			if documentID != "UM-MEM:block:"+record.BlockID {
				return nil, errors.New("Hindsight native recall document ID differs from canonical identity")
			}
		} else if text, ok := row["text"].(string); !ok || text != record.Content {
			return nil, errors.New("Hindsight native recall text differs from canonical content")
		}
		if !nativeHitHasTag(row["tags"], "um_mem:block:"+partition.primaryBlockID) {
			return nil, errors.New("Hindsight native protected block tag is missing")
		}
		result = append(result, record)
	}
	return result, nil
}

func recordsEqual(left, right memory.MemoryRecord) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && string(a) == string(b)
}

func containsAllTags(actual, required []string) bool {
	for _, tag := range required {
		if !slices.Contains(actual, tag) {
			return false
		}
	}
	return true
}

func nativeHitHasTag(raw any, expected string) bool {
	if tags, ok := raw.([]any); ok {
		for _, tag := range tags {
			if tag == expected {
				return true
			}
		}
	}
	return false
}
