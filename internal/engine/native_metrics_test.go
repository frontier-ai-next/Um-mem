package engine

import (
	"context"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestNativeWriteRecordsPartitionMetricOnce(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	request := CandidateRequest{Content: "Measured native fact", RequestedBlockID: "stable_project_facts",
		OperationID: "metric-write-1", SessionID: "session-1", RunID: "session-1"}
	first, err := selected.RememberCandidate(ctx, request)
	if err != nil || !first.Saved {
		t.Fatalf("native write failed: %+v, %v", first, err)
	}
	record, err := selected.ReadStored(ctx, first.RecordID)
	if err != nil || record == nil {
		t.Fatalf("canonical record missing: %+v, %v", record, err)
	}
	identity, err := selected.projector.(interface {
		PartitionIdentity(memory.MemoryRecord) (memory.NativePartitionIdentity, error)
	}).PartitionIdentity(*record)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := selected.index.PartitionMetrics(ctx, identity,
		record.BlockMembership.BackendPartitionID, "ready")
	if err != nil || metrics.RecordCount != 1 || metrics.WriteCount != 1 ||
		metrics.RecordBytes == nil || *metrics.RecordBytes <= 0 {
		t.Fatalf("native write metrics were not committed: %+v, %v", metrics, err)
	}
	if _, err := selected.RememberCandidate(ctx, request); err != nil {
		t.Fatal(err)
	}
	metrics, err = selected.index.PartitionMetrics(ctx, identity,
		record.BlockMembership.BackendPartitionID, "ready")
	if err != nil || metrics.WriteCount != 1 {
		t.Fatalf("receipt replay duplicated native write metric: %+v, %v", metrics, err)
	}
	health, err := selected.BlockHealth(ctx, []string{"stable_project_facts", "decisions_and_contracts"})
	if err != nil || health["readiness"] != "not_probed" || health["metrics_scope"] != "native_partition" {
		t.Fatalf("scoped health failed: %+v, %v", health, err)
	}
	rows := health["block_metrics"].([]map[string]any)
	if len(rows) != 1 || rows[0]["primary_block_id"] != "stable_project_facts" ||
		rows[0]["record_count"] != 1 || rows[0]["write_count"] != 1 ||
		rows[0]["health"] != "not_probed" {
		t.Fatalf("scoped metrics leaked or missed the selected leaf: %+v", rows)
	}
	page, err := selected.ExpandBlock(ctx, "stable_project_facts", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	root := page["root"].(map[string]any)
	if root["diagnostics_status"] != "not_probed" || root["metrics_scope"] != "native_partition" ||
		len(root["block_metrics"].([]map[string]any)) != 1 {
		t.Fatalf("expanded page did not include scoped observations: %+v", root)
	}
	full, err := selected.Health(ctx)
	if err != nil || full["native_partition_count"] != 1 || len(full["block_metrics"].([]map[string]any)) != 1 {
		t.Fatalf("backend health did not include native metrics: %+v, %v", full, err)
	}
}
