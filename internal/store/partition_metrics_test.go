package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestExistingIndexRestoresNativePartitionMetricIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite3")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx_native_partition_events_stable_partition",
		"idx_native_partition_events_partition_latency"} {
		if _, err := db.Exec("DROP INDEX " + name); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	rows, err := writer.db.Query("PRAGMA index_list('native_partition_events')")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var sequence, unique, originKey int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &originKey); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found["idx_native_partition_events_stable_partition"] ||
		!found["idx_native_partition_events_partition_latency"] {
		t.Fatalf("native partition metric indexes were not restored: %+v", found)
	}
}

func TestNativePartitionMetricsAggregateDurableOperations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.sqlite3")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	identity := memory.NativePartitionIdentity{BackendID: "mempalace", WorkspaceID: "alpha",
		PrimaryBlockID: "leaf", CatalogRevisionID: "catalog-1", PartitionID: "native-1",
		IsolationClass: "physical", PartitionMode: "native_collection_family"}
	bytesA, bytesB := int64(42), int64(43)
	for _, event := range []PartitionOperation{
		{Operation: "write", LatencyMS: 10, RecordBytes: &bytesA, Success: true},
		{Operation: "write", LatencyMS: 20, RecordBytes: &bytesB, Success: true},
		{Operation: "recall", LatencyMS: 30, HitCount: 2, SearchMode: "scoped", Success: true},
		{Operation: "recall", LatencyMS: 40, SearchMode: "search_all", Success: true},
		{Operation: "recall", LatencyMS: 50, SearchMode: "scoped", Success: false},
		{Operation: "render", RenderedTokens: 17, Success: true},
		{Operation: "consolidation", Success: true}, {Operation: "compaction", Success: true},
		{Operation: "eviction", Success: true}, {Operation: "expiry", Success: true},
	} {
		if err := writer.RecordPartitionOperation(ctx, identity, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	metrics, err := index.PartitionMetrics(ctx, identity, "stable-key", "ready")
	if err != nil || metrics.RecordCount != 0 || metrics.WriteCount != 2 ||
		metrics.RecallCount != 3 || metrics.HitCount != 2 || metrics.ZeroHitCount != 1 ||
		metrics.FailureCount != 1 || metrics.RecordBytes == nil || *metrics.RecordBytes != 85 ||
		metrics.LatencyP50MS != 30 || metrics.LatencyP95MS != 50 ||
		metrics.RenderedTokens != 17 || metrics.ConsolidationCount != 1 ||
		metrics.CompactionCount != 1 || metrics.EvictionCount != 1 || metrics.ExpiryCount != 1 ||
		metrics.ScopedRecallCount != 2 || metrics.BroadRecallCount != 1 ||
		metrics.LastAccessedAt == "" || metrics.Health != "ready" {
		t.Fatalf("native partition metrics changed: %+v, %v", metrics, err)
	}
	if err := writer.RecordPartitionOperation(ctx, identity,
		PartitionOperation{Operation: "recall", SearchMode: "unscoped", Success: true}); err == nil {
		t.Fatal("invalid native search mode was accepted")
	}
}
